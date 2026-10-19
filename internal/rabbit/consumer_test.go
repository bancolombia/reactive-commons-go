package rabbit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeConsumerChannel implements consumerChannel and lets tests drive channel
// lifecycle events (broker-initiated close, consumer cancel) deterministically.
// The listener channels are registered asynchronously by the supervisor
// goroutine, so the helpers wait for registration before delivering events.
type fakeConsumerChannel struct {
	mu      sync.Mutex
	closes  chan *amqp.Error
	cancels chan string
	closed  atomic.Bool
}

func (f *fakeConsumerChannel) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	f.mu.Lock()
	f.closes = c
	f.mu.Unlock()
	return c
}

func (f *fakeConsumerChannel) NotifyCancel(c chan string) chan string {
	f.mu.Lock()
	f.cancels = c
	f.mu.Unlock()
	return c
}

func (f *fakeConsumerChannel) Close() error {
	f.closed.Store(true)
	return nil
}

// kill simulates a broker-initiated channel close: it closes the NotifyClose
// listener channel registered by the supervisor.
func (f *fakeConsumerChannel) kill(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.closes != nil
	}, 2*time.Second, time.Millisecond)
	f.mu.Lock()
	close(f.closes)
	f.mu.Unlock()
}

// cancel simulates a broker-sent basic.cancel for the consumer.
func (f *fakeConsumerChannel) cancel(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.cancels != nil
	}, 2*time.Second, time.Millisecond)
	f.mu.Lock()
	f.cancels <- "ctag-test"
	f.mu.Unlock()
}

func consumerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// openConn returns a Connection whose IsOpen reports true without any real
// dial (the zero-value amqp.Connection is not closed).
func openConn() *Connection { return &Connection{conn: &amqp.Connection{}} }

// closedConn returns a Connection whose IsOpen reports false.
func closedConn() *Connection { return &Connection{} }

// newTestConsumer builds a supervisedConsumer with fast rebuild backoff.
func newTestConsumer(conn *Connection, open consumeOpen, dispatch consumeDispatch, ensureTopology func() error) *supervisedConsumer {
	sup := newSupervisedConsumer(conn, consumerTestLogger(), nil, "test", open, dispatch, ensureTopology)
	sup.initialBackoff = 10 * time.Millisecond
	sup.maxBackoff = 10 * time.Millisecond
	return sup
}

func TestConsumer_DispatchesDeliveries(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	received := make(chan amqp.Delivery, 4)
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		opens.Add(1)
		return &fakeConsumerChannel{}, deliveries, nil
	}, func(_ context.Context, d amqp.Delivery) { received <- d }, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	deliveries <- amqp.Delivery{Body: []byte("hello")}
	select {
	case d := <-received:
		assert.Equal(t, "hello", string(d.Body))
	case <-time.After(2 * time.Second):
		t.Fatal("delivery not dispatched")
	}
}

// TestConsumer_SelfHeal_RebuildsChannelWhenConnectionOpen is the regression
// test for the silent consumer death: a broker-killed channel while the
// connection stays healthy must be rebuilt by the supervisor, and the rebuilt
// channel must actually deliver.
func TestConsumer_SelfHeal_RebuildsChannelWhenConnectionOpen(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	d2 := make(chan amqp.Delivery)
	received := make(chan amqp.Delivery, 4)
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, make(chan amqp.Delivery), nil
		}
		return second, d2, nil
	}, func(_ context.Context, d amqp.Delivery) { received <- d }, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	first.kill(t)

	require.Eventually(t, func() bool { return opens.Load() == 2 }, 2*time.Second, 10*time.Millisecond)

	d2 <- amqp.Delivery{Body: []byte("after")}
	select {
	case d := <-received:
		assert.Equal(t, "after", string(d.Body))
	case <-time.After(2 * time.Second):
		t.Fatal("delivery on rebuilt channel not dispatched")
	}
}

