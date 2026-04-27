package rabbit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Sender publishes messages with publisher confirms.
// It uses a single AMQP channel whose confirms are dispatched by a background
// goroutine keyed by delivery tag — allowing multiple goroutines to call
// SendWithConfirm concurrently without deadlocking.
//
// On a broker reconnect the channel that backs this Sender is closed by the
// AMQP library; callers must invoke Reconnect to swap in a fresh channel
// (and reset the broker-side delivery-tag sequence) before publishing again.
type Sender struct {
	mu      sync.Mutex
	conn    *Connection
	ch      *amqp.Channel
	cfg     Config
	nextTag uint64
	pending map[uint64]chan error
}

// NewSender creates a Sender with its own publisher-confirm channel.
// A background goroutine is started to dispatch broker confirms.
func NewSender(conn *Connection, cfg Config) (*Sender, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("reactive-commons: create sender channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("reactive-commons: enable publisher confirms: %w", err)
	}

	s := &Sender{
		conn:    conn,
		ch:      ch,
		cfg:     cfg,
		pending: make(map[uint64]chan error),
	}

	// Register a single confirm listener once; the background goroutine dispatches
	// confirmations to the correct per-call channel by delivery tag.
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 256))
	go s.dispatchConfirms(confirms)

	return s, nil
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
//     dispatchConfirms goroutine on it.
//
// The previous dispatchConfirms goroutine exits on its own when the AMQP
// library closes the old NotifyPublish channel.
func (s *Sender) Reconnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ch != nil {
		_ = s.ch.Close()
	}

	for tag, ch := range s.pending {
		select {
		case ch <- fmt.Errorf("reactive-commons: publisher channel reset by reconnect"):
		default:
		}
		delete(s.pending, tag)
	}
	s.nextTag = 0

	ch, err := s.conn.Channel()
	if err != nil {
		return fmt.Errorf("reactive-commons: rebuild sender channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return fmt.Errorf("reactive-commons: re-enable publisher confirms: %w", err)
	}
	s.ch = ch

	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 256))
	go s.dispatchConfirms(confirms)
	return nil
}

// dispatchConfirms runs until the confirms channel is closed (channel/connection shutdown).
func (s *Sender) dispatchConfirms(confirms <-chan amqp.Confirmation) {
	for c := range confirms {
		s.mu.Lock()
		ch, ok := s.pending[c.DeliveryTag]
		if ok {
			delete(s.pending, c.DeliveryTag)
		}
		s.mu.Unlock()
		if ok {
			if c.Ack {
				ch <- nil
			} else {
				ch <- fmt.Errorf("reactive-commons: broker nacked delivery tag %d", c.DeliveryTag)
			}
		}
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
		return fmt.Errorf("%w: publish to %q/%q: %s", ErrNotConnected, exchange, routingKey, err)
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
