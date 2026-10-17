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
//   - if the connection is still open, the channel is rebuilt after a short
//     backoff (self-heal), so publishing recovers without a connection drop.
//
// On a full connection drop the reconnect hook calls Reconnect once the
// connection has been re-established.
type Sender struct {
	mu      sync.Mutex
	conn    *Connection
	ch      senderChannel
	cfg     Config
	log     *slog.Logger
	nextTag uint64
	pending map[uint64]chan error
	gen     uint64

	// rebuildDelay is the pause before rebuilding a dead channel while the
	// connection is still healthy, so a broker that keeps killing the channel
	// (e.g. a persistent topology conflict) cannot spin the rebuild hot.
	rebuildDelay time.Duration

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
		conn:         conn,
		cfg:          cfg,
		log:          log,
		pending:      make(map[uint64]chan error),
		rebuildDelay: time.Second,
	}
	s.openCh = s.openChannel
	s.connOpen = conn.IsOpen

	if err := s.rebuildLocked(); err != nil {
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
// The previous watcher exits on its own when the AMQP library closes the old
// listener channels.
func (s *Sender) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ch != nil {
		_ = s.ch.Close()
	}

	s.failPendingLocked()
	return s.rebuildLocked()
}

// rebuildLocked opens a fresh publisher-confirm channel and starts its watcher
// under a new generation. Caller must hold s.mu.
func (s *Sender) rebuildLocked() error {
	ch, err := s.openCh()
	if err != nil {
		return err
	}
	s.ch = ch
	s.nextTag = 0
	s.gen++
	go s.watchChannel(ch, s.gen)
	return nil
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
func (s *Sender) watchChannel(ch senderChannel, gen uint64) {
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 256))
	closes := ch.NotifyClose(make(chan *amqp.Error, 1))

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
// after a short backoff. Runs in the watcher goroutine; the mutex is not held
// across the sleep.
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

	time.Sleep(s.rebuildDelay)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		return // Reconnect already swapped in a newer channel.
	}
	if err := s.rebuildLocked(); err != nil {
		s.log.Error("reactive-commons: failed to self-heal publisher channel", "error", err)
		// The channel stays dead; publishes fail fast and the next
		// connection-level reconnect restores it.
	}
}

// SendWithConfirm publishes a message and waits for broker acknowledgement.
// Safe for concurrent use; multiple goroutines may call this simultaneously.
func (s *Sender) SendWithConfirm(ctx context.Context, body []byte, exchange, routingKey string, headers amqp.Table, persistent bool) error {
	mode := amqp.Transient
	if persistent {
		mode = amqp.Persistent
	}

	// Register pending slot and publish under the mutex to keep nextTag in sync
	// with the broker's internal delivery-tag sequence.
	s.mu.Lock()
	s.nextTag++
	tag := s.nextTag
	resultCh := make(chan error, 1)
	s.pending[tag] = resultCh

	err := s.ch.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: uint8(mode),
		Timestamp:    time.Now(),
		MessageId:    uuid.NewString(),
		Headers:      headers,
		AppId:        s.cfg.AppName,
		Body:         body,
	})
	if err != nil {
		delete(s.pending, tag)
		s.nextTag-- // roll back so next publish tag stays in sync with broker
		s.mu.Unlock()
		return fmt.Errorf("%w: publish to %q/%q: %s", async.ErrBrokerUnavailable, exchange, routingKey, err)
	}
	s.mu.Unlock()

	// Wait for the broker confirm outside the mutex — allows other goroutines to publish.
	select {
	case result := <-resultCh:
		return result
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, tag)
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

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.ch.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: uint8(mode),
		Timestamp:    time.Now(),
		MessageId:    uuid.NewString(),
		Headers:      headers,
		AppId:        s.cfg.AppName,
		Body:         body,
	})
}
