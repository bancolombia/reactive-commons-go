package rabbit

import (
	"sync"
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

// Deregister removes the channel for correlationID and closes it.
func (r *ReplyRouter) Deregister(correlationID string) {
	if v, ok := r.channels.LoadAndDelete(correlationID); ok {
		if ch, ok := v.(chan ReplyPayload); ok {
			close(ch)
		}
	}
}
