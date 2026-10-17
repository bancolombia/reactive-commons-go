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
// self-heal decision comes from connOpen.
func newTestSender(open func() (senderChannel, error), connOpen func() bool) *Sender {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Sender{
		cfg:          Config{AppName: "test"},
		log:          log,
		pending:      make(map[uint64]chan error),
		rebuildDelay: 0,
		openCh:       open,
		connOpen:     connOpen,
	}
	if err := s.rebuildLocked(); err != nil {
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
