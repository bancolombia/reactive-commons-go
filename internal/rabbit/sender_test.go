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

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChannel implements senderChannel and lets tests drive broker
// confirmations and channel closure deterministically. The listener channels
// are registered asynchronously by the watcher goroutine, so the helpers wait
// for registration before delivering events.
type fakeChannel struct {
	mu       sync.Mutex
	confirms chan amqp.Confirmation
	closes   chan *amqp.Error

	publishErr error
	publishes  atomic.Int64
	closed     atomic.Bool
}

func (f *fakeChannel) Confirm(bool) error { return nil }

func (f *fakeChannel) NotifyPublish(c chan amqp.Confirmation) chan amqp.Confirmation {
	f.mu.Lock()
	f.confirms = c
	f.mu.Unlock()
	return c
}

func (f *fakeChannel) NotifyClose(c chan *amqp.Error) chan *amqp.Error {
	f.mu.Lock()
	f.closes = c
	f.mu.Unlock()
	return c
}

func (f *fakeChannel) PublishWithContext(_ context.Context, _, _ string, _, _ bool, _ amqp.Publishing) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	f.publishes.Add(1)
	return nil
}

func (f *fakeChannel) IsClosed() bool { return f.closed.Load() }

func (f *fakeChannel) Close() error {
	f.closed.Store(true)
	return nil
}

// confirm delivers the confirmation for the given delivery tag.
func (f *fakeChannel) confirm(t *testing.T, tag uint64, ack bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.confirms != nil
	}, 2*time.Second, time.Millisecond)
	select {
	case f.confirms <- amqp.Confirmation{DeliveryTag: tag, Ack: ack}:
	case <-time.After(2 * time.Second):
		t.Fatal("confirm not consumed by watcher")
	}
}

// killChannel simulates a broker-initiated channel close.
func (f *fakeChannel) killChannel(t *testing.T) {
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

// waitPublish blocks until the channel has accepted at least one publish, so
// tests never deliver a confirm before the pending slot is registered.
func (f *fakeChannel) waitPublish(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return f.publishes.Load() >= 1 }, 2*time.Second, time.Millisecond)
}

// newTestSender builds a Sender whose channels come from open and whose
// self-heal decision comes from connOpen. Rebuild backoff is instant;
// tests exercising the retry loop override rebuildDelay/rebuildMaxDelay.
func newTestSender(open func() (senderChannel, error), connOpen func() bool) *Sender {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Sender{
		cfg:             Config{AppName: "test"},
		log:             log,
		pending:         make(map[uint64]chan error),
		publishSem:      make(chan struct{}, 1),
		rebuildDelay:    0,
		rebuildMaxDelay: 10 * time.Millisecond,
		openCh:          open,
		connOpen:        connOpen,
	}
	if _, err := s.rebuild(0, false); err != nil {
		panic(err)
	}
	return s
}

// pendingCount reads the pending map under the sender lock.
func pendingCount(s *Sender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func TestSender_SendWithConfirm_ReleasedByAck(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.SendWithConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
	}()

	ch.waitPublish(t)
	ch.confirm(t, 1, true)

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("SendWithConfirm not released by ack")
	}
}

func TestSender_SendWithConfirm_ReleasedByNack(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.SendWithConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
	}()

	ch.waitPublish(t)
	ch.confirm(t, 1, false)

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	case <-time.After(2 * time.Second):
		t.Fatal("SendWithConfirm not released by nack")
	}
}

// TestSender_SendWithConfirm_ReleasedByChannelClose is the regression test for
// the hang where a broker-killed channel orphaned every in-flight waiter until
// its context expired.
func TestSender_SendWithConfirm_ReleasedByChannelClose(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		errCh <- s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false)
	}()

	ch.waitPublish(t)
	ch.killChannel(t)

	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not released by channel close")
	}
	assert.Equal(t, 0, pendingCount(s))
}

func TestSender_SelfHeal_RebuildsChannelWhenConnectionOpen(t *testing.T) {
	first := &fakeChannel{}
	second := &fakeChannel{}
	var calls atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, func() bool { return true })

	first.killChannel(t)

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ch == second
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, int64(2), calls.Load())
}

