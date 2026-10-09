package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/envelope"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	kgo "github.com/segmentio/kafka-go"
)

// commandListener owns a *kafka.Reader over the app's shared commands topic
// and drives durable, at-least-once command delivery: fetch → decode →
// dispatch by name (with retries) → DLQ on exhaustion → manual offset commit.
//
// Commands with no registered handler are silently discarded (rabbit
// parity), as are commands matched by another instance's wildcard patterns.
type commandListener struct {
	cfg     Config
	topic   string
	groupID string
	reg     *handlerRegistry
	dlq     *dlqProducer
	reader  *kgo.Reader
	logger  *slog.Logger
	obs     *observability
}

func newCommandListener(cfg Config, reg *handlerRegistry, p *producer, log *slog.Logger, obs *observability) *commandListener {
	return &commandListener{
		cfg:     cfg,
		topic:   topicForCommand(cfg, cfg.AppName),
		groupID: groupIDForCommand(cfg),
		reg:     reg,
		dlq:     newDLQProducer(cfg, p),
		logger:  log,
		obs:     obs,
	}
}

func (l *commandListener) open() {
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

func (l *commandListener) close() error {
	if l == nil || l.reader == nil {
		return nil
	}
	return l.reader.Close()
}

// run loops until ctx is cancelled. Transient fetch errors are logged and
// retried after a short backoff; only a cancelled context terminates the loop.
func (l *commandListener) run(ctx context.Context) {
	consumerLoop{
		kind:   "command",
		topic:  l.topic,
		logger: l.logger,
		fetch:  l.reader.FetchMessage,
		handle: l.processMessage,
	}.run(ctx)
}

func (l *commandListener) processMessage(ctx context.Context, msg kgo.Message) {
	raw, err := envelope.UnmarshalRaw(msg.Value)
	if err == nil && raw.Name == "" {
		err = fmt.Errorf("kafka: envelope decode: missing required field %q", "name")
	}
	if err != nil {
		l.logger.Warn("kafka: command envelope decode failed; routing to DLQ",
			"topic", l.topic, "err", err)
		if dlqErr := l.dlq.publish(ctx, l.topic, msg, 0, "decode: "+err.Error()); dlqErr != nil {
			l.logger.Error("kafka: DLQ publish failed after decode error",
				"topic", l.topic, "err", dlqErr)
			return // do not commit; will be redelivered
		}
		l.commit(ctx, msg)
		return
	}

	handler := l.reg.CommandHandler(raw.Name)
	if handler == nil {
		l.logger.Debug("kafka: no handler for command; discarding",
			"topic", l.topic, "command", raw.Name)
		l.commit(ctx, msg)
		return
	}

	// Data stays a json.RawMessage so the same handler code works on both
	// transports (rabbit parity).
	cmd := async.Command[any]{
		Name:      raw.Name,
		CommandID: raw.CommandID,
		Data:      json.RawMessage(raw.Data),
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
		lastErr = l.invokeHandler(ctx, handler, cmd)
		if lastErr == nil {
			l.commit(ctx, msg)
			return
		}
		l.logger.Warn("kafka: command handler error",
			"topic", l.topic, "command", cmd.Name, "attempt", attempt+1, "err", lastErr)
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
func (l *commandListener) invokeHandler(ctx context.Context, handler async.CommandHandler[any], cmd async.Command[any]) (err error) {
	spanCtx, end := l.obs.startConsumeSpan(ctx, l.topic, "command")
	l.obs.messagesConsumed.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "command"))
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("kafka: command handler panic: %v", r)
		}
		l.obs.handlerDurationMs.Record(spanCtx, time.Since(start).Seconds(),
			metricAttrs("kafka", l.topic, "command"))
		if err != nil {
			l.obs.handlerErrors.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "command"))
		}
		end(err)
	}()
	hctx, cancel := context.WithTimeout(spanCtx, l.cfg.HandlerTimeout)
	defer cancel()
	return handler(hctx, cmd)
}

func (l *commandListener) commit(ctx context.Context, msg kgo.Message) {
	if err := l.reader.CommitMessages(ctx, msg); err != nil {
		l.logger.Warn("kafka: commit failed",
			"topic", l.topic, "partition", msg.Partition, "offset", msg.Offset, "err", err)
	}
}
