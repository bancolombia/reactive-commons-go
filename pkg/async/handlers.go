package async

import "context"

// EventHandler processes a received domain event.
// Return non-nil error to nack the message (triggers redelivery or DLQ routing).
type EventHandler[T any] func(ctx context.Context, event DomainEvent[T]) error

// CommandHandler processes a received command.
// Return non-nil error to nack the message (triggers redelivery or DLQ routing).
type CommandHandler[T any] func(ctx context.Context, cmd Command[T]) error

// QueryHandler processes a query and returns the response payload.
// Return (response, nil) on success; return (zero, error) to send an error reply.
// The from parameter carries reply-routing metadata needed to call Gateway.Reply.
type QueryHandler[Req, Res any] func(ctx context.Context, query AsyncQuery[Req], from From) (Res, error)

// NotificationHandler processes a received notification.
// Errors are logged; notifications are never redelivered (non-durable semantics).
type NotificationHandler[T any] func(ctx context.Context, n Notification[T]) error
