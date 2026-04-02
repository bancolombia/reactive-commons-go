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

// T031: Two apps — sender sends a command to receiver's AppName; receiver handler fires
// with the correct payload.
func TestCommands_DeliveredToTarget(t *testing.T) {
	type invoicePayload struct {
		InvoiceID string `json:"invoiceId"`
		Amount    int    `json:"amount"`
	}

	received := make(chan invoicePayload, 1)

	// --- receiver ---
	rcvName := fmt.Sprintf("receiver-%d", time.Now().UnixNano())
	rcvCfg := rabbit.NewConfigWithDefaults()
	rcvCfg.AppName = rcvName
	rcvCfg.Host = rabbitHost
	rcvCfg.Port = rabbitPort

	rcv, err := rabbit.NewApplication(rcvCfg)
	require.NoError(t, err)

	err = rcv.Registry().ListenCommand("create-invoice", func(ctx context.Context, cmd async.Command[any]) error {
		raw, ok := cmd.Data.(json.RawMessage)
		require.True(t, ok)
		var p invoicePayload
		require.NoError(t, json.Unmarshal(raw, &p))
		received <- p
		return nil
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

	// --- sender ---
	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("sender-%d", time.Now().UnixNano())
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

	err = snd.Gateway().SendCommand(context.Background(), async.Command[any]{
		Name:      "create-invoice",
		CommandID: "cmd-001",
		Data:      invoicePayload{InvoiceID: "INV-99", Amount: 500},
	}, rcvName)
	require.NoError(t, err)

	select {
	case payload := <-received:
		assert.Equal(t, "INV-99", payload.InvoiceID)
		assert.Equal(t, 500, payload.Amount)
	case <-time.After(10 * time.Second):
		t.Fatal("command handler not called within timeout")
	}
}

// T032: Send a command to a service that has no handler for it; must not crash,
// must not block, must not error on the sender side.
func TestCommands_NoHandler_SilentlyDiscarded(t *testing.T) {
	rcvName := fmt.Sprintf("cmd-noreg-%d", time.Now().UnixNano())
	rcvCfg := rabbit.NewConfigWithDefaults()
	rcvCfg.AppName = rcvName
	rcvCfg.Host = rabbitHost
	rcvCfg.Port = rabbitPort

	rcv, err := rabbit.NewApplication(rcvCfg)
	require.NoError(t, err)
	// No handler registered

	rcvCtx, rcvCancel := context.WithCancel(context.Background())
	t.Cleanup(rcvCancel)
	go func() { _ = rcv.Start(rcvCtx) }()

	select {
	case <-rcv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("receiver not ready")
	}

	// sender
	sndCfg := rabbit.NewConfigWithDefaults()
	sndCfg.AppName = fmt.Sprintf("cmd-noreg-snd-%d", time.Now().UnixNano())
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = snd.Gateway().SendCommand(ctx, async.Command[any]{
		Name:      "unknown-cmd",
		CommandID: "cmd-x",
		Data:      map[string]any{"key": "val"},
	}, rcvName)
	assert.NoError(t, err, "SendCommand must not error even when receiver has no handler")
}

// TestCommands_PanicInHandler_ConsumerContinues verifies the consumer recovers after
// a handler panics and continues processing subsequent commands.
func TestCommands_PanicInHandler_ConsumerContinues(t *testing.T) {
	rcvName := fmt.Sprintf("cmd-panic-%d", time.Now().UnixNano())
	rcvCfg := rabbit.NewConfigWithDefaults()
	rcvCfg.AppName = rcvName
	rcvCfg.Host = rabbitHost
	rcvCfg.Port = rabbitPort

	rcv, err := rabbit.NewApplication(rcvCfg)
	require.NoError(t, err)

	var panicCount, successCount int32
	err = rcv.Registry().ListenCommand("cmd.panic", func(ctx context.Context, cmd async.Command[any]) error {
		val := atomic.AddInt32(&panicCount, 1)
		if val == 1 {
			panic("intentional command panic")
		}
		atomic.AddInt32(&successCount, 1)
		return nil
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
	sndCfg.AppName = fmt.Sprintf("cmd-panic-snd-%d", time.Now().UnixNano())
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

	err = snd.Gateway().SendCommand(context.Background(), async.Command[any]{
		Name:      "cmd.panic",
		CommandID: "panic-cmd-1",
		Data:      nil,
	}, rcvName)
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return atomic.LoadInt32(&panicCount) >= 1 }, 10*time.Second, 50*time.Millisecond)

	err = snd.Gateway().SendCommand(context.Background(), async.Command[any]{
		Name:      "cmd.panic",
		CommandID: "panic-cmd-2",
		Data:      nil,
	}, rcvName)
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return atomic.LoadInt32(&successCount) >= 1 }, 15*time.Second, 100*time.Millisecond,
		"command consumer did not recover after panic")
}

// TestCommands_HandlerReturnsError_MessageRedelivered verifies that when a command
// handler returns an error the message is nacked and redelivered, and that the
// consumer continues successfully.
func TestCommands_HandlerReturnsError_MessageRedelivered(t *testing.T) {
	rcvName := fmt.Sprintf("cmd-err-%d", time.Now().UnixNano())
	rcvCfg := rabbit.NewConfigWithDefaults()
	rcvCfg.AppName = rcvName
	rcvCfg.Host = rabbitHost
	rcvCfg.Port = rabbitPort

	rcv, err := rabbit.NewApplication(rcvCfg)
	require.NoError(t, err)

	var callCount, successCount int32
	err = rcv.Registry().ListenCommand("cmd.err", func(ctx context.Context, cmd async.Command[any]) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			return fmt.Errorf("transient command error")
		}
		atomic.AddInt32(&successCount, 1)
		return nil
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
	sndCfg.AppName = fmt.Sprintf("cmd-err-snd-%d", time.Now().UnixNano())
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

	err = snd.Gateway().SendCommand(context.Background(), async.Command[any]{
		Name:      "cmd.err",
		CommandID: "err-cmd-1",
		Data:      nil,
	}, rcvName)
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return atomic.LoadInt32(&successCount) >= 1 }, 15*time.Second, 100*time.Millisecond,
		"command handler error: consumer did not redeliver and succeed")
}
