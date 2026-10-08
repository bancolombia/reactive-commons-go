package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePublisher records the last published message and can trigger a side
// effect (e.g. routing a reply) when a publish happens.
type fakePublisher struct {
	mu        sync.Mutex
	topic     string
	key       []byte
	value     []byte
	headers   map[string]string
	err       error
	onPublish func(topic string, key, value []byte, headers map[string]string)
}

func (f *fakePublisher) publish(_ context.Context, topic string, key, value []byte, headers map[string]string) error {
	f.mu.Lock()
	f.topic, f.key, f.value, f.headers = topic, key, value, headers
	cb := f.onPublish
	f.mu.Unlock()
	if cb != nil {
		cb(topic, key, value, headers)
	}
	return f.err
}

func (f *fakePublisher) lastTopic() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.topic
}

func baseGwConfig() Config {
	return Config{AppName: "svc", InstanceID: "inst-1", MaxMessageBytes: 1_000_000}
}

func newTestGateway(cfg Config, pub *fakePublisher, primed <-chan struct{}) (*kafkaGateway, *replyrouter.ReplyRouter) {
	router := replyrouter.NewReplyRouter()
	return newGateway(cfg, pub, router, primed, resolveObservability(cfg)), router
}

func closedChan() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestGateway_RequestReply_Success(t *testing.T) {
	pub := &fakePublisher{}
	gw, router := newTestGateway(baseGwConfig(), pub, closedChan())
	pub.onPublish = func(_ string, key, _ []byte, _ map[string]string) {
		router.Route(string(key), replyrouter.ReplyPayload{Body: []byte(`{"ok":true}`)})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := gw.RequestReply(ctx,
		async.AsyncQuery[any]{Resource: "get-product", QueryData: json.RawMessage(`{"id":1}`)},
		"remote")
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(body))

	// Wire assertions on the published query.
	assert.Equal(t, "remote.queries", pub.topic)
	assert.Equal(t, "query", pub.headers["reactive-commons-kind"])
	assert.Equal(t, "svc", pub.headers[hdr.SourceApplication])
	assert.Equal(t, "svc.replies", pub.headers[hdr.ReplyID])
	assert.Equal(t, "get-product", pub.headers[hdr.ServedQueryID])
	assert.NotEmpty(t, pub.headers[hdr.CorrelationID])
	assert.NotEqual(t, "0", pub.headers[hdr.ReplyTimeoutMillis])
	assert.Equal(t, pub.headers[hdr.CorrelationID], string(pub.key))
	assert.JSONEq(t, `{"resource":"get-product","queryData":{"id":1}}`, string(pub.value))
}

func TestGateway_RequestReply_ErrorReply(t *testing.T) {
	pub := &fakePublisher{}
	gw, router := newTestGateway(baseGwConfig(), pub, closedChan())
	pub.onPublish = func(_ string, key, _ []byte, _ map[string]string) {
		router.Route(string(key), replyrouter.ReplyPayload{
			Body:    []byte(`{"errorMessage":"boom"}`),
			IsError: true,
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "remote")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reactive-commons: query handler error: boom")
}

func TestGateway_RequestReply_ErrorReplyUnmarshalFallback(t *testing.T) {
	pub := &fakePublisher{}
	gw, router := newTestGateway(baseGwConfig(), pub, closedChan())
	pub.onPublish = func(_ string, key, _ []byte, _ map[string]string) {
		router.Route(string(key), replyrouter.ReplyPayload{Body: []byte(`not-json`), IsError: true})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "remote")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reactive-commons: query handler error: not-json")
}

func TestGateway_RequestReply_EmptyCompletion(t *testing.T) {
	pub := &fakePublisher{}
	gw, router := newTestGateway(baseGwConfig(), pub, closedChan())
	pub.onPublish = func(_ string, key, _ []byte, _ map[string]string) {
		router.Route(string(key), replyrouter.ReplyPayload{Body: []byte("null"), IsEmpty: true})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "remote")
	require.NoError(t, err)
	assert.Nil(t, body)
}

func TestGateway_RequestReply_Timeout(t *testing.T) {
	gw, _ := newTestGateway(baseGwConfig(), &fakePublisher{}, closedChan())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "remote")
	assert.ErrorIs(t, err, async.ErrQueryTimeout)
}

func TestGateway_RequestReply_WaitsForPriming(t *testing.T) {
	pub := &fakePublisher{}
	primed := make(chan struct{}) // never closed
	gw, _ := newTestGateway(baseGwConfig(), pub, primed)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "remote")
	assert.ErrorIs(t, err, async.ErrQueryTimeout)
	assert.Empty(t, pub.lastTopic(), "query must not be published before priming")
}

func TestGateway_RequestReply_DisabledReplyListener(t *testing.T) {
	cfg := baseGwConfig()
	cfg.DisableReplyListener = true
	gw, _ := newTestGateway(cfg, &fakePublisher{}, nil)

	_, err := gw.RequestReply(context.Background(), async.AsyncQuery[any]{Resource: "q"}, "remote")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DisableReplyListener")
}