func TestSender_SelfHeal_SkipsWhenConnectionDown(t *testing.T) {
	ch := &fakeChannel{}
	var calls atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		calls.Add(1)
		return ch, nil
	}, func() bool { return false })

	ch.killChannel(t)

	// Give any (wrong) rebuild plenty of time to happen.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(1), calls.Load())
	s.mu.Lock()
	assert.Equal(t, ch, s.ch)
	s.mu.Unlock()
}

// TestSender_StaleGeneration_DoesNotTouchNewPending guards the race where a
// confirm or close event from a superseded channel generation would release
// (or drain) waiters registered on the newer channel, which reuses the same
// delivery-tag space.
func TestSender_StaleGeneration_DoesNotTouchNewPending(t *testing.T) {
	first := &fakeChannel{}
	second := &fakeChannel{}
	var calls atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}, func() bool { return false })

	// Send 1 on generation 1.
	done1 := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done1 <- s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false)
	}()
	first.waitPublish(t)

	// Reconnect drains waiter 1 and swaps in generation 2.
	require.NoError(t, s.Reconnect())
	select {
	case err := <-done1:
		assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter 1 not drained by Reconnect")
	}

	// Send 2 on generation 2; its tag is also 1.
	done2 := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done2 <- s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false)
	}()
	require.Eventually(t, func() bool { return second.publishes.Load() >= 1 }, 2*time.Second, time.Millisecond)

	// A stale confirm from generation 1 must not release waiter 2.
	first.confirm(t, 1, true)
	select {
	case <-done2:
		t.Fatal("stale confirm released the new generation's waiter")
	case <-time.After(200 * time.Millisecond):
	}

	// A stale close from generation 1 must not drain generation 2's pending.
	first.killChannel(t)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, pendingCount(s))

	// The real confirm on generation 2 releases waiter 2.
	second.confirm(t, 1, true)
	select {
	case err := <-done2:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("waiter 2 not released by its own confirm")
	}
}

func TestSender_SendWithConfirm_PublishErrorWrapsBrokerUnavailable(t *testing.T) {
	ch := &fakeChannel{publishErr: errors.New("boom")}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	err := s.SendWithConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
	assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	assert.Contains(t, err.Error(), "publish to")
}

func TestSender_SendNoConfirm_PropagatesPublishError(t *testing.T) {
	ch := &fakeChannel{publishErr: errors.New("boom")}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	err := s.SendNoConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
	assert.Error(t, err)
}

func TestSender_Reconnect_ReturnsOpenError(t *testing.T) {
	ch := &fakeChannel{}
	openErr := errors.New("open failed")
	var calls atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		if calls.Add(1) == 1 {
			return ch, nil
		}
		return nil, openErr
	}, func() bool { return false })

	err := s.Reconnect()
	assert.ErrorIs(t, err, openErr)
}

// TestSender_SendWithConfirm_Concurrent verifies the confirm dispatcher keeps
// concurrent waiters independent: each waiter is released only by its own tag.
func TestSender_SendWithConfirm_Concurrent(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return false })

	const n = 5
	errs := make([]chan error, n)
	for i := range n {
		errs[i] = make(chan error, 1)
		go func(i int) {
			errs[i] <- s.SendWithConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
		}(i)
	}
	require.Eventually(t, func() bool { return ch.publishes.Load() >= n }, 2*time.Second, time.Millisecond)
	require.Equal(t, n, pendingCount(s))

	// Ack four of the five tags out of order; exactly one waiter must remain
	// (tag 3, whichever goroutine holds it).
	for _, tag := range []uint64{1, 5, 2, 4} {
		ch.confirm(t, tag, true)
	}
	require.Eventually(t, func() bool { return pendingCount(s) == 1 }, 2*time.Second, 10*time.Millisecond)

	ch.confirm(t, 3, true)
	for i := range n {
		select {
		case err := <-errs[i]:
			assert.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d not released", i)
		}
	}
}

