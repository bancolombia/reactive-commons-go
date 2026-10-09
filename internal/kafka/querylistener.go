package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/envelope"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	kgo "github.com/segmentio/kafka-go"
)

// queryListener owns a *kafka.Reader over the app's shared queries topic and
// serves async queries: fetch → decode → dispatch by resource → publish the
// reply → manual commit.
//
// Queries are not retried and have no DLQ (rabbit parity): a handler error is
// returned to the caller as an error reply, and a query with no registered
// handler is discarded so the caller times out. Handler errors and reply
// publish failures are bounded by cfg.HandlerTimeout.
type queryListener struct {
	cfg     Config
	topic   string
	groupID string
	reg     *handlerRegistry
	gw      *kafkaGateway
	reader  *kgo.Reader
	logger  *slog.Logger
	obs     *observability
}

func newQueryListener(cfg Config, reg *handlerRegistry, gw *kafkaGateway, log *slog.Logger, obs *observability) *queryListener {
	return &queryListener{
		cfg:     cfg,
		topic:   topicForQuery(cfg, cfg.AppName),
		groupID: groupIDForQuery(cfg),
		reg:     reg,
		gw:      gw,
		logger:  log,
		obs:     obs,
	}
}

func (l *queryListener) open() {
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

func (l *queryListener) close() error {
	if l == nil || l.reader == nil {
		return nil
	}
	return l.reader.Close()
}

// run loops until ctx is cancelled. Transient fetch errors are logged and
// retried after a short backoff; only a cancelled context terminates the loop.
func (l *queryListener) run(ctx context.Context) {
	consumerLoop{
		kind:   "query",
		topic:  l.topic,
		logger: l.logger,
		fetch:  l.reader.FetchMessage,
		handle: l.processMessage,
	}.run(ctx)
}

func (l *queryListener) processMessage(ctx context.Context, msg kgo.Message) {
	from := async.From{
		CorrelationID: headerValue(msg, hdr.CorrelationID),
		ReplyID:       headerValue(msg, hdr.ReplyID),
	}

	raw, err := envelope.UnmarshalRaw(msg.Value)
	if err == nil && raw.Resource == "" {
		err = fmt.Errorf("kafka: envelope decode: missing required field %q", "resource")
	}
	if err != nil {
		l.logger.Warn("kafka: query envelope decode failed",
			"topic", l.topic, "err", err)
		// The correlation headers are independent of the body, so the caller
		// can fail fast instead of waiting out its whole timeout.
		if from.ReplyID != "" && from.CorrelationID != "" {
			if replyErr := l.gw.replyError(ctx, err.Error(), from); replyErr != nil {
				l.logger.Error("kafka: failed to send decode-error reply", "err", replyErr)
			}
		}
		l.commit(ctx, msg)
		return
	}

	handler := l.reg.QueryHandler(raw.Resource)
	if handler == nil {
		l.logger.Debug("kafka: no handler for query; caller will time out",
			"topic", l.topic, "resource", raw.Resource)
		l.commit(ctx, msg)
		return
	}

	// QueryData stays a json.RawMessage so the same handler code works on
	// both transports (rabbit parity).
	query := async.AsyncQuery[any]{
		Resource:  raw.Resource,
		QueryData: json.RawMessage(raw.QueryData),
	}

	result, err := l.invokeHandler(ctx, handler, query, from)
	if err != nil {
		l.logger.Warn("kafka: query handler error",
			"topic", l.topic, "resource", raw.Resource, "err", err)
		if replyErr := l.gw.replyError(ctx, err.Error(), from); replyErr != nil {
			l.logger.Error("kafka: failed to send error reply", "err", replyErr)
		}
		l.commit(ctx, msg)
		return
	}

	if replyErr := l.gw.Reply(ctx, result, from); replyErr != nil {
		l.logger.Error("kafka: failed to send query reply", "err", replyErr)
	}
	l.commit(ctx, msg)
}

// invokeHandler runs the handler with a per-message context bounded by
// cfg.HandlerTimeout. Panics are converted to errors so a bad handler cannot
// crash the loop. Wrapped in a consumer span and metric counters.
func (l *queryListener) invokeHandler(ctx context.Context, handler async.QueryHandler[any, any], query async.AsyncQuery[any], from async.From) (result any, err error) {
	spanCtx, end := l.obs.startConsumeSpan(ctx, l.topic, "query")
	l.obs.messagesConsumed.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "query"))
	start := time.Now()
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("kafka: query handler panic: %v", r)
		}
		l.obs.handlerDurationMs.Record(spanCtx, time.Since(start).Seconds(),
			metricAttrs("kafka", l.topic, "query"))
		if err != nil {
			l.obs.handlerErrors.Add(spanCtx, 1, metricAttrs("kafka", l.topic, "query"))
		}
		end(err)
	}()
	hctx, cancel := context.WithTimeout(spanCtx, l.cfg.HandlerTimeout)
	defer cancel()
	return handler(hctx, query, from)
}

func (l *queryListener) commit(ctx context.Context, msg kgo.Message) {
	if err := l.reader.CommitMessages(ctx, msg); err != nil {
		l.logger.Warn("kafka: commit failed",
			"topic", l.topic, "partition", msg.Partition, "offset", msg.Offset, "err", err)
	}
}

// headerValue returns the value of the first header named key, or "".
func headerValue(msg kgo.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
