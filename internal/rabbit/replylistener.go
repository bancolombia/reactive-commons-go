package rabbit

import (
	"context"
	"log/slog"
	"sync"

	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
)

type replyListener struct {
	conn   *Connection
	router *ReplyRouter
	cfg    Config
	log    *slog.Logger
	wg     *sync.WaitGroup
}

func newReplyListener(conn *Connection, router *ReplyRouter, cfg Config, wg *sync.WaitGroup) *replyListener {
	return &replyListener{conn: conn, router: router, cfg: cfg, log: cfg.Logger, wg: wg}
}

// Start begins consuming from the reply queue. Replies are auto-acked (exclusive queue).
// The goroutine runs until ctx is cancelled.
func (l *replyListener) Start(ctx context.Context, queueName string) error {
	ch, err := l.conn.Channel()
	if err != nil {
		return err
	}
	// Auto-ack and exclusive: this is a per-instance ephemeral reply queue.
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
				l.route(d)
			}
		}
	}()
	return nil
}

func (l *replyListener) route(d amqp.Delivery) {
	correlationID := headerString(d.Headers, hdr.CorrelationID)
	if correlationID == "" {
		l.log.Warn("reactive-commons: reply received with no correlation-id, discarding")
		return
	}

	isError := headerString(d.Headers, hdr.ReplyError) == "true"
	isEmpty := headerString(d.Headers, hdr.CompletionOnlySignal) == "true"

	l.router.Route(correlationID, ReplyPayload{
		Body:    d.Body,
		IsError: isError,
		IsEmpty: isEmpty,
	})
}
