package rabbit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	amqp "github.com/rabbitmq/amqp091-go"
)

type commandListener struct {
	sup *supervisedConsumer
	reg *handlerRegistry
	cfg Config
	log *slog.Logger
}

// newCommandListener builds a command listener whose consumer channel is
// supervised: a broker-initiated channel close or consumer cancel is healed
// in place, while connection-level losses are restored by the application's
// reconnect hook (which supersedes the previous supervisor via start).
func newCommandListener(conn *Connection, reg *handlerRegistry, cfg Config, wg *sync.WaitGroup, ensureTopology func() error) *commandListener {
	l := &commandListener{reg: reg, cfg: cfg, log: cfg.Logger}
	l.sup = newSupervisedConsumer(conn, cfg.Logger, wg, "command",
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

// Start begins consuming from the commands queue. Returns when the consumer
// channel is established; the supervisor goroutine runs until ctx is cancelled.
func (l *commandListener) Start(ctx context.Context, queueName string) error {
	return l.sup.start(ctx, queueName)
}

func (l *commandListener) dispatch(ctx context.Context, d amqp.Delivery) {
	requeueOnFailure := !l.cfg.WithDLQRetry

	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in command handler", "panic", r)
			_ = d.Nack(false, requeueOnFailure)
		}
	}()

	var env async.RawEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		l.log.Error("reactive-commons: failed to deserialize command envelope", "error", err)
		_ = d.Nack(false, false)
		return
	}

	handler := l.reg.CommandHandler(env.Name)
	if handler == nil {
		_ = d.Ack(false)
		return
	}

	cmd := async.Command[any]{
		Name:      env.Name,
		CommandID: env.CommandID,
		Data:      json.RawMessage(env.Data),
	}

	if err := handler(ctx, cmd); err != nil {
		l.log.Error("reactive-commons: command handler error", "command", env.Name, "error", err)
		_ = d.Nack(false, requeueOnFailure)
		return
	}
	_ = d.Ack(false)
}
