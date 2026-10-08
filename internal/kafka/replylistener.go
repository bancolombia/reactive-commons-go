package kafka

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	kgo "github.com/segmentio/kafka-go"
)

// replyListener drives best-effort delivery of query replies for this
// instance. Every instance of an app consumes the shared reply topic through
// its own consumer group (fan-out, like notifications) and the router
// forwards only the replies that match a locally registered correlation ID.
//
// Replies are never retried and never DLQ'd: undecodable or uncorrelated
// replies are dropped.
type replyListener struct {
	cfg      Config
	topic    string
	groupID  string
	router   *replyrouter.ReplyRouter
	reader   *kgo.Reader
	primed   chan struct{} // closed once the first fetch cycle anchored offsets
	primeNum int64         // number of partitions to prime before signaling
	logger   *slog.Logger
	obs      *observability
}

func newReplyListener(cfg Config, router *replyrouter.ReplyRouter, partitions int, log *slog.Logger, obs *observability) *replyListener {
	if partitions < 1 {
		partitions = 1
	}
	return &replyListener{
		cfg:      cfg,
		topic:    topicForReply(cfg),
		groupID:  groupIDForReply(cfg),
		router:   router,
		primed:   make(chan struct{}),
		primeNum: int64(partitions),
		logger:   log,
		obs:      obs,
	}
}

func (l *replyListener) open() {
	l.reader = kgo.NewReader(kgo.ReaderConfig{
		Brokers:           l.cfg.BootstrapBrokers,
		GroupID:           l.groupID,
		Topic:             l.topic,
		MinBytes:          1,
		MaxBytes:          10 << 20, // 10 MiB
		SessionTimeout:    l.cfg.ConsumerSessionTimeout,
		HeartbeatInterval: l.cfg.ConsumerHeartbeatInterval,
		StartOffset:       kgo.LastOffset,
		CommitInterval:    time.Second, // auto-commit; replies are best-effort
		Dialer:            dialer(l.cfg),
	})
}

func (l *replyListener) close() error {
	if l == nil || l.reader == nil {
		return nil
	}
	return l.reader.Close()
}

// run primes the reader (anchoring the group's offsets so replies produced
// after priming are never missed) and then routes replies until ctx is
// cancelled. Priming does not gate Ready(); RequestReply gates itself on the
// primed channel bounded by its own context.
func (l *replyListener) run(ctx context.Context) {
	l.awaitPrimed(ctx)

	const fetchBackoff = 1 * time.Second
	for {
		msg, err := l.reader.ReadMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return
			}
			l.logger.Warn("kafka: reply fetch failed; retrying",
				"topic", l.topic, "err", err, "backoff", fetchBackoff)
			select {
			case <-time.After(fetchBackoff):
			case <-ctx.Done():
				return
			}
			continue
		}
		l.route(ctx, msg)
	}
}

// awaitPrimed waits until every partition of the reply topic has completed at
// least one fetch (group joined, offsets resolved at LastOffset). The stats
// counter is shared across partition readers, so the target is the partition
// count. If ctx is cancelled first the primed channel stays open forever and
// callers unblock through their own context.
func (l *replyListener) awaitPrimed(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if l.reader.Stats().Fetches >= l.primeNum {
			close(l.primed)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (l *replyListener) route(ctx context.Context, msg kgo.Message) {
	correlationID := headerValue(msg, hdr.CorrelationID)
	if correlationID == "" {
		l.logger.Warn("kafka: reply received with no correlation-id, discarding",
			"topic", l.topic)
		return
	}
	isError := headerValue(msg, hdr.ReplyError) == "true"
	isEmpty := headerValue(msg, hdr.CompletionOnlySignal) == "true"

	l.obs.messagesConsumed.Add(ctx, 1, metricAttrs("kafka", l.topic, "reply"))
	l.router.Route(correlationID, replyrouter.ReplyPayload{
		Body:    msg.Value,
		IsError: isError,
		IsEmpty: isEmpty,
	})
}