// TestConsumer_SkipsSelfHealWhenConnectionDown: a connection-level loss is
// the reconnect hook's job — the supervisor must not race it with a rebuild.
func TestConsumer_SkipsSelfHealWhenConnectionDown(t *testing.T) {
	ch := &fakeConsumerChannel{}
	var opens atomic.Int64
	sup := newTestConsumer(closedConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		opens.Add(1)
		return ch, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	ch.kill(t)

	// Give any (wrong) rebuild plenty of time to happen.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), opens.Load())
}

// TestConsumer_StartWaitsForSupersededSupervisorDispatch is the regression
// test for the double-consume window: a superseded supervisor that is inside
// a (possibly slow) dispatch cannot observe its stop channel, so a restart
// that opened its consumer immediately would briefly consume the same queue
// twice. start must wait for the superseded supervisor to fully exit.
func TestConsumer_StartWaitsForSupersededSupervisorDispatch(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	var opens atomic.Int64
	deliveries := make(chan amqp.Delivery)
	dispatchEntered := make(chan struct{})
	releaseDispatch := make(chan struct{})
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, deliveries, nil
		}
		return second, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {
		close(dispatchEntered)
		<-releaseDispatch
	}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	// Block the first supervisor inside dispatch: it cannot observe its stop
	// channel until the handler returns.
	deliveries <- amqp.Delivery{Body: []byte("block")}
	<-dispatchEntered

	startReturned := make(chan struct{})
	go func() {
		_ = sup.start(ctx, "q")
		close(startReturned)
	}()

	// start must not open the second consumer while the superseded supervisor
	// is still mid-dispatch.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int64(1), opens.Load(), "start opened a consumer while the superseded supervisor was mid-dispatch")

	close(releaseDispatch)
	select {
	case <-startReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not return after the superseded supervisor exited")
	}
	assert.Equal(t, int64(2), opens.Load())
}

// TestConsumer_StartSupersedesPreviousSupervisor guards double consumption: a
// second start (e.g. a connection-level restore racing the self-heal) must
// stop the previous supervisor before it can reopen its channel.
func TestConsumer_StartSupersedesPreviousSupervisor(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, make(chan amqp.Delivery), nil
		}
		return second, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	// Kill the first channel so the old supervisor enters its heal backoff,
	// then start again immediately: the superseded supervisor must exit
	// instead of reopening a second consumer.
	first.kill(t)
	require.NoError(t, sup.start(ctx, "q"))
	assert.Equal(t, int64(2), opens.Load())

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(2), opens.Load(), "superseded supervisor must not reopen its channel")
}

// TestConsumer_RetriesRebuildUntilSuccess: a rebuild that keeps failing
// (e.g. the queue was deleted and topology restore has not run yet) retries
// with backoff until it succeeds.
func TestConsumer_RetriesRebuildUntilSuccess(t *testing.T) {
	first := &fakeConsumerChannel{}
	recovered := &fakeConsumerChannel{}
	d2 := make(chan amqp.Delivery)
	received := make(chan amqp.Delivery, 4)
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		switch opens.Add(1) {
		case 1:
			return first, make(chan amqp.Delivery), nil
		case 2, 3:
			return nil, nil, errors.New("channel refused")
		default:
			return recovered, d2, nil
		}
	}, func(_ context.Context, d amqp.Delivery) { received <- d }, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	first.kill(t)

	require.Eventually(t, func() bool { return opens.Load() >= 4 }, 2*time.Second, 10*time.Millisecond)

	d2 <- amqp.Delivery{Body: []byte("recovered")}
	select {
	case d := <-received:
		assert.Equal(t, "recovered", string(d.Body))
	case <-time.After(2 * time.Second):
		t.Fatal("delivery after retried rebuild not dispatched")
	}
}

// TestConsumer_CancelTriggersSelfHeal: a broker-sent basic.cancel (e.g. the
// queue was deleted) heals the same way a channel close does.
func TestConsumer_CancelTriggersSelfHeal(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, make(chan amqp.Delivery), nil
		}
		return second, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	first.cancel(t)

	require.Eventually(t, func() bool { return opens.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
}

// TestConsumer_EnsureTopologyBeforeRebuild: the topology callback must run
// before the reopen attempt so a queue deleted while the connection stayed up
// is recreated instead of failing the rebuild forever.
func TestConsumer_EnsureTopologyBeforeRebuild(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	var opens atomic.Int64
	var topoCalls atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, make(chan amqp.Delivery), nil
		}
		return second, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {}, func() error {
		topoCalls.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	first.kill(t)

	require.Eventually(t, func() bool { return opens.Load() == 2 }, 2*time.Second, 10*time.Millisecond)
	assert.GreaterOrEqual(t, topoCalls.Load(), int64(1))
}

// TestConsumer_StartFailsWhenInitialOpenFails: Start returns the error so the
// caller's retry loop (restoreWithBackoff) takes over — no supervisor is
// spawned for a channel that never came up.
func TestConsumer_StartFailsWhenInitialOpenFails(t *testing.T) {
	openErr := errors.New("consume failed")
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		return nil, nil, openErr
	}, func(_ context.Context, d amqp.Delivery) {}, nil)

	err := sup.start(context.Background(), "q")
	assert.ErrorIs(t, err, openErr)
}

