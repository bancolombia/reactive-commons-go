package rabbit

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
)

type queryListener struct {
	conn *Connection
	reg  *handlerRegistry
	gw   *gateway
	cfg  Config
	log  *slog.Logger
	wg   *sync.WaitGroup
}

func newQueryListener(conn *Connection, reg *handlerRegistry, gw *gateway, cfg Config, wg *sync.WaitGroup) *queryListener {
	return &queryListener{conn: conn, reg: reg, gw: gw, cfg: cfg, log: cfg.Logger, wg: wg}
}

// Start begins consuming from the queries queue. Returns when the consumer channel
// is established; the goroutine runs until ctx is cancelled.
func (l *queryListener) Start(ctx context.Context, queueName string) error {
	ch, err := l.conn.Channel()
	if err != nil {
		return err
	}
	if err = ch.Qos(l.cfg.PrefetchCount, 0, false); err != nil {
		_ = ch.Close()
		return err
	}
	var deliveries <-chan amqp.Delivery
	if deliveries, err = ch.Consume(queueName, "", false, false, false, false, nil); err != nil {
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

func (l *queryListener) dispatch(ctx context.Context, d amqp.Delivery) {
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in query handler", "panic", r)
			_ = d.Nack(false, true)
		}
	}()

	var env async.RawEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		l.log.Error("reactive-commons: failed to deserialize query envelope", "error", err)
		_ = d.Nack(false, false)
		return
	}

	from := async.From{
		CorrelationID: headerString(d.Headers, hdr.CorrelationID),
		ReplyID:       headerString(d.Headers, hdr.ReplyID),
	}

	handler := l.reg.QueryHandler(env.Resource)
	if handler == nil {
		_ = d.Ack(false)
		return
	}

	query := async.AsyncQuery[any]{
		Resource:  env.Resource,
		QueryData: json.RawMessage(env.QueryData),
	}

	result, err := handler(ctx, query, from)
	if err != nil {
		if replyErr := l.gw.replyError(ctx, err.Error(), from); replyErr != nil {
			l.log.Error("reactive-commons: failed to send error reply", "error", replyErr)
		}
		_ = d.Ack(false)
		return
	}

	if replyErr := l.gw.Reply(ctx, result, from); replyErr != nil {
		l.log.Error("reactive-commons: failed to send query reply", "error", replyErr)
	}
	_ = d.Ack(false)
}

// headerString extracts a string value from an AMQP header table.
func headerString(headers amqp.Table, key string) string {
	if headers == nil {
		return ""
	}
	if v, ok := headers[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
