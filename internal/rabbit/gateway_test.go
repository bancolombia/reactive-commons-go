package rabbit

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/internal/replyrouter"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
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
	router := replyrouter.NewReplyRouter()

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

// recordingSender captures the context deadline and headers of each send so
// tests can assert the reply timeout plumbing.
type recordingSender struct {
	mu          sync.Mutex
	calls       int
	hasDeadline bool
	deadline    time.Time
	headers     amqp.Table
}

func (s *recordingSender) SendWithConfirm(ctx context.Context, _ []byte, _, _ string, headers amqp.Table, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.deadline, s.hasDeadline = ctx.Deadline()
	s.headers = headers
	return nil
}

func (s *recordingSender) SendNoConfirm(context.Context, []byte, string, string, amqp.Table, bool) error {
	return nil
}

func (s *recordingSender) snapshot() (int, bool, time.Time, amqp.Table) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.hasDeadline, s.deadline, s.headers
}

// TestGateway_RequestReply_AppliesDefaultTimeout: a caller context without a
// deadline is bounded by ReplyTimeout, and the header advertises it.
func TestGateway_RequestReply_AppliesDefaultTimeout(t *testing.T) {
	snd := &recordingSender{}
	gw := newGateway(snd, Config{AppName: "svc", ReplyTimeout: 50 * time.Millisecond})
	gw.withReplySupport("svc.replies", replyrouter.NewReplyRouter())

	start := time.Now()
	_, err := gw.RequestReply(context.Background(), async.AsyncQuery[any]{Resource: "q"}, "target")

	require.ErrorIs(t, err, async.ErrQueryTimeout)
	assert.Less(t, time.Since(start), 2*time.Second, "RequestReply ignored the default timeout")

	calls, hasDeadline, deadline, headers := snd.snapshot()
	assert.Equal(t, 1, calls)
	assert.True(t, hasDeadline)
	assert.WithinDuration(t, start.Add(50*time.Millisecond), deadline, 200*time.Millisecond)
	ms, err := strconv.Atoi(headers[hdr.ReplyTimeoutMillis].(string))
	require.NoError(t, err)
	assert.InDelta(t, 50, ms, 1, "header must advertise the default timeout")
}

// TestGateway_RequestReply_CallerDeadlineWins: an explicit caller deadline is
// never replaced by the config default.
func TestGateway_RequestReply_CallerDeadlineWins(t *testing.T) {
	snd := &recordingSender{}
	gw := newGateway(snd, Config{AppName: "svc", ReplyTimeout: time.Millisecond})
	gw.withReplySupport("svc.replies", replyrouter.NewReplyRouter())

	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	// End the wait quickly; the assertion is about the deadline captured by the
	// send, not about waiting an hour.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "q"}, "target")
	require.ErrorIs(t, err, async.ErrQueryTimeout)

	_, hasDeadline, deadline, _ := snd.snapshot()
	assert.True(t, hasDeadline)
	assert.True(t, deadline.After(time.Now().Add(30*time.Minute)), "caller deadline must not be shortened")
}

// TestGateway_ReplyContext_HeaderPrecedence covers string and numeric header
// encodings plus the fallback rules.
func TestGateway_ReplyContext_HeaderPrecedence(t *testing.T) {
	gw := newGateway(nil, Config{ReplyTimeout: 5 * time.Second})

	cases := []struct {
		name    string
		headers amqp.Table
		want    time.Duration
	}{
		{"string millis", amqp.Table{hdr.ReplyTimeoutMillis: "50"}, 50 * time.Millisecond},
		{"bytes millis", amqp.Table{hdr.ReplyTimeoutMillis: []byte("75")}, 75 * time.Millisecond},
		{"int millis", amqp.Table{hdr.ReplyTimeoutMillis: 80}, 80 * time.Millisecond},
		{"int32 millis", amqp.Table{hdr.ReplyTimeoutMillis: int32(90)}, 90 * time.Millisecond},
		{"int64 millis", amqp.Table{hdr.ReplyTimeoutMillis: int64(100)}, 100 * time.Millisecond},
		{"float millis", amqp.Table{hdr.ReplyTimeoutMillis: float64(110)}, 110 * time.Millisecond},
		{"zero falls back", amqp.Table{hdr.ReplyTimeoutMillis: "0"}, 5 * time.Second},
		{"malformed falls back", amqp.Table{hdr.ReplyTimeoutMillis: "abc"}, 5 * time.Second},
		{"negative falls back", amqp.Table{hdr.ReplyTimeoutMillis: int64(-1)}, 5 * time.Second},
		{"missing falls back", nil, 5 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := gw.replyContext(context.Background(), tc.headers)
			defer cancel()
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			assert.WithinDuration(t, time.Now().Add(tc.want), deadline, 200*time.Millisecond)
		})
	}
}

func TestGateway_ReplyContext_NoConfigTimeoutLeavesContextUnbounded(t *testing.T) {
	gw := newGateway(nil, Config{})
	ctx, cancel := gw.replyContext(context.Background(), nil)
	defer cancel()

	_, ok := ctx.Deadline()
	assert.False(t, ok)
}