// TestConsumer_ContextCancelStopsSupervisor: ctx cancellation must stop both
// the consume loop and any in-flight heal attempt.
func TestConsumer_ContextCancelStopsSupervisor(t *testing.T) {
	ch := &fakeConsumerChannel{}
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		opens.Add(1)
		return ch, make(chan amqp.Delivery), nil
	}, func(_ context.Context, d amqp.Delivery) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, sup.start(ctx, "q"))

	ch.kill(t)
	cancel()

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), opens.Load())
}

// TestConsumer_SupersedeWaitTimesOutAndProceeds: a handler stuck in dispatch
// must not block a replacement consumer forever. After the bounded wait, start
// proceeds and logs a warning.
func TestConsumer_SupersedeWaitTimesOutAndProceeds(t *testing.T) {
	first := &fakeConsumerChannel{}
	second := &fakeConsumerChannel{}
	deliveries := make(chan amqp.Delivery)
	dispatchEntered := make(chan struct{})
	releaseDispatch := make(chan struct{})
	var opens atomic.Int64

	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, deliveries, nil
		}
		return second, make(chan amqp.Delivery), nil
	}, func(context.Context, amqp.Delivery) {
		close(dispatchEntered)
		<-releaseDispatch
	}, nil)
	sup.supersedeWait = 50 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))

	deliveries <- amqp.Delivery{Body: []byte("block")}
	<-dispatchEntered

	startReturned := make(chan struct{})
	go func() {
		_ = sup.start(ctx, "q")
		close(startReturned)
	}()

	select {
	case <-startReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("start did not proceed after the supersede wait")
	}
	assert.Equal(t, int64(2), opens.Load())
	close(releaseDispatch)
}

// TestConsumer_HealthTransitions covers running -> rebuilding (with error) ->
// stopped across a broker channel kill and context cancellation.
func TestConsumer_HealthTransitions(t *testing.T) {
	first := &fakeConsumerChannel{}
	openErr := errors.New("channel refused")
	var opens atomic.Int64

	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		if opens.Add(1) == 1 {
			return first, make(chan amqp.Delivery), nil
		}
		return nil, nil, openErr
	}, func(context.Context, amqp.Delivery) {}, nil)
	sup.initialBackoff = 100 * time.Millisecond
	sup.maxBackoff = 100 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, sup.start(ctx, "q"))
	require.Eventually(t, func() bool { return sup.snapshot().State == ConsumerRunning }, 2*time.Second, 5*time.Millisecond)

	first.kill(t)
	require.Eventually(t, func() bool {
		s := sup.snapshot()
		return s.State == ConsumerRebuilding && s.LastError != ""
	}, 2*time.Second, 5*time.Millisecond)

	cancel()
	require.Eventually(t, func() bool { return sup.snapshot().State == ConsumerStopped }, 2*time.Second, 5*time.Millisecond)
}

// TestConsumer_HealthSupersededGenerationNotClobbered: a late exit from an old
// supervisor must not mark the replacement stopped.
func TestConsumer_HealthSupersededGenerationNotClobbered(t *testing.T) {
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		return &fakeConsumerChannel{}, make(chan amqp.Delivery), nil
	}, func(context.Context, amqp.Delivery) {}, nil)

	sup.stateMu.Lock()
	sup.runGen = 2
	sup.setHealthLocked(ConsumerRunning, nil)
	sup.stateMu.Unlock()

	sup.setHealthForGen(1, ConsumerStopped, errors.New("stale exit"))

	assert.Equal(t, ConsumerRunning, sup.snapshot().State)
}

// TestConsumer_StartAfterCtxCancelDoesNotLaunch: a shutdown racing the initial
// open must close the fresh channel instead of launching a consumer.
func TestConsumer_StartAfterCtxCancelDoesNotLaunch(t *testing.T) {
	ch := &fakeConsumerChannel{}
	var opens atomic.Int64
	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		opens.Add(1)
		return ch, make(chan amqp.Delivery), nil
	}, func(context.Context, amqp.Delivery) {}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sup.start(ctx, "q")

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(1), opens.Load())
	assert.True(t, ch.closed.Load(), "fresh channel must be closed on a cancelled start")
	assert.Equal(t, ConsumerStopped, sup.snapshot().State)
}
