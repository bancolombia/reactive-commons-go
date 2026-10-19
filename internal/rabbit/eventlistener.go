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
			return openConsumerChannel(conn, cfg.PrefetchCount, queueName, false, false)
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

	dispatchEnvelope(l.log, "event", requeueOnFailure, d,
		func(requeue bool) { _ = d.Nack(false, requeue) },
		func(env *async.RawEnvelope) {
			runEnvelopeHandler(l.log, "event", env.Name, requeueOnFailure, d, ctx, l.reg.EventHandler(env.Name), async.DomainEvent[any]{
				Name:    env.Name,
				EventID: env.EventID,
				Data:    json.RawMessage(env.Data),
			})
		})
}
