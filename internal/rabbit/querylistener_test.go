package rabbit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	hdr "github.com/bancolombia/reactive-commons-go/pkg/headers"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeaderString_NilHeaders(t *testing.T) {
	assert.Equal(t, "", headerString(nil, "x-correlation-id"))
}

func TestHeaderString_MissingKey(t *testing.T) {
	headers := amqp.Table{"some-other-key": "value"}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_StringValue(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": "corr-123"}
	assert.Equal(t, "corr-123", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_NonStringValue_ReturnsEmpty(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": 12345}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}

func TestHeaderString_NilValue_ReturnsEmpty(t *testing.T) {
	headers := amqp.Table{"x-correlation-id": nil}
	assert.Equal(t, "", headerString(headers, "x-correlation-id"))
}

// fakeAcknowledger records acks without a broker connection.
type fakeAcknowledger struct {
	acked  atomic.Int64
	nacked atomic.Int64
}

func (f *fakeAcknowledger) Ack(uint64, bool) error {
	f.acked.Add(1)
	return nil
}

func (f *fakeAcknowledger) Nack(uint64, bool, bool) error {
	f.nacked.Add(1)
	return nil
}

func (f *fakeAcknowledger) Reject(uint64, bool) error {
	f.nacked.Add(1)
	return nil
}

// TestQueryListener_BoundsReplyWithHeaderTimeout: the reply send must use the
// caller's x-reply-timeout-millis, not the app-lifetime context. Without this
// bound a lost confirm would wedge the query consumer indefinitely.
func TestQueryListener_BoundsReplyWithHeaderTimeout(t *testing.T) {
	snd := &recordingSender{}
	gw := newGateway(snd, Config{AppName: "svc", ReplyTimeout: time.Hour})
	reg := newHandlerRegistry()
	require.NoError(t, reg.ServeQuery("q", func(context.Context, async.AsyncQuery[any], async.From) (any, error) {
		return map[string]string{"ok": "yes"}, nil
	}))
	l := &queryListener{reg: reg, gw: gw, cfg: Config{AppName: "svc"}, log: restoreTestLogger()}
	ack := &fakeAcknowledger{}

	l.dispatch(context.Background(), amqp.Delivery{
		Body: []byte(`{"resource":"q","queryData":{}}`),
		Headers: amqp.Table{
			hdr.CorrelationID:      "corr-1",
			hdr.ReplyID:            "svc.replies",
			hdr.ReplyTimeoutMillis: "50",
		},
		Acknowledger: ack,
	})

	calls, hasDeadline, deadline, headers := snd.snapshot()
	require.Equal(t, 1, calls, "reply must be published")
	assert.True(t, hasDeadline, "reply send must have a bounded context")
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), deadline, 200*time.Millisecond)
	assert.Equal(t, "corr-1", headers[hdr.CorrelationID])
	assert.Equal(t, int64(1), ack.acked.Load())
}
