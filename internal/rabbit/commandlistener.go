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
			return openConsumerChannel(conn, cfg.PrefetchCount, queueName, false, false)
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

	dispatchEnvelope(l.log, "command", requeueOnFailure, d,
		func(requeue bool) { _ = d.Nack(false, requeue) },
		func(env *async.RawEnvelope) {
			runEnvelopeHandler(l.log, "command", env.Name, requeueOnFailure, d, ctx, l.reg.CommandHandler(env.Name), async.Command[any]{
				Name:      env.Name,
				CommandID: env.CommandID,
				Data:      json.RawMessage(env.Data),
			})
		})
}
