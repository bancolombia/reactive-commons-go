package rabbit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// senderChannel is the subset of *amqp.Channel the Sender depends on. Declared
// as an interface so unit tests can drive channel lifecycle events (broker
// confirmations, broker-initiated close) deterministically; *amqp.Channel
// satisfies it directly.
type senderChannel interface {
	Confirm(noWait bool) error
	NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation
	NotifyClose(receiver chan *amqp.Error) chan *amqp.Error
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	IsClosed() bool
	Close() error
}

// errSenderChannelClosed is the transient error released to in-flight
// SendWithConfirm waiters when the backing channel dies (broker-rejected
// publish, operator action, or the underlying connection dropping).
var errSenderChannelClosed = fmt.Errorf("%w: publisher channel closed by broker", async.ErrBrokerUnavailable)

// Sender publishes messages with publisher confirms.
//
// The Sender owns one AMQP channel at a time, identified by a monotonically
// increasing generation counter. A watcher goroutine per generation observes
// broker confirmations and channel closure:
//
//   - a broker confirm releases the matching SendWithConfirm waiter;
//   - a channel close releases every in-flight waiter with a transient error
//     instead of leaving them blocked until their context expires;
//   - if the connection is still open, the channel is rebuilt with
//     exponential backoff (self-heal), retrying until it succeeds, so a
//     transient rebuild failure cannot leave the sender permanently dead.
//
// On a full connection drop the reconnect hook calls Reconnect once the
// connection has been re-established; a failed rebuild there retries in the
// background the same way.
type Sender struct {
	mu   sync.Mutex
	conn *Connection
	ch   senderChannel

	// rebuildMu serializes channel construction (open/confirm/close), keeping
	// the slow network RPCs out of s.mu so sends never block on them.
	rebuildMu sync.Mutex
	cfg       Config
	log       *slog.Logger
	nextTag   uint64
	pending   map[uint64]chan error
	gen       uint64

	// publishSem serializes register+publish pairs so nextTag ordering matches
	// the broker's delivery-tag sequence. A channel-based semaphore (not a
	// sync.Mutex) keeps the queue wait context-aware: a send queued behind a
	// wedged publisher returns at its own deadline instead of hanging on the
	// broker's context-unaware write.
	publishSem chan struct{}

	// rebuildDelay is the initial pause before rebuilding a dead channel and
	// the base of the rebuild retry backoff; rebuildMaxDelay caps it. The
	// backoff keeps a broker that kills the channel on sight (e.g. a
	// persistent topology conflict) from spinning the rebuild hot.
	rebuildDelay    time.Duration
	rebuildMaxDelay time.Duration

	// Test seams; nil means the production behavior.
	openCh   func() (senderChannel, error)
	connOpen func() bool
}

// NewSender creates a Sender with its own publisher-confirm channel and starts
// the channel watcher.
func NewSender(conn *Connection, cfg Config) (*Sender, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Sender{
		conn:            conn,
		cfg:             cfg,
		log:             log,
		pending:         make(map[uint64]chan error),
		publishSem:      make(chan struct{}, 1),
		rebuildDelay:    time.Second,
		rebuildMaxDelay: 30 * time.Second,
	}
	s.openCh = s.openChannel
	s.connOpen = conn.IsOpen

	if _, err := s.rebuild(0, false); err != nil {
		return nil, err
	}
	return s, nil
}

// openChannel opens a channel from the connection and enables publisher confirms.
func (s *Sender) openChannel() (senderChannel, error) {
	ch, err := s.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("reactive-commons: create sender channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("reactive-commons: enable publisher confirms: %w", err)
	}
	return ch, nil
}

// Reconnect rebuilds the Sender's underlying publisher-confirm channel after
// the broker connection has been re-established. The AMQP library closes the
// previous channel when the connection drops, so any subsequent publish would
// otherwise fail with "channel/connection is not open" forever. This method:
//
//   - releases any in-flight SendWithConfirm waiters with a transient error so
//     callers fail fast instead of blocking until ctx expires;
//   - resets the local delivery-tag counter (the broker also resets its own
//     sequence on a fresh channel);
//   - opens a new channel, re-enables publisher confirms, and starts a new
//     watcher goroutine on it.
//
// If the rebuild fails, a background retry loop keeps trying with exponential
// backoff — a single failed attempt must not leave the sender dead until the
// next connection drop.
//
// The previous watcher exits on its own when the AMQP library closes the old
// listener channels.
func (s *Sender) Reconnect() error {
	// Drain pending waiters before the open: even if the rebuild below fails,
	// callers must not keep waiting on the dead channel.
	s.mu.Lock()
	s.failPendingLocked()
	gen := s.gen
	s.mu.Unlock()

	installed, err := s.rebuild(gen, false)
	if err != nil {
		go s.retryRebuild(gen)
	}
	_ = installed // false means another rebuild owns the channel
	return err
}

