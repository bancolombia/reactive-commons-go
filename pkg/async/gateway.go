package async

import (
	"context"
	"encoding/json"
)

// DirectAsyncGateway sends commands and queries to named target services.
type DirectAsyncGateway interface {
	// SendCommand delivers cmd to targetService. Blocks until broker confirms.
	// Returns error on broker failure or context cancellation.
	SendCommand(ctx context.Context, cmd Command[any], targetService string) error

	// RequestReply sends query to targetService and waits for the typed response.
	// Returns (raw JSON bytes, nil) on success.
	// Returns (nil, ErrQueryTimeout) when ctx deadline is exceeded.
	// Returns (nil, error) on broker failure or handler error reply.
	RequestReply(ctx context.Context, query AsyncQuery[any], targetService string) (json.RawMessage, error)

	// Reply sends a query response to the original caller.
	// from MUST be the From value received in the QueryHandler invocation.
	// Pass nil response to send a completion-only signal (x-empty-completion: true).
	Reply(ctx context.Context, response any, from From) error
}
