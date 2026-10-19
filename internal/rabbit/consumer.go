package rabbit

import (
	"context"
	"errors"
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

// openConsumerChannel opens a fresh channel for queueName and starts a
// consumer on it. prefetch > 0 enables per-consumer QoS; autoAck and
// exclusive pass through to Consume. Shared by all five listeners — their
// channels differ only in these settings.
func openConsumerChannel(conn *Connection, prefetch int, queueName string, autoAck, exclusive bool) (consumerChannel, <-chan amqp.Delivery, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, nil, err
	}
	if prefetch > 0 {
		if err = ch.Qos(prefetch, 0, false); err != nil {
			_ = ch.Close()
			return nil, nil, err
		}
	}
	deliveries, err := ch.Consume(queueName, "", autoAck, exclusive, false, false, nil)
	if err != nil {
		_ = ch.Close()
		return nil, nil, err
	}
	return ch, deliveries, nil
}

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

	// supersedeWait bounds how long start waits for a superseded supervisor to
	// finish its in-flight dispatch. A handler that does not return must not
	// block consumer restoral forever.
	supersedeWait time.Duration

	// mu guards stop and done. start closes the previous supervisor's stop
	// channel, swaps in the new pair, and waits for the previous done channel
	// before opening a new consumer.
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}

	// stateMu guards the health snapshot and the supervisor generation. Every
	// health update is tagged with a generation so a late-exiting superseded
	// supervisor cannot mark a newer one stopped.
	stateMu    sync.RWMutex
	state      ConsumerState
	stateSince time.Time
	lastErr    string
	runGen     uint64
}

func newSupervisedConsumer(conn *Connection, log *slog.Logger, wg *sync.WaitGroup, name string, open consumeOpen, dispatch consumeDispatch, ensureTopology func() error) *supervisedConsumer {
	if log == nil {
		log = slog.Default()
	}
	c := &supervisedConsumer{
		conn:           conn,
		log:            log,
		wg:             wg,
		name:           name,
		open:           open,
		dispatch:       dispatch,
		ensureTopology: ensureTopology,
		initialBackoff: 1 * time.Second,
		maxBackoff:     30 * time.Second,
		supersedeWait:  30 * time.Second,
		state:          ConsumerStopped,
		stateSince:     time.Now(),
	}
	return c
}

