package rabbit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	amqp "github.com/rabbitmq/amqp091-go"
)

// dispatchEnvelope runs the delivery contract shared by every listener: it
// recovers panics (logged with the listener kind), unmarshals the envelope,
// and hands a well-formed envelope to handle. nack applies the failure policy
// for manual-ack listeners — the panic path nacks with requeueOnFailure, a
// broken envelope is always nacked without requeue. Auto-ack listeners
// (notifications) pass a nil nack and can only log.
func dispatchEnvelope(log *slog.Logger, kind string, requeueOnFailure bool, d amqp.Delivery, nack func(requeue bool), handle func(env *async.RawEnvelope)) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("reactive-commons: panic in "+kind+" handler", "panic", r)
			if nack != nil {
				nack(requeueOnFailure)
			}
		}
	}()

	var env async.RawEnvelope
	if err := json.Unmarshal(d.Body, &env); err != nil {
		log.Error("reactive-commons: failed to deserialize "+kind+" envelope", "error", err)
		if nack != nil {
			nack(false)
		}
		return
	}
	handle(&env)
}

// runEnvelopeHandler applies the manual-ack result contract shared by events
// and commands: a nil handler acks (nothing registered for the message), a
// failing handler is logged and nacked (requeued unless requeueOnFailure is
// false, i.e. WithDLQRetry routes it to the DLQ instead), success acks.
func runEnvelopeHandler[T any](log *slog.Logger, kind, name string, requeueOnFailure bool, d amqp.Delivery, ctx context.Context, handler func(context.Context, T) error, msg T) {
	if handler == nil {
		_ = d.Ack(false)
		return
	}
	if err := handler(ctx, msg); err != nil {
		log.Error("reactive-commons: "+kind+" handler error", kind, name, "error", err)
		_ = d.Nack(false, requeueOnFailure)
		return
	}
	_ = d.Ack(false)
}
