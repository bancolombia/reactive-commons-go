package unit_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/stretchr/testify/assert"
)

func newTestRouter() *replyrouter.ReplyRouter {
	return replyrouter.NewReplyRouter()
}

func TestReplyRouter_Register_ReceivesRoutedMessage(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-1")

	go router.Route("corr-1", replyrouter.ReplyPayload{Body: []byte(`{"result":"ok"}`)})

	select {
	case p := <-ch:
		assert.Equal(t, `{"result":"ok"}`, string(p.Body))
		assert.False(t, p.IsError)
		assert.False(t, p.IsEmpty)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for routed message")
	}
}

func TestReplyRouter_Deregister_CleansUp(t *testing.T) {
	router := newTestRouter()
	router.Register("corr-2")
	router.Deregister("corr-2")

	// After deregister, routing should be a no-op (no panic, no block)
	assert.NotPanics(t, func() {
		router.Route("corr-2", replyrouter.ReplyPayload{Body: []byte(`{}`)})
	})
}

func TestReplyRouter_Route_UnknownCorrelation_IsNoOp(t *testing.T) {
	router := newTestRouter()
	assert.NotPanics(t, func() {
		router.Route("unknown-id", replyrouter.ReplyPayload{Body: []byte(`{}`)})
	})
}

func TestReplyRouter_LateReply_IsDiscarded(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-3")

	// Drain the channel first
	router.Route("corr-3", replyrouter.ReplyPayload{Body: []byte(`first`)})
	<-ch

	// A second route after the channel has been read should not block
	done := make(chan struct{})
	go func() {
		router.Route("corr-3", replyrouter.ReplyPayload{Body: []byte(`late`)})
		close(done)
	}()

	select {
	case <-done:
		// Good — didn't block
	case <-time.After(time.Second):
		t.Fatal("Route blocked on a full channel")
	}
}

// T040: route to already-deregistered correlationID must not panic or leak.
func TestReplyRouter_RouteAfterDeregister_IsNoOp(t *testing.T) {
	router := newTestRouter()
	router.Register("corr-4")
	router.Deregister("corr-4")

	assert.NotPanics(t, func() {
		router.Route("corr-4", replyrouter.ReplyPayload{Body: []byte(`late-data`)})
	})
}

func TestReplyRouter_ErrorPayload_IsRouted(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-err")

	go router.Route("corr-err", replyrouter.ReplyPayload{
		Body:    []byte(`{"errorMessage":"something went wrong"}`),
		IsError: true,
	})

	select {
	case p := <-ch:
		assert.True(t, p.IsError)
		assert.Contains(t, string(p.Body), "something went wrong")
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

// Concurrent Route and Deregister for the same correlation ID must never
// panic (a Route interleaved between Load and Deregister used to send on a
// closed channel). Run under -race to also catch map-level races.
func TestReplyRouter_ConcurrentRouteAndDeregister_NoPanic(t *testing.T) {
	router := newTestRouter()

	const workers = 8
	const iterations = 500
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				id := fmt.Sprintf("corr-%d-%d", w, i)
				ch := router.Register(id)

				var inner sync.WaitGroup
				inner.Add(1)
				go func() {
					defer inner.Done()
					router.Route(id, replyrouter.ReplyPayload{Body: []byte(`{"x":1}`)})
				}()
				router.Deregister(id)
				inner.Wait()

				// Best-effort drain; the channel may or may not hold a value.
				select {
				case <-ch:
				default:
				}
			}
		}(w)
	}
	wg.Wait()
}
