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
			return openConsumerChannel(conn, cfg.PrefetchCount, queueName, false, false)
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

	dispatchEnvelope(l.log, "query", requeueOnFailure, d,
		func(requeue bool) { _ = d.Nack(false, requeue) },
		func(env *async.RawEnvelope) {
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
				// Handler errors are answered to the caller, not redelivered:
				// the message is acked so it cannot loop.
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
		})
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
