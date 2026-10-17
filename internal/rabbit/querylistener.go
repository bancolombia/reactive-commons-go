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
	sup *supervisedConsumer
	reg *handlerRegistry
	gw  *gateway
	cfg Config
	log *slog.Logger
}

// newQueryListener builds a query listener whose consumer channel is
// supervised: a broker-initiated channel close or consumer cancel is healed
// in place, while connection-level losses are restored by the application's
// reconnect hook (which supersedes the previous supervisor via start).
func newQueryListener(conn *Connection, reg *handlerRegistry, gw *gateway, cfg Config, wg *sync.WaitGroup, ensureTopology func() error) *queryListener {
	l := &queryListener{reg: reg, gw: gw, cfg: cfg, log: cfg.Logger}
	l.sup = newSupervisedConsumer(conn, cfg.Logger, wg, "query",
		func(queueName string) (consumerChannel, <-chan amqp.Delivery, error) {
			ch, err := conn.Channel()
			if err != nil {
				return nil, nil, err
			}
			if err = ch.Qos(cfg.PrefetchCount, 0, false); err != nil {
				_ = ch.Close()
				return nil, nil, err
			}
			deliveries, err := ch.Consume(queueName, "", false, false, false, false, nil)
			if err != nil {
				_ = ch.Close()
				return nil, nil, err
			}
			return ch, deliveries, nil
		},
		l.dispatch, ensureTopology)
	return l
}

// Start begins consuming from the queries queue. Returns when the consumer
// channel is established; the supervisor goroutine runs until ctx is cancelled.
func (l *queryListener) Start(ctx context.Context, queueName string) error {
	return l.sup.start(ctx, queueName)
}

func (l *queryListener) dispatch(ctx context.Context, d amqp.Delivery) {
	requeueOnFailure := !l.cfg.WithDLQRetry

	defer func() {
		if r := recover(); r != nil {
			l.log.Error("reactive-commons: panic in query handler", "panic", r)
			_ = d.Nack(false, requeueOnFailure)
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
