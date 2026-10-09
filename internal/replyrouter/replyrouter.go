// Package replyrouter correlates async-query replies to their waiting callers.
// It is shared by the rabbit and kafka transports.
package replyrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// ReplyPayload carries a query reply body and its metadata flags.
type ReplyPayload struct {
	Body    []byte
	IsError bool // x-reply-error: true
	IsEmpty bool // x-empty-completion: true
}

// ReplyRouter correlates async query replies to their waiting callers using
// a sync.Map from correlationID string to chan ReplyPayload.
type ReplyRouter struct {
	channels sync.Map // map[string]chan ReplyPayload
}

func NewReplyRouter() *ReplyRouter {
	return &ReplyRouter{}
}

// Register creates a reply channel for the given correlationID and returns it.
// The caller should read from the channel (with a context timeout) and then
// call Deregister when done.
func (r *ReplyRouter) Register(correlationID string) chan ReplyPayload {
	ch := make(chan ReplyPayload, 1)
	r.channels.Store(correlationID, ch)
	return ch
}

// Route delivers payload to the channel registered for correlationID.
// If no channel is registered (e.g., the caller already timed out), the call is a no-op.
func (r *ReplyRouter) Route(correlationID string, payload ReplyPayload) {
	if v, ok := r.channels.Load(correlationID); ok {
		if ch, ok := v.(chan ReplyPayload); ok {
			select {
			case ch <- payload:
			default:
				// Channel full or closed — discard late reply silently.
			}
		}
	}
}

// Deregister removes the channel for correlationID. The channel is not
// closed: a concurrent Route may hold a reference and a send on a closed
// channel would panic. Callers stop reading after Deregister, so leaving the
// channel open is safe.
func (r *ReplyRouter) Deregister(correlationID string) {
	r.channels.LoadAndDelete(correlationID)
}

// AwaitReply blocks until a reply arrives on ch or ctx is cancelled, and
// decodes the shared error/empty/body conventions used by every transport.
// Returns async.ErrQueryTimeout on cancellation.
func AwaitReply(ctx context.Context, ch <-chan ReplyPayload) (json.RawMessage, error) {
	select {
	case p := <-ch:
		if p.IsError {
			var errBody struct {
				ErrorMessage string `json:"errorMessage"`
			}
			// Best-effort unmarshal — use raw body as fallback if it fails.
			if unmarshalErr := json.Unmarshal(p.Body, &errBody); unmarshalErr != nil {
				return nil, fmt.Errorf("reactive-commons: query handler error: %s", p.Body)
			}
			return nil, fmt.Errorf("reactive-commons: query handler error: %s", errBody.ErrorMessage)
		}
		if p.IsEmpty {
			return nil, nil
		}
		return json.RawMessage(p.Body), nil
	case <-ctx.Done():
		return nil, async.ErrQueryTimeout
	}
}