// rebuild opens a fresh publisher-confirm channel and installs it when
// expectedGen is still current. Channel construction and the close of the old
// channel run outside s.mu so sends are never blocked by broker RPCs.
//
// requireOpen makes the rebuild abort while the connection is down: background
// retries use it, while the initial open and Reconnect always attempt (they run
// immediately after a successful dial).
//
// The confirm and close listeners are registered synchronously before the
// channel is exposed: amqp091 drops confirmations that arrive while no
// NotifyPublish listener is registered.
func (s *Sender) rebuild(expectedGen uint64, requireOpen bool) (installed bool, err error) {
	s.rebuildMu.Lock()
	defer s.rebuildMu.Unlock()

	s.mu.Lock()
	stale := s.gen != expectedGen
	s.mu.Unlock()
	if stale || (requireOpen && !s.connOpen()) {
		return false, nil
	}

	ch, err := s.openCh()
	if err != nil {
		return false, err
	}

	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 256))
	closes := ch.NotifyClose(make(chan *amqp.Error, 1))
	if ch.IsClosed() {
		_ = ch.Close()
		return false, fmt.Errorf("reactive-commons: publisher channel closed while rebuilding")
	}

	s.mu.Lock()
	if s.gen != expectedGen || (requireOpen && !s.connOpen()) {
		s.mu.Unlock()
		_ = ch.Close()
		return false, nil
	}
	old := s.ch
	s.failPendingLocked()
	s.ch = ch
	s.gen++
	newGen := s.gen
	s.mu.Unlock()

	if old != nil && !old.IsClosed() {
		_ = old.Close()
	}
	go s.watchChannel(newGen, confirms, closes)
	return true, nil
}

// failPendingLocked releases every in-flight SendWithConfirm waiter with a
// transient error and resets the delivery-tag counter. Caller must hold s.mu.
func (s *Sender) failPendingLocked() {
	for tag, ch := range s.pending {
		select {
		case ch <- errSenderChannelClosed:
		default:
		}
		delete(s.pending, tag)
	}
	s.nextTag = 0
}

// watchChannel dispatches broker confirmations and watches channel closure for
// one channel generation. The AMQP library closes both listener channels when
// the channel shuts down, so either branch may observe the death; the
// generation guard keeps the cleanup single-shot per generation.
func (s *Sender) watchChannel(gen uint64, confirms chan amqp.Confirmation, closes chan *amqp.Error) {
	for {
		select {
		case c, ok := <-confirms:
			if !ok {
				s.onChannelClosed(gen)
				return
			}
			s.dispatchConfirm(gen, c)
		case err, ok := <-closes:
			if !ok {
				s.onChannelClosed(gen)
				return
			}
			if err != nil {
				s.log.Error("reactive-commons: publisher channel closed by broker", "error", err)
			}
			s.onChannelClosed(gen)
			return
		}
	}
}

// dispatchConfirm releases the SendWithConfirm waiter registered for the
// confirmed delivery tag. Confirms from a superseded channel generation are
// dropped: their waiters were already released by failPendingLocked, and the
// tag space has been reused by the newer channel.
func (s *Sender) dispatchConfirm(gen uint64, c amqp.Confirmation) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return
	}
	ch, ok := s.pending[c.DeliveryTag]
	if ok {
		delete(s.pending, c.DeliveryTag)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if c.Ack {
		ch <- nil
	} else {
		ch <- fmt.Errorf("%w: broker nacked delivery tag %d", async.ErrBrokerUnavailable, c.DeliveryTag)
	}
}

// onChannelClosed runs once per channel generation: it releases in-flight
// waiters and, when the connection is still healthy, self-heals the channel
// with exponential backoff. Runs in the watcher goroutine.
func (s *Sender) onChannelClosed(gen uint64) {
	s.mu.Lock()
	if s.gen != gen {
		s.mu.Unlock()
		return
	}
	s.failPendingLocked()
	s.mu.Unlock()

	if !s.connOpen() {
		// The connection is down or stopping: the reconnect hook rebuilds
		// the channel once the connection returns.
		return
	}

	s.retryRebuild(gen)
}

// retryRebuild repeatedly attempts rebuildLocked with exponential backoff
// until it succeeds, the sender generation moves on (a newer Reconnect owns
// the channel now), or the connection drops. Runs in the calling goroutine —
// the channel watcher for self-heals and a background goroutine for failed
// Reconnect calls — so the reconnect hook is never blocked.
func (s *Sender) retryRebuild(gen uint64) {
	backoff := s.rebuildDelay
	for {
		time.Sleep(backoff)

		installed, err := s.rebuild(gen, true)
		if err == nil {
			if installed {
				s.log.Info("reactive-commons: publisher channel rebuilt")
			}
			// Not installed means the generation moved on or the connection
			// dropped: another owner is responsible for recovery.
			return
		}
		s.log.Error("reactive-commons: failed to rebuild publisher channel; retrying", "error", err, "delay", backoff)
		backoff = min(backoff*2, s.rebuildMaxDelay)
	}
}

