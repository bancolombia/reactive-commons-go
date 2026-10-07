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

// retryBackoff returns the delay before the (attempt+1)-th handler invocation.
// attempt is 0-indexed: attempt=0 is the delay before the first retry after
// the initial failure. delay = initial * 2^attempt, capped at maxDelay.
func retryBackoff(attempt int, initial, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if initial <= 0 {
		return 0
	}
	d := initial
	for i := 0; i < attempt; i++ {
		d *= 2
		if d <= 0 || d >= maxDelay {
			return maxDelay
		}
	}
	if d > maxDelay {
		return maxDelay
	}
	return d
}

// eventListener owns a *kafka.Reader and drives the durable, at-least-once
// event delivery loop: fetch → decode → invoke handler (with retries) → DLQ
// on exhaustion → manual offset commit.
type eventListener struct {
	cfg     Config
	name    string
	topic   string
	groupID string
	handler async.EventHandler[any]
	dlq     *dlqProducer
	reader  *kgo.Reader
	logger  *slog.Logger
	obs     *observability
}

func newEventListener(cfg Config, name string, handler async.EventHandler[any], p *producer, log *slog.Logger, obs *observability) *eventListener {
	return &eventListener{
		cfg:     cfg,
		name:    name,
		topic:   topicForEvent(cfg, name),
		groupID: groupIDForEvent(cfg, name),
		handler: handler,
		dlq:     newDLQProducer(cfg, p),
		logger:  log,
		obs:     obs,
	}
}

func (l *eventListener) open() {
	l.reader = kgo.NewReader(kgo.ReaderConfig{
		Brokers:           l.cfg.BootstrapBrokers,
		GroupID:           l.groupID,
		Topic:             l.topic,
		MinBytes:          1,
		MaxBytes:          10 << 20, // 10 MiB
		SessionTimeout:    l.cfg.ConsumerSessionTimeout,
		HeartbeatInterval: l.cfg.ConsumerHeartbeatInterval,
		CommitInterval:    0, // manual commit only
		Dialer:            dialer(l.cfg),
	})
}

func (l *eventListener) close() error {
	if l == nil || l.reader == nil {
		return nil
	}
	return l.reader.Close()
}

// run loops until ctx is cancelled. Transient fetch errors (broker restart,
// group rebalance, temporary network drop) are logged and retried after a
// short backoff; only a cancelled context terminates the loop.
func (l *eventListener) run(ctx context.Context) {
	const fetchBackoff = 1 * time.Second
	for {
		msg, err := l.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return
			}
			l.logger.Warn("kafka: fetch failed; retrying",
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

func (l *eventListener) processMessage(ctx context.Context, msg kgo.Message) {
	raw, err := envelope.UnmarshalRaw(msg.Value)
	if err != nil {
		l.logger.Warn("kafka: envelope decode failed; routing to DLQ",
			"topic", l.topic, "err", err)
		if dlqErr := l.dlq.publish(ctx, l.topic, msg, 0, "decode: "+err.Error()); dlqErr != nil {
			l.logger.Error("kafka: DLQ publish failed after decode error",
				"topic", l.topic, "err", dlqErr)
			return // do not commit; will be redelivered
		}
		l.commit(ctx, msg)
		return
	}

	event := async.DomainEvent[any]{
		Name:    raw.Name,
		EventID: raw.EventID,
	}
	if len(raw.Data) > 0 {
		var d any
		if err := json.Unmarshal(raw.Data, &d); err != nil {
			l.logger.Warn("kafka: data decode failed; passing raw json",
				"topic", l.topic, "err", err)
			event.Data = raw.Data
		} else {
			event.Data = d
		}
	}

	attempts := l.cfg.MaxRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			backoff := retryBackoff(attempt-1, l.cfg.RetryInitialDelay, l.cfg.RetryMaxDelay)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return // graceful shutdown: no commit, message will redeliver
			}
		}
		lastErr = l.invokeHandler(ctx, event)
		if lastErr == nil {
			l.commit(ctx, msg)
			return
		}
		l.logger.Warn("kafka: handler error",
			"topic", l.topic, "attempt", attempt+1, "err", lastErr)
	}

	reason := "handler exhausted retries"
	if lastErr != nil {
		reason = lastErr.Error()
	}
	if dlqErr := l.dlq.publish(ctx, l.topic, msg, attempts, reason); dlqErr != nil {
		l.logger.Error("kafka: DLQ publish failed after retries",
			"topic", l.topic, "err", dlqErr)
		return // do not commit; will be redelivered
	}
	l.commit(ctx, msg)
}

// invokeHandler runs the handler with a per-message context bounded by
// cfg.HandlerTimeout. Panics are converted to errors so a bad handler cannot
// crash the loop. Wrapped in a consumer span and metric counters.
func (l *eventListener) invokeHandler(ctx context.Context, event async.DomainEvent[any]) (err error) {
	spanCtx, end := l.obs.startConsumeSpan(ctx, l.topic, "event")
	l.obs.messagesConsumed.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "event"))
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("kafka: handler panic: %v", r)
		}
		l.obs.handlerDurationMs.Record(spanCtx, time.Since(start).Seconds(),
			metricAttrs("kafka", l.topic, "event"))
		if err != nil {
			l.obs.handlerErrors.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "event"))
		}
		end(err)
	}()
	hctx, cancel := context.WithTimeout(spanCtx, l.cfg.HandlerTimeout)
	defer cancel()
	return l.handler(hctx, event)
}

func (l *eventListener) commit(ctx context.Context, msg kgo.Message) {
	if err := l.reader.CommitMessages(ctx, msg); err != nil {
		l.logger.Warn("kafka: commit failed",
			"topic", l.topic, "partition", msg.Partition, "offset", msg.Offset, "err", err)
	}
}
