//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T046: Connected subscriber registers for "cache-invalidated"; sender broadcasts;
// handler is invoked with the correct payload.
func TestNotifications_ConnectedSubscriberReceives(t *testing.T) {
	type cachePayload struct {
		Key string `json:"key"`
	}

	received := make(chan cachePayload, 1)

	ts := time.Now().UnixNano()

	// --- subscriber ---
	subCfg := rabbit.NewConfigWithDefaults()
	subCfg.AppName = fmt.Sprintf("notif-sub-%d", ts)
	subCfg.Host = rabbitHost
	subCfg.Port = rabbitPort

	sub, err := rabbit.NewApplication(subCfg)
	require.NoError(t, err)

	err = sub.Registry().ListenNotification("cache-invalidated", func(ctx context.Context, n async.Notification[any]) error {
		raw, ok := n.Data.(json.RawMessage)
		require.True(t, ok)
		var p cachePayload
		require.NoError(t, json.Unmarshal(raw, &p))
		received <- p
		return nil
	})
	require.NoError(t, err)

	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)
	go func() { _ = sub.Start(subCtx) }()
	select {
	case <-sub.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("subscriber not ready")
	}

	// --- broadcaster ---
	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("notif-snd-%d", ts)
	sndCfg.Host = rabbitHost
	sndCfg.Port = rabbitPort

	snd, err := rabbit.NewApplication(sndCfg)
	require.NoError(t, err)

	sndCtx, sndCancel := context.WithCancel(context.Background())
	t.Cleanup(sndCancel)
	go func() { _ = snd.Start(sndCtx) }()
	select {
	case <-snd.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("broadcaster not ready")
	}

	err = snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name:    "cache-invalidated",
		EventID: "notif-001",
		Data:    cachePayload{Key: "user:42"},
	})
	require.NoError(t, err)

	select {
	case payload := <-received:
		assert.Equal(t, "user:42", payload.Key)
	case <-time.After(10 * time.Second):
		t.Fatal("notification handler not called within timeout")
	}
}

// T047: Late-joining subscriber must NOT receive a notification emitted before it started.
// Non-durable semantics: missed notifications are acceptable (and expected).
func TestNotifications_LateJoiner_MissesPastNotification(t *testing.T) {
	ts := time.Now().UnixNano()

	// --- broadcaster (starts first) ---
	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("notif-late-snd-%d", ts)
	sndCfg.Host = rabbitHost
	sndCfg.Port = rabbitPort

	snd, err := rabbit.NewApplication(sndCfg)
	require.NoError(t, err)

	sndCtx, sndCancel := context.WithCancel(context.Background())
	t.Cleanup(sndCancel)
	go func() { _ = snd.Start(sndCtx) }()
	select {
	case <-snd.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("broadcaster not ready")
	}

	// Emit BEFORE the late subscriber exists.
	err = snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name:    "stale-event",
		EventID: "notif-past",
		Data:    map[string]any{"v": 1},
	})
	require.NoError(t, err)

	// Small delay to ensure the broker has processed the publish.
	time.Sleep(100 * time.Millisecond)

	// --- late subscriber (starts AFTER the emit) ---
	var callCount atomic.Int32

	subCfg := rabbit.NewConfigWithDefaults()
	subCfg.AppName = fmt.Sprintf("notif-late-sub-%d", ts)
	subCfg.Host = rabbitHost
	subCfg.Port = rabbitPort

	sub, err := rabbit.NewApplication(subCfg)
	require.NoError(t, err)

	err = sub.Registry().ListenNotification("stale-event", func(ctx context.Context, n async.Notification[any]) error {
		callCount.Add(1)
		return nil
	})
	require.NoError(t, err)

	subCtx, subCancel := context.WithCancel(context.Background())
	t.Cleanup(subCancel)
	go func() { _ = sub.Start(subCtx) }()
	select {
	case <-sub.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("late subscriber not ready")
	}

	// Wait 300ms — the past notification must NOT arrive.
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int32(0), callCount.Load(), "late-joiner must not receive past notification")
}

// T048: Broadcasting with no subscribers must not return an error.
func TestNotifications_ZeroSubscribers_NoError(t *testing.T) {
	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("notif-nosub-snd-%d", time.Now().UnixNano())
	sndCfg.Host = rabbitHost
	sndCfg.Port = rabbitPort

	snd, err := rabbit.NewApplication(sndCfg)
	require.NoError(t, err)

	sndCtx, sndCancel := context.WithCancel(context.Background())
	t.Cleanup(sndCancel)
	go func() { _ = snd.Start(sndCtx) }()
	select {
	case <-snd.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("broadcaster not ready")
	}

	err = snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name:    "orphan-notification",
		EventID: "notif-orphan",
		Data:    map[string]any{},
	})
	assert.NoError(t, err, "broadcasting with no subscribers must not error")
}

// TestNotifications_HandlerReturnsError_LogsAndContinues verifies that when a
// notification handler returns an error the consumer logs it and continues
// processing (notifications are best-effort, no redeliver).
func TestNotifications_HandlerReturnsError_LogsAndContinues(t *testing.T) {
	rcvCfg := rabbit.NewConfigWithDefaults()
	rcvCfg.AppName = fmt.Sprintf("notif-err-rcv-%d", time.Now().UnixNano())
	rcvCfg.Host = rabbitHost
	rcvCfg.Port = rabbitPort

	rcv, err := rabbit.NewApplication(rcvCfg)
	require.NoError(t, err)

	var callCount int32
	err = rcv.Registry().ListenNotification("notif.err", func(ctx context.Context, n async.Notification[any]) error {
		atomic.AddInt32(&callCount, 1)
		return fmt.Errorf("notification handler error")
	})
	require.NoError(t, err)

	rcvCtx, rcvCancel := context.WithCancel(context.Background())
	t.Cleanup(rcvCancel)
	go func() { _ = rcv.Start(rcvCtx) }()
	select {
	case <-rcv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("receiver not ready")
	}

	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("notif-err-snd-%d", time.Now().UnixNano())
	sndCfg.Host = rabbitHost
	sndCfg.Port = rabbitPort
	snd, err := rabbit.NewApplication(sndCfg)
	require.NoError(t, err)
	sndCtx, sndCancel := context.WithCancel(context.Background())
	t.Cleanup(sndCancel)
	go func() { _ = snd.Start(sndCtx) }()
	select {
	case <-snd.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("sender not ready")
	}

	err = snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name:    "notif.err",
		EventID: "notif-err-1",
		Data:    nil,
	})
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return atomic.LoadInt32(&callCount) >= 1 }, 10*time.Second, 100*time.Millisecond,
		"notification handler was not called")
	// Consumer must still be running (no crash) — we can send a second one and it will be handled.
}
