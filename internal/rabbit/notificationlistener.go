package rabbit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	amqp "github.com/rabbitmq/amqp091-go"
)

type notificationListener struct {
	sup *supervisedConsumer
	reg *handlerRegistry
	cfg Config
	log *slog.Logger
}

// newNotificationListener builds a notification listener whose consumer
// channel is supervised: a broker-initiated channel close or consumer cancel
// is healed in place, while connection-level losses are restored by the
// application's reconnect hook (which supersedes the previous supervisor via
// start).
func newNotificationListener(conn *Connection, reg *handlerRegistry, cfg Config, wg *sync.WaitGroup, ensureTopology func() error) *notificationListener {
	l := &notificationListener{reg: reg, cfg: cfg, log: cfg.Logger}
	l.sup = newSupervisedConsumer(conn, cfg.Logger, wg, "notification",
		func(queueName string) (consumerChannel, <-chan amqp.Delivery, error) {
			// Auto-ack and exclusive: fan-out temp queue, one per app instance.
			return openConsumerChannel(conn, 0, queueName, true, true)
		},
		l.dispatch, ensureTopology)
	return l
}

// Start begins consuming from the notification temp queue. The supervisor
// goroutine runs until ctx is cancelled.
func (l *notificationListener) Start(ctx context.Context, queueName string) error {
	return l.sup.start(ctx, queueName)
}

func (l *notificationListener) dispatch(ctx context.Context, d amqp.Delivery) {
	dispatchEnvelope(l.log, "notification", false, d, nil,
		func(env *async.RawEnvelope) {
			handler := l.reg.NotificationHandler(env.Name)
			if handler == nil {
				return
			}

			n := async.Notification[any]{
				Name:    env.Name,
				EventID: env.EventID,
				Data:    json.RawMessage(env.Data),
			}

			if err := handler(ctx, n); err != nil {
				l.log.Error("reactive-commons: notification handler error", "notification", env.Name, "error", err)
				// No redeliver — notification delivery is best-effort.
			}
		})
}
