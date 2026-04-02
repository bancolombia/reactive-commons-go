package unit_test

import (
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/rabbit"
	"github.com/stretchr/testify/assert"
)

func newTestRouter() *rabbit.ReplyRouter {
	return rabbit.NewReplyRouter()
}

func TestReplyRouter_Register_ReceivesRoutedMessage(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-1")

	go router.Route("corr-1", rabbit.ReplyPayload{Body: []byte(`{"result":"ok"}`)})

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
		router.Route("corr-2", rabbit.ReplyPayload{Body: []byte(`{}`)})
	})
}

func TestReplyRouter_Route_UnknownCorrelation_IsNoOp(t *testing.T) {
	router := newTestRouter()
	assert.NotPanics(t, func() {
		router.Route("unknown-id", rabbit.ReplyPayload{Body: []byte(`{}`)})
	})
}

func TestReplyRouter_LateReply_IsDiscarded(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-3")

	// Drain the channel first
	router.Route("corr-3", rabbit.ReplyPayload{Body: []byte(`first`)})
	<-ch

	// A second route after the channel has been read should not block
	done := make(chan struct{})
	go func() {
		router.Route("corr-3", rabbit.ReplyPayload{Body: []byte(`late`)})
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
		router.Route("corr-4", rabbit.ReplyPayload{Body: []byte(`late-data`)})
	})
}

func TestReplyRouter_ErrorPayload_IsRouted(t *testing.T) {
	router := newTestRouter()
	ch := router.Register("corr-err")

	go router.Route("corr-err", rabbit.ReplyPayload{
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
