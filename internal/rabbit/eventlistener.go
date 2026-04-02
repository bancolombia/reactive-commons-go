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
	conn *Connection
	reg  *handlerRegistry
	cfg  Config
	log  *slog.Logger
	wg   *sync.WaitGroup
}

func newEventListener(conn *Connection, reg *handlerRegistry, cfg Config, wg *sync.WaitGroup) *eventListener {
	return &eventListener{conn: conn, reg: reg, cfg: cfg, log: cfg.Logger, wg: wg}
}

// Start begins consuming from queueName in a goroutine. Returns when the consumer
// channel is established. The goroutine runs until ctx is cancelled.
func (l *eventListener) Start(ctx context.Context, queueName string) error {
	ch, err := l.conn.Channel()
	if err != nil {
		return err
	}
	if err = ch.Qos(l.cfg.PrefetchCount, 0, false); err != nil {
		_ = ch.Close()
		return err
	}
	var deliveries <-chan amqp.Delivery
	if deliveries, err = ch.Consume(queueName, "", false, false, false, false, nil); err != nil {
		_ = ch.Close()
		return err
	}

	if l.wg != nil {
		l.wg.Add(1)
	}
	go func() {
		defer func() { _ = ch.Close() }()
		if l.wg != nil {
			defer l.wg.Done()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case d, ok := <-deliveries:
				if !ok {
					return
				}
				l.dispatch(ctx, d)
			}
		}
	}()
	return nil
}

func (l *eventListener) dispatch(ctx context.Context, d amqp.Delivery) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in event handler", "panic", r)
			_ = d.Nack(false, true)
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
		_ = d.Nack(false, true)
		return
	}
	_ = d.Ack(false)
}
