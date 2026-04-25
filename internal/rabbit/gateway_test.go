package rabbit

import (
	"context"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateway_RequestReply_ErrorWhenReplyQueueNotInitialized(t *testing.T) {
	gw := newGateway(nil, Config{AppName: "svc"})

	q := async.AsyncQuery[any]{Resource: "get-product"}
	body, err := gw.RequestReply(context.Background(), q, "target")

	require.Error(t, err)
	assert.Nil(t, body)
	assert.Contains(t, err.Error(), "reply queue not initialized")
}

func TestGateway_WithReplySupport_StoresQueueAndRouter(t *testing.T) {
	gw := newGateway(nil, Config{AppName: "svc"})
	router := NewReplyRouter()

	gw.withReplySupport("svc.replies.abc", router)

	assert.Equal(t, "svc.replies.abc", gw.replyQueue)
	assert.Same(t, router, gw.replyRouter)
}

// Reply must surface a json.Marshal error before touching the sender, which
// keeps the marshal-failure branch reachable in unit tests where no broker
// connection exists.
func TestGateway_Reply_MarshalError(t *testing.T) {
	gw := newGateway(nil, Config{AppName: "svc"})

	// chan int cannot be marshaled to JSON; the call must return an error
	// (and not panic on the nil sender, since marshal happens first).
	err := gw.Reply(context.Background(), make(chan int), async.From{
		CorrelationID: "corr",
		ReplyID:       "reply-q",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal query reply")
}

