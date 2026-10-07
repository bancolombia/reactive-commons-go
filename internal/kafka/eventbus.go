package kafka

import (
	"context"
	"fmt"

	"github.com/bancolombia/reactive-commons-go/internal/envelope"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
)

// domainEventBus implements async.DomainEventBus over a producer.
type domainEventBus struct {
	cfg      Config
	producer *producer
	obs      *observability
}

var _ async.DomainEventBus = (*domainEventBus)(nil)

func newDomainEventBus(cfg Config, p *producer, obs *observability) *domainEventBus {
	return &domainEventBus{cfg: cfg, producer: p, obs: obs}
}

// Emit publishes event to the topic derived from event.Name via
// cfg.TopicNameFunc. Blocks until the broker acks per cfg.ProducerAcks.
// Returns ErrPayloadTooLarge if the marshalled envelope exceeds
// cfg.MaxMessageBytes.
func (b *domainEventBus) Emit(ctx context.Context, event async.DomainEvent[any]) error {
	if event.EventID == "" && b.cfg.AutoGenerateMissingEventID {
		event.EventID = uuid.NewString()
	}
	body, err := envelope.Marshal(event)
	if err != nil {
		return err
	}
	if b.cfg.MaxMessageBytes > 0 && len(body) > b.cfg.MaxMessageBytes {
		return fmt.Errorf("%w: envelope size %d > %d", ErrPayloadTooLarge, len(body), b.cfg.MaxMessageBytes)
	}
	topic := topicForEvent(b.cfg, event.Name)
	headers := map[string]string{
		"content-type":              "application/json",
		"reactive-commons-envelope": "v1",
		"reactive-commons-kind":     "event",
		"reactive-commons-version":  libraryVersion,
	}
	spanCtx, end := b.obs.startProduceSpan(ctx, topic, "event")
	err = b.producer.publish(spanCtx, topic, []byte(event.EventID), body, headers)
	end(err)
	if err == nil {
		b.obs.messagesEmitted.Add(ctx, 1,
			metricAttrs("kafka", topic, "event"))
	}
	return err
}

// EmitNotification publishes a notification to the topic derived from n.Name
// via cfg.NotificationTopicNameFunc. Mirrors Emit but sets the wire header
// reactive-commons-kind=notification. Blocks until the broker acks per
// cfg.ProducerAcks. Returns ErrPayloadTooLarge if the marshalled envelope
// exceeds cfg.MaxMessageBytes.
func (b *domainEventBus) EmitNotification(ctx context.Context, n async.Notification[any]) error {
	if n.EventID == "" && b.cfg.AutoGenerateMissingEventID {
		n.EventID = uuid.NewString()
	}
	body, err := envelope.Marshal(n)
	if err != nil {
		return err
	}
	if b.cfg.MaxMessageBytes > 0 && len(body) > b.cfg.MaxMessageBytes {
		return fmt.Errorf("%w: envelope size %d > %d", ErrPayloadTooLarge, len(body), b.cfg.MaxMessageBytes)
	}
	topic := topicForNotification(b.cfg, n.Name)
	headers := map[string]string{
		"content-type":              "application/json",
		"reactive-commons-envelope": "v1",
		"reactive-commons-kind":     "notification",
		"reactive-commons-version":  libraryVersion,
	}
	spanCtx, end := b.obs.startProduceSpan(ctx, topic, "notification")
	err = b.producer.publish(spanCtx, topic, []byte(n.EventID), body, headers)
	end(err)
	if err == nil {
		b.obs.messagesEmitted.Add(ctx, 1,
			metricAttrs("kafka", topic, "notification"))
	}
	return err
}