// SendWithConfirm publishes a message and waits for broker acknowledgement.
// Safe for concurrent use; multiple goroutines may call this simultaneously.
func (s *Sender) SendWithConfirm(ctx context.Context, body []byte, exchange, routingKey string, headers amqp.Table, persistent bool) error {
	mode := amqp.Transient
	if persistent {
		mode = amqp.Persistent
	}

	// The publish slot serializes register+publish pairs so nextTag ordering
	// matches the broker's delivery-tag sequence. The wait is context-aware:
	// a send queued behind a wedged publisher returns at its own deadline.
	if !s.acquirePublishSlot(ctx) {
		return ctx.Err()
	}

	// Register the pending slot under s.mu; the publish itself runs outside
	// the lock so a wedged write can never block confirm dispatch or the
	// channel-close drain.
	s.mu.Lock()
	s.nextTag++
	tag := s.nextTag
	resultCh := make(chan error, 1)
	s.pending[tag] = resultCh
	ch := s.ch
	s.mu.Unlock()

	// ctx may have expired while waiting for the publish slot. Roll the tag
	// back before releasing the slot so the next send keeps its place in the
	// broker's delivery-tag sequence.
	if err := ctx.Err(); err != nil {
		s.rollbackTag(tag, resultCh)
		s.releasePublishSlot()
		return err
	}
	if ch == nil {
		s.rollbackTag(tag, resultCh)
		s.releasePublishSlot()
		return fmt.Errorf("%w: publisher channel not initialized", async.ErrBrokerUnavailable)
	}

	err := ch.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: uint8(mode),
		Timestamp:    time.Now(),
		MessageId:    uuid.NewString(),
		Headers:      headers,
		AppId:        s.cfg.AppName,
		Body:         body,
	})
	if err != nil {
		// Roll back before releasing the slot: no other register can
		// interleave, so the decrement cannot corrupt nextTag.
		s.rollbackTag(tag, resultCh)
		s.releasePublishSlot()
		return fmt.Errorf("%w: publish to %q/%q: %s", async.ErrBrokerUnavailable, exchange, routingKey, err)
	}
	// The slot is released only after the publish, so the next send registers
	// the very tag the broker will assign to its publish. The confirm wait
	// below holds neither the slot nor any lock.
	s.releasePublishSlot()

	select {
	case result := <-resultCh:
		return result
	case <-ctx.Done():
		s.mu.Lock()
		if s.pending[tag] == resultCh {
			delete(s.pending, tag)
		}
		s.mu.Unlock()
		return ctx.Err()
	}
}

// SendNoConfirm publishes a message without waiting for broker acknowledgement.
// Best-effort: a publish that races a broker-initiated channel close can be
// lost without an error, but subsequent sends fail fast and the channel
// self-heals. Used only for notifications, where "no listener bound" is a
// valid outcome; every other send path uses SendWithConfirm.
func (s *Sender) SendNoConfirm(ctx context.Context, body []byte, exchange, routingKey string, headers amqp.Table, persistent bool) error {
	mode := amqp.Transient
	if persistent {
		mode = amqp.Persistent
	}

	if !s.acquirePublishSlot(ctx) {
		return ctx.Err()
	}
	defer s.releasePublishSlot()

	// Snapshot the channel under s.mu; a reconnect may swap it immediately
	// after, in which case the publish fails fast on the closed channel.
	s.mu.Lock()
	ch := s.ch
	s.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("%w: publisher channel not initialized", async.ErrBrokerUnavailable)
	}

	return ch.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: uint8(mode),
		Timestamp:    time.Now(),
		MessageId:    uuid.NewString(),
		Headers:      headers,
		AppId:        s.cfg.AppName,
		Body:         body,
	})
}

// acquirePublishSlot blocks until the publish slot is free or ctx is done.
func (s *Sender) acquirePublishSlot(ctx context.Context) bool {
	select {
	case s.publishSem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Sender) releasePublishSlot() { <-s.publishSem }

// rollbackTag removes a pending entry for a publish that never reached the
// broker and rolls the delivery-tag counter back. The removal is guarded by
// pointer identity so a reconnect that drained the waiter and reset nextTag in
// between is never corrupted: while the caller holds the publish slot no other
// send registers, so a still-matching entry implies nextTag is still tag.
func (s *Sender) rollbackTag(tag uint64, resultCh chan error) {
	s.mu.Lock()
	if s.pending[tag] == resultCh {
		delete(s.pending, tag)
		if s.nextTag == tag {
			s.nextTag--
		}
	}
	s.mu.Unlock()
}
