package rabbit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	amqp "github.com/rabbitmq/amqp091-go"
)

type eventListener struct {
	sup *supervisedConsumer
	reg *handlerRegistry
	cfg Config
	log *slog.Logger
}

// newEventListener builds an event listener whose consumer channel is
// supervised: a broker-initiated channel close or consumer cancel is healed
// in place, while connection-level losses are restored by the application's
// reconnect hook (which supersedes the previous supervisor via start).
func newEventListener(conn *Connection, reg *handlerRegistry, cfg Config, wg *sync.WaitGroup, ensureTopology func() error) *eventListener {
	l := &eventListener{reg: reg, cfg: cfg, log: cfg.Logger}
	l.sup = newSupervisedConsumer(conn, cfg.Logger, wg, "event",
		func(queueName string) (consumerChannel, <-chan amqp.Delivery, error) {
			ch, err := conn.Channel()
			if err != nil {
				return nil, nil, err
			}
			if err = ch.Qos(cfg.PrefetchCount, 0, false); err != nil {
				_ = ch.Close()
				return nil, nil, err
			}
			deliveries, err := ch.Consume(queueName, "", false, false, false, false, nil)
			if err != nil {
				_ = ch.Close()
				return nil, nil, err
			}
			return ch, deliveries, nil
		},
		l.dispatch, ensureTopology)
	return l
}

// Start begins consuming from queueName. Returns when the consumer channel is
// established; the supervisor goroutine runs until ctx is cancelled.
func (l *eventListener) Start(ctx context.Context, queueName string) error {
	return l.sup.start(ctx, queueName)
}

func (l *eventListener) dispatch(ctx context.Context, d amqp.Delivery) {
	// requeueOnFailure controls whether handler failures put the message back on
	// the same queue (true, immediate retry forever) or are nacked without
	// requeue so the broker dead-letters into the configured DLX (false). When
	// WithDLQRetry is on, we MUST nack without requeue so the TTL retry loop
	// declared in topology.go can do its job — matching reactive-commons-java.
	requeueOnFailure := !l.cfg.WithDLQRetry

	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in event handler", "panic", r)
			_ = d.Nack(false, requeueOnFailure)
		}
	}()

	var env async.RawEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		l.log.Error("reactive-commons: failed to deserialize event envelope", "error", err)
		_ = d.Nack(false, false)
		return
	}

	handler := l.reg.EventHandler(env.Name)
	if handler == nil {
		_ = d.Ack(false)
		return
	}

	event := async.DomainEvent[any]{
		Name:    env.Name,
		EventID: env.EventID,
		Data:    json.RawMessage(env.Data),
	}

	if err := handler(ctx, event); err != nil {
		l.log.Error("reactive-commons: event handler error", "event", env.Name, "error", err)
		_ = d.Nack(false, requeueOnFailure)
		return
	}
	_ = d.Ack(false)
}