// TestSender_SelfHeal_RetriesUntilSuccess is the regression test for the
// single-shot self-heal: a transient rebuild failure used to leave the sender
// dead until the next connection drop. The rebuild must retry with backoff
// until it succeeds.
func TestSender_SelfHeal_RetriesUntilSuccess(t *testing.T) {
	first := &fakeChannel{}
	recovered := &fakeChannel{}
	var opens atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		switch opens.Add(1) {
		case 1:
			return first, nil
		case 2, 3:
			return nil, errors.New("transient channel open failure")
		default:
			return recovered, nil
		}
	}, func() bool { return true })
	s.rebuildDelay = 5 * time.Millisecond
	s.rebuildMaxDelay = 20 * time.Millisecond

	first.killChannel(t)

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ch == recovered
	}, 2*time.Second, 10*time.Millisecond, "sender never recovered after transient rebuild failures")
	assert.GreaterOrEqual(t, opens.Load(), int64(4), "two failed rebuilds plus the successful one")
}

// TestSender_SelfHeal_RetryStopsWhenConnectionDrops: the self-heal retry loop
// must leave recovery to the reconnect hook once the connection is gone.
func TestSender_SelfHeal_RetryStopsWhenConnectionDrops(t *testing.T) {
	first := &fakeChannel{}
	var opens atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		if opens.Add(1) == 1 {
			return first, nil
		}
		return nil, errors.New("transient channel open failure")
	}, func() bool {
		return opens.Load() < 3 // connection "drops" after two failed rebuilds
	})
	s.rebuildDelay = 5 * time.Millisecond
	s.rebuildMaxDelay = 20 * time.Millisecond

	first.killChannel(t)

	require.Eventually(t, func() bool { return opens.Load() >= 3 }, 2*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, int64(3), opens.Load(), "rebuild retries must stop when the connection drops")
}

// TestSender_Reconnect_RetriesFailedRebuild: a Reconnect whose rebuild fails
// (the reconnect hook path) must keep retrying in the background instead of
// leaving the sender dead until the next connection drop.
func TestSender_Reconnect_RetriesFailedRebuild(t *testing.T) {
	first := &fakeChannel{}
	second := &fakeChannel{}
	var opens atomic.Int64
	s := newTestSender(func() (senderChannel, error) {
		switch opens.Add(1) {
		case 1:
			return first, nil
		case 2:
			return nil, errors.New("transient channel open failure")
		default:
			return second, nil
		}
	}, func() bool { return true })
	s.rebuildDelay = 5 * time.Millisecond
	s.rebuildMaxDelay = 20 * time.Millisecond

	err := s.Reconnect()
	assert.Error(t, err, "first rebuild attempt fails")

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.ch == second
	}, 2*time.Second, 10*time.Millisecond, "background retry never rebuilt the channel")
}

// blockingChannel wedges PublishWithContext until release is closed, emulating
// a TCP write stuck on a half-open connection.
type blockingChannel struct {
	fakeChannel
	entered chan struct{}
	release chan struct{}
}

func (b *blockingChannel) PublishWithContext(context.Context, string, string, bool, bool, amqp.Publishing) error {
	close(b.entered)
	<-b.release
	return nil
}

// TestSender_SendWithConfirm_HonorsCtxBehindWedgedPublish is the regression
// test for sends hanging behind a wedged publisher: the publish slot wait is
// context-aware, so a send whose deadline expires while queued returns
// promptly instead of waiting for the wedge to clear.
func TestSender_SendWithConfirm_HonorsCtxBehindWedgedPublish(t *testing.T) {
	ch := &blockingChannel{entered: make(chan struct{}), release: make(chan struct{})}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return true })

	stuckCtx, stuckCancel := context.WithCancel(context.Background())
	stuckDone := make(chan error, 1)
	go func() {
		stuckDone <- s.SendWithConfirm(stuckCtx, []byte(`{}`), "ex", "rk", nil, false)
	}()

	select {
	case <-ch.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first publish never reached the channel")
	}

	// A second send whose deadline expires while queued behind the wedged
	// publisher must return at its own deadline — not after the wedge clears.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false)
	elapsed := time.Since(start)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 500*time.Millisecond, "queued send ignored its deadline: %v", elapsed)

	close(ch.release)
	stuckCancel()
	select {
	case err := <-stuckDone:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("wedged sender never released")
	}
}