func TestGateway_RequestReply_EmptyTarget(t *testing.T) {
	gw, _ := newTestGateway(baseGwConfig(), &fakePublisher{}, closedChan())
	_, err := gw.RequestReply(context.Background(), async.AsyncQuery[any]{Resource: "q"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targetService is required")
}

func TestGateway_RequestReply_PublishError(t *testing.T) {
	pub := &fakePublisher{err: errors.New("broker down")}
	gw, _ := newTestGateway(baseGwConfig(), pub, closedChan())
	_, err := gw.RequestReply(context.Background(), async.AsyncQuery[any]{Resource: "q"}, "remote")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker down")
}

func TestGateway_SendCommand_Validation(t *testing.T) {
	gw, _ := newTestGateway(baseGwConfig(), &fakePublisher{}, closedChan())

	err := gw.SendCommand(context.Background(), async.Command[any]{Name: "x"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targetService is required")

	err = gw.SendCommand(context.Background(), async.Command[any]{}, "target")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "command name is required")
}

func TestGateway_SendCommand_PublishesEnvelope(t *testing.T) {
	pub := &fakePublisher{}
	gw, _ := newTestGateway(baseGwConfig(), pub, closedChan())

	cmd := async.Command[any]{Name: "orders.create", CommandID: "cmd-1", Data: json.RawMessage(`{"id":1}`)}
	require.NoError(t, gw.SendCommand(context.Background(), cmd, "target"))

	assert.Equal(t, "target.commands", pub.topic)
	assert.Equal(t, "command", pub.headers["reactive-commons-kind"])
	assert.Equal(t, "svc", pub.headers[hdr.SourceApplication])
	assert.Equal(t, "cmd-1", string(pub.key))
	assert.JSONEq(t, `{"name":"orders.create","commandId":"cmd-1","data":{"id":1}}`, string(pub.value))
}

func TestGateway_Reply_PublishesResponse(t *testing.T) {
	pub := &fakePublisher{}
	gw, _ := newTestGateway(baseGwConfig(), pub, closedChan())

	from := async.From{CorrelationID: "corr-1", ReplyID: "caller.replies"}
	require.NoError(t, gw.Reply(context.Background(), map[string]string{"result": "ok"}, from))

	assert.Equal(t, "caller.replies", pub.topic)
	assert.Equal(t, "reply", pub.headers["reactive-commons-kind"])
	assert.Equal(t, "corr-1", pub.headers[hdr.CorrelationID])
	assert.Equal(t, "corr-1", string(pub.key))
	assert.JSONEq(t, `{"result":"ok"}`, string(pub.value))
	assert.NotContains(t, pub.headers, hdr.CompletionOnlySignal)
}

func TestGateway_Reply_EmptyCompletion(t *testing.T) {
	pub := &fakePublisher{}
	gw, _ := newTestGateway(baseGwConfig(), pub, closedChan())

	from := async.From{CorrelationID: "corr-2", ReplyID: "caller.replies"}
	require.NoError(t, gw.Reply(context.Background(), nil, from))

	assert.Equal(t, "null", string(pub.value))
	assert.Equal(t, "true", pub.headers[hdr.CompletionOnlySignal])
}

func TestGateway_Reply_MarshalError(t *testing.T) {
	gw, _ := newTestGateway(baseGwConfig(), &fakePublisher{}, closedChan())

	// chan int cannot be marshaled to JSON.
	err := gw.Reply(context.Background(), make(chan int), async.From{CorrelationID: "c", ReplyID: "r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal query reply")
}

func TestGateway_Reply_MissingRoutingMetadata(t *testing.T) {
	gw, _ := newTestGateway(baseGwConfig(), &fakePublisher{}, closedChan())

	err := gw.Reply(context.Background(), "x", async.From{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing reply routing metadata")
}

func TestGateway_ReplyError_PublishesErrorReply(t *testing.T) {
	pub := &fakePublisher{}
	gw, _ := newTestGateway(baseGwConfig(), pub, closedChan())

	from := async.From{CorrelationID: "corr-3", ReplyID: "caller.replies"}
	require.NoError(t, gw.replyError(context.Background(), "boom", from))

	assert.Equal(t, "caller.replies", pub.topic)
	assert.Equal(t, "true", pub.headers[hdr.ReplyError])
	assert.Equal(t, "corr-3", pub.headers[hdr.CorrelationID])
	assert.JSONEq(t, `{"errorMessage":"boom"}`, string(pub.value))
}

func TestGateway_SendCommand_PayloadTooLarge(t *testing.T) {
	cfg := baseGwConfig()
	cfg.MaxMessageBytes = 10
	gw, _ := newTestGateway(cfg, &fakePublisher{}, closedChan())

	err := gw.SendCommand(context.Background(), async.Command[any]{Name: "c", Data: "0123456789"}, "target")
	assert.ErrorIs(t, err, ErrPayloadTooLarge)
}

func TestNotReadyGateway_AllMethodsError(t *testing.T) {
	gw := notReadyGateway{}
	ctx := context.Background()

	assert.Error(t, gw.SendCommand(ctx, async.Command[any]{Name: "x"}, "t"))
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "x"}, "t")
	assert.Error(t, err)
	assert.Error(t, gw.Reply(ctx, nil, async.From{}))
}
