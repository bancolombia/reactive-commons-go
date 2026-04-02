package rabbit

import (
	"context"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
)

type eventBus struct {
	sender *Sender
	cfg    Config
}

func newEventBus(sender *Sender, cfg Config) *eventBus {
	return &eventBus{sender: sender, cfg: cfg}
}

func (b *eventBus) Emit(ctx context.Context, event async.DomainEvent[any]) error {
	body, err := ToMessage(event)
	if err != nil {
		return err
	}
	headers := amqp.Table{hdr.SourceApplication: b.cfg.AppName}
	return b.sender.SendWithConfirm(ctx, body, b.cfg.DomainEventsExchange, event.Name, headers, b.cfg.PersistentEvents)
}

func (b *eventBus) EmitNotification(ctx context.Context, n async.Notification[any]) error {
	body, err := ToMessage(n)
	if err != nil {
		return err
	}
	headers := amqp.Table{hdr.SourceApplication: b.cfg.AppName}
	return b.sender.SendNoConfirm(ctx, body, b.cfg.DomainEventsExchange, n.Name, headers, false)
}
