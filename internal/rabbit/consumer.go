package rabbit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// consumerChannel is the subset of *amqp.Channel the supervised consumer
// depends on. Declared as an interface so unit tests can drive channel
// lifecycle events (broker-initiated close, consumer cancel) deterministically;
// *amqp.Channel satisfies it directly.
type consumerChannel interface {
	NotifyClose(receiver chan *amqp.Error) chan *amqp.Error
	NotifyCancel(receiver chan string) chan string
	Close() error
}

// consumeOpen opens a consumer channel for queueName: optional QoS, then
// Consume. Used both for the initial channel and for every self-heal rebuild.
type consumeOpen func(queueName string) (consumerChannel, <-chan amqp.Delivery, error)

// consumeDispatch handles one delivery (the listener-specific handler logic).
type consumeDispatch func(ctx context.Context, d amqp.Delivery)

// supervisedConsumer runs the consume loop for one listener and keeps its
// channel alive: when the broker closes the consumer's channel or cancels its
// consumer while the connection is still healthy, the channel is rebuilt with
// exponential backoff. A connection-level loss is left to the application's
// reconnect hook, which calls start again and supersedes this supervisor via
// its stop channel — so two supervisors can never consume the same queue at
// once.
type supervisedConsumer struct {
	conn           *Connection
	log            *slog.Logger
	wg             *sync.WaitGroup
	name           string // listener kind ("event", "command", …) for log lines
	open           consumeOpen
	dispatch       consumeDispatch
	ensureTopology func() error // re-declare topology before a heal attempt; nil skips

	// Rebuild backoff schedule. Seeded from the production defaults; unit
	// tests shorten them by overriding the fields directly.
	initialBackoff time.Duration
	maxBackoff     time.Duration

	// mu guards stop. start closes the previous supervisor's stop channel
	// before swapping in a new one.
	mu   sync.Mutex
	stop chan struct{}
}

func newSupervisedConsumer(conn *Connection, log *slog.Logger, wg *sync.WaitGroup, name string, open consumeOpen, dispatch consumeDispatch, ensureTopology func() error) *supervisedConsumer {
	if log == nil {
		log = slog.Default()
	}
	return &supervisedConsumer{
		conn:           conn,
		log:            log,
		wg:             wg,
		name:           name,
		open:           open,
		dispatch:       dispatch,
		ensureTopology: ensureTopology,
		initialBackoff: 1 * time.Second,
		maxBackoff:     30 * time.Second,
	}
}

// start opens the first consumer channel and launches the supervisor goroutine.
// It returns an error when the initial open fails, so the caller can retry with
// its own backoff. Every start supersedes any previous supervisor: the old one
// stops consuming on its next select iteration, before the new one is created.
func (c *supervisedConsumer) start(ctx context.Context, queueName string) error {
	c.mu.Lock()
	if c.stop != nil {
		close(c.stop)
	}
	stop := make(chan struct{})
	c.stop = stop
	c.mu.Unlock()

	ch, deliveries, err := c.open(queueName)
	if err != nil {
		return err
	}

	if c.wg != nil {
		c.wg.Add(1)
	}
	go c.supervise(ctx, queueName, stop, ch, deliveries)
	return nil
}

// supervise runs one consume loop per channel. When the channel dies it either
// exits (connection down or superseded — the reconnect hook owns recovery) or
// self-heals: topology is re-declared and a fresh channel consumed, retrying
// with exponential backoff until success, ctx cancellation, or supersession.
// Every failure is logged at error level — a half-restored listener must not
// look healthy.
func (c *supervisedConsumer) supervise(ctx context.Context, queueName string, stop chan struct{}, ch consumerChannel, deliveries <-chan amqp.Delivery) {
	if c.wg != nil {
		defer c.wg.Done()
	}
	defer func() { _ = ch.Close() }()

	for {
		closes := ch.NotifyClose(make(chan *amqp.Error, 1))
		cancels := ch.NotifyCancel(make(chan string, 1))

		// logDeath describes how the current channel died. Emitted only when
		// the connection is still healthy — on a connection drop the
		// reconnect machinery logs the outage, so per-consumer lines would
		// just duplicate it.
		var logDeath func()

	consume:
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				// Superseded by a newer start (e.g. the connection-level
				// restore path) — that supervisor owns the listener now.
				return
			case d, ok := <-deliveries:
				if !ok {
					logDeath = func() {
						c.log.Error("reactive-commons: consumer deliveries channel closed by broker", "listener", c.name, "queue", queueName)
					}
					break consume
				}
				c.dispatch(ctx, d)
			case err, ok := <-closes:
				if ok && err != nil {
					logDeath = func() {
						c.log.Error("reactive-commons: consumer channel closed by broker", "listener", c.name, "queue", queueName, "error", err)
					}
				}
				break consume
			case tag, ok := <-cancels:
				if ok {
					logDeath = func() {
						c.log.Error("reactive-commons: consumer cancelled by broker", "listener", c.name, "queue", queueName, "consumerTag", tag)
					}
				}
				break consume
			}
		}

		_ = ch.Close()

		if !c.conn.IsOpen() {
			// The connection is down: the reconnect hook restarts consumers
			// once it returns, so self-healing here would race it.
			return
		}
		if logDeath != nil {
			logDeath()
		}

		backoff := c.initialBackoff
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-time.After(backoff):
			}

			if c.ensureTopology != nil {
				if err := c.ensureTopology(); err != nil {
					c.log.Error("reactive-commons: failed to restore topology for consumer; retrying", "listener", c.name, "queue", queueName, "error", err, "delay", backoff)
					backoff = min(backoff*2, c.maxBackoff)
					continue
				}
			}

			newCh, newDeliveries, err := c.open(queueName)
			if err != nil {
				c.log.Error("reactive-commons: failed to rebuild consumer channel; retrying", "listener", c.name, "queue", queueName, "error", err, "delay", backoff)
				backoff = min(backoff*2, c.maxBackoff)
				continue
			}

			ch, deliveries = newCh, newDeliveries
			c.log.Info("reactive-commons: consumer channel rebuilt", "listener", c.name, "queue", queueName)
			break
		}
	}
}