// TestSender_SendWithConfirm_ExpiredCtxSkipsPublish: a send with an
// already-expired context must not touch the channel at all.
func TestSender_SendWithConfirm_ExpiredCtxSkipsPublish(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return true })

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	err := s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int64(0), ch.publishes.Load(), "expired send must not publish")
	assert.Equal(t, 0, pendingCount(s))
}

// TestSender_Rebuild_RegistersListenersSynchronously guards the confirm-
// registration race: the NotifyPublish/NotifyClose listeners must exist before
// a rebuilt channel is exposed. amqp091 drops confirms when no listener is
// registered, which used to leave a waiter blocked until its ctx expired.
func TestSender_Rebuild_RegistersListenersSynchronously(t *testing.T) {
	ch := &fakeChannel{}
	s := newTestSender(func() (senderChannel, error) { return ch, nil }, func() bool { return true })

	ch.mu.Lock()
	confirms, closes := ch.confirms, ch.closes
	ch.mu.Unlock()
	require.NotNil(t, confirms, "NotifyPublish must be registered before rebuild returns")
	require.NotNil(t, closes, "NotifyClose must be registered before rebuild returns")

	// A send immediately after construction must be releasable by a confirm.
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.SendWithConfirm(context.Background(), []byte(`{}`), "ex", "rk", nil, false)
	}()
	ch.waitPublish(t)
	ch.confirm(t, 1, true)

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("confirm lost: waiter was not released")
	}
}

// TestSender_Rebuild_OpenDoesNotBlockSends guards the lock split: channel
// construction runs outside s.mu, so a slow open cannot block sends. With the
// old code the send would wait on s.mu (ctx-unaware) until the open returned.
func TestSender_Rebuild_OpenDoesNotBlockSends(t *testing.T) {
	first := &fakeChannel{publishErr: errors.New("channel is closed")}
	fresh := &fakeChannel{}
	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	var opens atomic.Int64

	s := newTestSender(func() (senderChannel, error) {
		if opens.Add(1) == 1 {
			return first, nil
		}
		close(openStarted)
		<-releaseOpen
		return fresh, nil
	}, func() bool { return true })
	s.rebuildDelay = 5 * time.Millisecond
	s.rebuildMaxDelay = 10 * time.Millisecond

	first.killChannel(t)
	select {
	case <-openStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("rebuild open never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.SendWithConfirm(ctx, []byte(`{}`), "ex", "rk", nil, false) }()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	case <-time.After(2 * time.Second):
		t.Fatal("send blocked behind a channel rebuild held under s.mu")
	}
	close(releaseOpen)
}

// TestSender_Rebuild_SupersededDuringOpenDiscardsChannel: a rebuild that was
// superseded while its open was in flight must close the fresh channel instead
// of installing it over the newer generation.
func TestSender_Rebuild_SupersededDuringOpenDiscardsChannel(t *testing.T) {
	first := &fakeChannel{}
	fresh := &fakeChannel{}
	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	var opens atomic.Int64

	s := newTestSender(func() (senderChannel, error) {
		if opens.Add(1) == 1 {
			return first, nil
		}
		close(openStarted)
		<-releaseOpen
		return fresh, nil
	}, func() bool { return true })

	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		_, _ = s.rebuild(gen, true)
		close(done)
	}()
	<-openStarted

	// A newer generation takes ownership while the open is blocked.
	s.mu.Lock()
	s.gen++
	s.mu.Unlock()
	close(releaseOpen)
	<-done

	s.mu.Lock()
	assert.Same(t, first, s.ch, "superseded rebuild must not replace the channel")
	s.mu.Unlock()
	assert.True(t, fresh.closed.Load(), "superseded fresh channel must be closed")
}
