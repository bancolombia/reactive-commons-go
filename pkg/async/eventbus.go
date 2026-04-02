package async

import "context"

// DomainEventBus publishes domain events and notifications to all subscribed services.
// Implementations MUST block until the broker acknowledges the publish
// (publisher confirm semantics).
type DomainEventBus interface {
	// Emit publishes event to the domainEvents exchange. Returns error if the broker
	// does not acknowledge within the context deadline.
	Emit(ctx context.Context, event DomainEvent[any]) error

	// EmitNotification publishes a non-durable broadcast notification.
	EmitNotification(ctx context.Context, n Notification[any]) error
}