// start opens the first consumer channel and launches the supervisor goroutine.
// It returns an error when the initial open fails, so the caller can retry with
// its own backoff. Every start supersedes any previous supervisor: the old one
// is stopped and its exit is awaited before the new consumer is opened, so two
// supervisors can never consume the same queue at once.
func (c *supervisedConsumer) start(ctx context.Context, queueName string) error {
	c.mu.Lock()
	var prevDone <-chan struct{}
	if c.stop != nil {
		close(c.stop)
		prevDone = c.done
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	c.stop, c.done = stop, done

	// Allocate the generation under the same lock that orders supersession, so
	// concurrent starts cannot leave the older call owning the newest health
	// generation. Lock order is mu -> stateMu; health snapshots take stateMu
	// only.
	c.stateMu.Lock()
	c.runGen++
	gen := c.runGen
	c.setHealthLocked(ConsumerRebuilding, nil)
	c.stateMu.Unlock()
	c.mu.Unlock()

	// Wait for the superseded supervisor to fully exit — closing its channel
	// and finishing any in-flight dispatch — before opening the new consumer.
	// Opening first would let the two briefly consume the same queue and
	// double-process prefetched manual-ack messages. The wait is bounded: a
	// handler that never returns must not prevent recovery; at-least-once
	// semantics already permit the resulting duplicate delivery.
	if prevDone != nil {
		timer := time.NewTimer(c.supersedeWait)
		select {
		case <-prevDone:
			timer.Stop()
		case <-ctx.Done():
			timer.Stop()
			c.setHealthForGen(gen, ConsumerStopped, ctx.Err())
			return ctx.Err()
		case <-timer.C:
			c.log.Warn("reactive-commons: previous consumer did not stop within supersede wait; starting a new consumer (possible duplicate delivery)",
				"listener", c.name, "queue", queueName, "wait", c.supersedeWait)
		}
	}

	ch, deliveries, err := c.open(queueName)
	if err != nil {
		c.setHealthForGen(gen, ConsumerStopped, err)
		return err
	}

	// A newer start or shutdown may have superseded us while the channel was
	// opening; do not launch a consumer nobody owns.
	select {
	case <-ctx.Done():
		_ = ch.Close()
		c.setHealthForGen(gen, ConsumerStopped, ctx.Err())
		return ctx.Err()
	case <-stop:
		_ = ch.Close()
		c.setHealthForGen(gen, ConsumerStopped, nil)
		return nil
	default:
	}

	if c.wg != nil {
		c.wg.Add(1)
	}
	// Mark running before launching so Health() is accurate the instant a
	// caller observes a successful start (e.g. when Ready() closes). The
	// supervisor re-affirms this on every channel.
	c.setHealthForGen(gen, ConsumerRunning, nil)
	go c.supervise(ctx, queueName, gen, stop, done, ch, deliveries)
	return nil
}

// supervise runs one consume loop per channel. When the channel dies it either
// exits (connection down or superseded — the reconnect hook owns recovery) or
// self-heals: topology is re-declared and a fresh channel consumed, retrying
// with exponential backoff until success, ctx cancellation, or supersession.
// Every failure is logged at error level — a half-restored listener must not
// look healthy.
func (c *supervisedConsumer) supervise(ctx context.Context, queueName string, gen uint64, stop, done chan struct{}, ch consumerChannel, deliveries <-chan amqp.Delivery) {
	// Order matters: close(done) is registered before wg.Done so the wait in
	// start observes the supervisor's exit only after the shutdown WaitGroup
	// has been released.
	defer close(done)
	defer func() { c.setHealthForGen(gen, ConsumerStopped, ctx.Err()) }()
	if c.wg != nil {
		defer c.wg.Done()
	}
	defer func() { _ = ch.Close() }()

	for {
		closes := ch.NotifyClose(make(chan *amqp.Error, 1))
		cancels := ch.NotifyCancel(make(chan string, 1))
		c.setHealthForGen(gen, ConsumerRunning, nil)

		// logDeath describes how the current channel died. Emitted only when
		// the connection is still healthy — on a connection drop the
		// reconnect machinery logs the outage, so per-consumer lines would
		// just duplicate it.
		var logDeath func()
		var deathReason string

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
					deathReason = "deliveries channel closed by broker"
					logDeath = func() {
						c.log.Error("reactive-commons: consumer deliveries channel closed by broker", "listener", c.name, "queue", queueName)
					}
					break consume
				}
				c.dispatch(ctx, d)
			case err, ok := <-closes:
				if ok && err != nil {
					deathReason = err.Error()
					logDeath = func() {
						c.log.Error("reactive-commons: consumer channel closed by broker", "listener", c.name, "queue", queueName, "error", err)
					}
				} else if !ok {
					deathReason = "channel close listener closed"
				}
				break consume
			case tag, ok := <-cancels:
				if ok {
					deathReason = "consumer cancelled by broker: " + tag
					logDeath = func() {
						c.log.Error("reactive-commons: consumer cancelled by broker", "listener", c.name, "queue", queueName, "consumerTag", tag)
					}
				} else {
					deathReason = "consumer cancel listener closed"
				}
				break consume
			}
		}

		_ = ch.Close()

		if deathReason != "" {
			c.setHealthForGen(gen, ConsumerRebuilding, errors.New(deathReason))
		}
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

			// A newer start may have superseded us while we slept. Bail out
			// before opening another consumer; the start-side wait guarantees
			// it, this check avoids the pointless open.
			select {
			case <-stop:
				return
			default:
			}

			if c.ensureTopology != nil {
				if err := c.ensureTopology(); err != nil {
					c.setHealthForGen(gen, ConsumerRebuilding, err)
					c.log.Error("reactive-commons: failed to restore topology for consumer; retrying", "listener", c.name, "queue", queueName, "error", err, "delay", backoff)
					backoff = min(backoff*2, c.maxBackoff)
					continue
				}
			}

			newCh, newDeliveries, err := c.open(queueName)
			if err != nil {
				c.setHealthForGen(gen, ConsumerRebuilding, err)
				c.log.Error("reactive-commons: failed to rebuild consumer channel; retrying", "listener", c.name, "queue", queueName, "error", err, "delay", backoff)
				backoff = min(backoff*2, c.maxBackoff)
				continue
			}

			// Superseded while opening: close the fresh channel and let the
			// new supervisor own the queue.
			select {
			case <-stop:
				_ = newCh.Close()
				return
			default:
			}

			ch, deliveries = newCh, newDeliveries
			c.log.Info("reactive-commons: consumer channel rebuilt", "listener", c.name, "queue", queueName)
			break
		}
	}
}

// setHealthForGen records a health transition for the current supervisor only.
// A superseded supervisor's late exit must not clobber its replacement's state.
func (c *supervisedConsumer) setHealthForGen(gen uint64, state ConsumerState, err error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.runGen != gen {
		return
	}
	c.setHealthLocked(state, err)
}

// setHealthLocked updates the snapshot. Caller must hold stateMu.
func (c *supervisedConsumer) setHealthLocked(state ConsumerState, err error) {
	if c.state != state {
		c.state = state
		c.stateSince = time.Now()
	}
	if err != nil {
		c.lastErr = err.Error()
	} else if state == ConsumerRunning {
		c.lastErr = ""
	}
}

// snapshot returns a copy of the consumer health.
func (c *supervisedConsumer) snapshot() ConsumerHealth {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return ConsumerHealth{State: c.state, Since: c.stateSince, LastError: c.lastErr}
}

// markStopped forces the health state to stopped. Used at shutdown after the
// supervisor drain, when a handler may still be running past the deadline.
func (c *supervisedConsumer) markStopped() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.setHealthLocked(ConsumerStopped, nil)
}
