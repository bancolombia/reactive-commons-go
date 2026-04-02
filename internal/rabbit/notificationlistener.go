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
	conn *Connection
	reg  *handlerRegistry
	cfg  Config
	log  *slog.Logger
	wg   *sync.WaitGroup
}

func newNotificationListener(conn *Connection, reg *handlerRegistry, cfg Config, wg *sync.WaitGroup) *notificationListener {
	return &notificationListener{conn: conn, reg: reg, cfg: cfg, log: cfg.Logger, wg: wg}
}

// Start declares an exclusive auto-delete temp queue, binds all registered
// notification names, then begins consuming. The goroutine runs until ctx is cancelled.
// Fan-out semantics: each app instance gets its own queue so every instance receives every notification.
func (l *notificationListener) Start(ctx context.Context, queueName string) error {
	ch, err := l.conn.Channel()
	if err != nil {
		return err
	}

	deliveries, err := ch.Consume(queueName, "", true, true, false, false, nil)
	if err != nil {
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

func (l *notificationListener) dispatch(ctx context.Context, d amqp.Delivery) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in notification handler", "panic", r)
			// Notifications are non-durable — no redeliver on error.
		}
	}()

	var env async.RawEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		l.log.Error("reactive-commons: failed to deserialize notification envelope", "error", err)
		return
	}

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
}
