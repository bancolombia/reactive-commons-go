package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/envelope"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	kgo "github.com/segmentio/kafka-go"
)

// notificationListener drives non-durable, fan-out notification delivery. It
// uses a per-instance consumer group (via cfg.InstanceID), starts at
// LastOffset so historical notifications are not replayed, and relies on
// auto-commit so a failing handler cannot block the offset.
//
// Handler errors are logged and dropped: notifications are never retried and
// never routed to a DLQ (there is no notification DLQ).
type notificationListener struct {
	cfg     Config
	name    string
	topic   string
	groupID string
	handler async.NotificationHandler[any]
	reader  *kgo.Reader
	logger  *slog.Logger
	obs     *observability
}

func newNotificationListener(cfg Config, name string, handler async.NotificationHandler[any], log *slog.Logger, obs *observability) *notificationListener {
	return &notificationListener{
		cfg:     cfg,
		name:    name,
		topic:   topicForNotification(cfg, name),
		groupID: groupIDForNotification(cfg, name, cfg.InstanceID),
		handler: handler,
		logger:  log,
		obs:     obs,
	}
}

func (l *notificationListener) open() {
	l.reader = kgo.NewReader(kgo.ReaderConfig{
		Brokers:           l.cfg.BootstrapBrokers,
		GroupID:           l.groupID,
		Topic:             l.topic,
		MinBytes:          1,
		MaxBytes:          10 << 20, // 10 MiB
		SessionTimeout:    l.cfg.ConsumerSessionTimeout,
		HeartbeatInterval: l.cfg.ConsumerHeartbeatInterval,
		StartOffset:       kgo.LastOffset,
		CommitInterval:    time.Second, // auto-commit; failed handlers do not block
		Dialer:            dialer(l.cfg),
	})
}

func (l *notificationListener) close() error {
	if l == nil || l.reader == nil {
		return nil
	}
	return l.reader.Close()
}

func (l *notificationListener) run(ctx context.Context) {
	const fetchBackoff = 1 * time.Second
	for {
		msg, err := l.reader.ReadMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return
			}
			l.logger.Warn("kafka: notification fetch failed; retrying",
				"topic", l.topic, "err", err, "backoff", fetchBackoff)
			select {
			case <-time.After(fetchBackoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		l.processMessage(ctx, msg)
	}
}

func (l *notificationListener) processMessage(ctx context.Context, msg kgo.Message) {
	raw, err := envelope.UnmarshalRaw(msg.Value)
	if err != nil {
		l.logger.Warn("kafka: notification envelope decode failed; dropping",
			"topic", l.topic, "err", err)
		return
	}
	n := async.Notification[any]{
		Name:    raw.Name,
		EventID: raw.EventID,
	}
	if len(raw.Data) > 0 {
		var d any
		if err := json.Unmarshal(raw.Data, &d); err != nil {
			l.logger.Warn("kafka: notification data decode failed; passing raw json",
				"topic", l.topic, "err", err)
			n.Data = raw.Data
		} else {
			n.Data = d
		}
	}
	if err := l.invokeHandler(ctx, n); err != nil {
		l.logger.Warn("kafka: notification handler error (not retried)",
			"topic", l.topic, "eventId", n.EventID, "err", err)
	}
}

// invokeHandler runs the handler with a per-message context bounded by
// cfg.HandlerTimeout. Panics are converted to errors so a bad handler cannot
// crash the loop. Wrapped in a consumer span and metric counters.
func (l *notificationListener) invokeHandler(ctx context.Context, n async.Notification[any]) (err error) {
	spanCtx, end := l.obs.startConsumeSpan(ctx, l.topic, "notification")
	l.obs.messagesConsumed.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "notification"))
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("kafka: notification handler panic: %v", r)
		}
		l.obs.handlerDurationMs.Record(spanCtx, time.Since(start).Seconds(),
			metricAttrs("kafka", l.topic, "notification"))
		if err != nil {
			l.obs.handlerErrors.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "notification"))
		}
		end(err)
	}()
	hctx, cancel := context.WithTimeout(spanCtx, l.cfg.HandlerTimeout)
	defer cancel()
	return l.handler(hctx, n)
}
