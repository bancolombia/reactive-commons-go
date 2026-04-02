package async

import "context"

// Application is the top-level lifecycle interface for a reactive-commons instance.
type Application interface {
	// Registry returns the HandlerRegistry for registering message handlers.
	Registry() HandlerRegistry

	// EventBus returns the DomainEventBus for publishing domain events and notifications.
	EventBus() DomainEventBus

	// Gateway returns the DirectAsyncGateway for sending commands and queries.
	Gateway() DirectAsyncGateway

	// Start declares broker topology, starts all consumers, and begins processing messages.
	// Blocks until the context is cancelled. Returns any fatal startup error.
	Start(ctx context.Context) error
}
