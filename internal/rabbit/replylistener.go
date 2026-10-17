package rabbit

import (
	"context"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
)

type replyListener struct {
	sup    *supervisedConsumer
	router *replyrouter.ReplyRouter
	cfg    Config
	log    *slog.Logger
}

// newReplyListener builds a reply listener whose consumer channel is
// supervised: a broker-initiated channel close or consumer cancel is healed
// in place, while connection-level losses are restored by the application's
// reconnect hook (which supersedes the previous supervisor via start).
func newReplyListener(conn *Connection, router *replyrouter.ReplyRouter, cfg Config, wg *sync.WaitGroup, ensureTopology func() error) *replyListener {
	l := &replyListener{router: router, cfg: cfg, log: cfg.Logger}
	l.sup = newSupervisedConsumer(conn, cfg.Logger, wg, "reply",
		func(queueName string) (consumerChannel, <-chan amqp.Delivery, error) {
			ch, err := conn.Channel()
			if err != nil {
				return nil, nil, err
			}
			// Auto-ack and exclusive: this is a per-instance ephemeral reply queue.
			deliveries, err := ch.Consume(queueName, "", true, true, false, false, nil)
			if err != nil {
				_ = ch.Close()
				return nil, nil, err
			}
			return ch, deliveries, nil
		},
		func(_ context.Context, d amqp.Delivery) { l.route(d) },
		ensureTopology)
	return l
}

// Start begins consuming from the reply queue. The supervisor goroutine runs
// until ctx is cancelled.
func (l *replyListener) Start(ctx context.Context, queueName string) error {
	return l.sup.start(ctx, queueName)
}

func (l *replyListener) route(d amqp.Delivery) {
	correlationID := headerString(d.Headers, hdr.CorrelationID)
	if correlationID == "" {
		l.log.Warn("reactive-commons: reply received with no correlation-id, discarding")
		return
	}

	isError := headerString(d.Headers, hdr.ReplyError) == "true"
	isEmpty := headerString(d.Headers, hdr.CompletionOnlySignal) == "true"

	l.router.Route(correlationID, replyrouter.ReplyPayload{
		Body:    d.Body,
		IsError: isError,
		IsEmpty: isEmpty,
	})
}
