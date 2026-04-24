//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Package-level RabbitMQ connection details shared by all tests in this file.
var (
	rabbitHost string
	rabbitPort int
)

// tryStartContainer attempts to start a RabbitMQ container. Returns (host, port, cleanup, ok).
// Returns ok=false without panicking when Docker is unavailable.
func tryStartContainer(ctx context.Context) (host string, port int, cleanup func(), ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	dockerImage := os.Getenv("TEST_RABBITMQ_IMAGE")
	if dockerImage == "" {
		dockerImage = "rabbitmq:3.12-alpine"
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImage,
			ExposedPorts: []string{"5672/tcp"},
			WaitingFor:   wait.ForListeningPort("5672/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		log.Printf("Failed to start RabbitMQ container: %v", err)
		return "", 0, nil, false
	}

	h, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", 0, nil, false
	}
	p, err := container.MappedPort(ctx, "5672")
	if err != nil {
		_ = container.Terminate(ctx)
		return "", 0, nil, false
	}
	return h, p.Int(), func() { _ = container.Terminate(ctx) }, true
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Priority 1: use RABBITMQ_URL env var (e.g. in CI with a pre-existing broker)
	if url := os.Getenv("RABBITMQ_HOST"); url != "" {
		rabbitHost = url
		if portStr := os.Getenv("RABBITMQ_PORT"); portStr != "" {
			p, err := strconv.Atoi(portStr)
			if err != nil {
				log.Fatalf("invalid RABBITMQ_PORT %q: %v", portStr, err)
			}
			rabbitPort = p
		} else {
			rabbitPort = 5672
		}
		os.Exit(m.Run())
	}

	// Priority 2: spin up a container via testcontainers-go
	host, port, cleanup, ok := tryStartContainer(ctx)
	if !ok {
		// Docker is not available. Check if a local broker is already running.
		conn, err := net.DialTimeout("tcp", "localhost:5672", 2*time.Second)
		if err != nil {
			log.Println("SKIP: integration tests require Docker or a running RabbitMQ (set RABBITMQ_HOST / RABBITMQ_PORT). Skipping.")
			os.Exit(0)
		}
		_ = conn.Close()
		rabbitHost = "localhost"
		rabbitPort = 5672
		os.Exit(m.Run())
	}
	rabbitHost = host
	rabbitPort = port

	code := m.Run()
	cleanup()
	os.Exit(code)
}

// startApp starts a rabbit.Application and waits until it is ready or the test times out.
func startApp(t *testing.T, appName string) *rabbit.Application {
	t.Helper()
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort
	cfg.Password = "guest"

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- app.Start(ctx) }()

	// Wait for ready or timeout
	select {
	case <-app.Ready():
	case err := <-errCh:
		t.Fatalf("app %q failed to start: %v", appName, err)
	case <-time.After(30 * time.Second):
		t.Fatalf("app %q did not become ready in time", appName)
	}

	return app
}

// T025: A single subscriber registers a handler; publisher emits a matching event;
// the handler is called with the correct payload.
func TestEvents_SingleSubscriber_ReceivesEvent(t *testing.T) {
	type orderPayload struct {
		OrderID string `json:"orderId"`
		Amount  int    `json:"amount"`
	}

	received := make(chan orderPayload, 1)

	// Register handler BEFORE starting
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-single-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	err = app.Registry().ListenEvent("order.created", func(ctx context.Context, e async.DomainEvent[any]) error {
		raw, ok := e.Data.(json.RawMessage)
		require.True(t, ok, "Data should be json.RawMessage")
		var p orderPayload
		require.NoError(t, json.Unmarshal(raw, &p))
		received <- p
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	// Emit event
	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "order.created",
		EventID: "test-event-id-001",
		Data:    orderPayload{OrderID: "ORD-42", Amount: 99},
	})
	require.NoError(t, err)

	// Assert handler was called with correct payload
	select {
	case payload := <-received:
		assert.Equal(t, "ORD-42", payload.OrderID)
		assert.Equal(t, 99, payload.Amount)
	case <-time.After(10 * time.Second):
		t.Fatal("handler not called within timeout")
	}
}

// T026: Two independent subscribers (different AppNames) both register handlers for
// the same event type. When the event is published, both handlers should fire.
func TestEvents_TwoSubscribers_BothReceiveEvent(t *testing.T) {
	ts := time.Now().UnixNano()
	app1Name := fmt.Sprintf("test-sub-a-%d", ts)
	app2Name := fmt.Sprintf("test-sub-b-%d", ts)

	var count1, count2 atomic.Int32

	// Build app1
	cfg1 := rabbit.NewConfigWithDefaults()
	cfg1.AppName = app1Name
	cfg1.Host = rabbitHost
	cfg1.Port = rabbitPort

	a1, err := rabbit.NewApplication(cfg1)
	require.NoError(t, err)
	err = a1.Registry().ListenEvent("inventory.updated", func(ctx context.Context, e async.DomainEvent[any]) error {
		count1.Add(1)
		return nil
	})
	require.NoError(t, err)

	// Build app2
	cfg2 := rabbit.NewConfigWithDefaults()
	cfg2.AppName = app2Name
	cfg2.Host = rabbitHost
	cfg2.Port = rabbitPort

	a2, err := rabbit.NewApplication(cfg2)
	require.NoError(t, err)
	err = a2.Registry().ListenEvent("inventory.updated", func(ctx context.Context, e async.DomainEvent[any]) error {
		count2.Add(1)
		return nil
	})
	require.NoError(t, err)

	// Start both apps
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for _, app := range []*rabbit.Application{a1, a2} {
		a := app
		go func() { _ = a.Start(ctx) }()
	}

	// Wait for both to be ready
	for _, app := range []*rabbit.Application{a1, a2} {
		select {
		case <-app.Ready():
		case <-time.After(30 * time.Second):
			t.Fatal("app not ready")
		}
	}

	// Emit event from app1
	err = a1.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "inventory.updated",
		EventID: "inv-evt-001",
		Data:    map[string]any{"sku": "ITEM-1"},
	})
	require.NoError(t, err)

	// Both handlers should fire
	assert.Eventually(t, func() bool { return count1.Load() >= 1 }, 10*time.Second, 100*time.Millisecond,
		"app1 handler not called")
	assert.Eventually(t, func() bool { return count2.Load() >= 1 }, 10*time.Second, 100*time.Millisecond,
		"app2 handler not called")
}

// T063: A panicking event handler must not crash the consumer goroutine.
// After the panic is recovered, the consumer must continue to process subsequent events.
func TestEvents_PanicInHandler_ConsumerContinues(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-panic-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	var panicCount, successCount atomic.Int32

	// First handler panics on the first call, then succeeds on subsequent ones.
	err = app.Registry().ListenEvent("panic.test", func(ctx context.Context, e async.DomainEvent[any]) error {
		if panicCount.Add(1) == 1 {
			panic("intentional panic for test")
		}
		successCount.Add(1)
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errCh := make(chan error, 1)
	go func() { errCh <- app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	// Send first event — handler panics, consumer must recover and keep running.
	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "panic.test",
		EventID: "panic-evt-001",
		Data:    map[string]any{"seq": 1},
	})
	require.NoError(t, err)

	// Wait a moment then send a second event — handler should succeed this time.
	assert.Eventually(t, func() bool { return panicCount.Load() >= 1 }, 10*time.Second, 50*time.Millisecond,
		"panicking handler was not called")

	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "panic.test",
		EventID: "panic-evt-002",
		Data:    map[string]any{"seq": 2},
	})
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return successCount.Load() >= 1 }, 10*time.Second, 100*time.Millisecond,
		"consumer goroutine did not recover and continue processing after panic")
}

// T027: Publishing an event for which no handler is registered should not cause an
// error on the publisher side and should not panic or crash.
func TestEvents_UnregisteredEvent_SilentlyIgnored(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-noreg-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	// No handler registered for "order.updated"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	// Should not return an error even though nobody is listening
	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "order.updated",
		EventID: "evt-no-handler",
		Data:    map[string]any{"id": "X"},
	})
	assert.NoError(t, err, "publishing with no subscribers must not error")
}

// TestEvents_HandlerReturnsError_MessageRedelivered verifies that when an event
// handler returns an error the consumer nacks the message and it is redelivered,
// and that the consumer continues processing subsequent deliveries successfully.
func TestEvents_HandlerReturnsError_MessageRedelivered(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-evterr-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	var callCount, successCount atomic.Int32

	err = app.Registry().ListenEvent("err.test", func(ctx context.Context, e async.DomainEvent[any]) error {
		n := callCount.Add(1)
		if n == 1 {
			return fmt.Errorf("transient error on first delivery")
		}
		successCount.Add(1)
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "err.test",
		EventID: "err-evt-001",
		Data:    map[string]any{"seq": 1},
	})
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return successCount.Load() >= 1 }, 15*time.Second, 100*time.Millisecond,
		"event handler error: consumer did not redeliver and succeed")
}

// TestEvents_WildcardSubscription_ReceivesMatchingEvents verifies that a
// handler registered with a RabbitMQ topic pattern (e.g. "order.*") fires
// for matching concrete event names. Uses the broker-side topic binding
// directly: registering "order.*" creates an AMQP binding with that key
// against the durable domainEvents exchange.
func TestEvents_WildcardSubscription_ReceivesMatchingEvents(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-wildcard-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	var matched, unmatched atomic.Int32
	matchedNames := make(chan string, 4)

	err = app.Registry().ListenEvent("order.*", func(ctx context.Context, e async.DomainEvent[any]) error {
		matched.Add(1)
		matchedNames <- e.Name
		return nil
	})
	require.NoError(t, err)

	// A second pattern that should NOT match "order.*" deliveries.
	err = app.Registry().ListenEvent("invoice.#", func(ctx context.Context, e async.DomainEvent[any]) error {
		unmatched.Add(1)
		return nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	// Emit two events that the topic binding "order.*" should deliver.
	for _, name := range []string{"order.created", "order.cancelled"} {
		err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
			Name:    name,
			EventID: name + "-id",
			Data:    map[string]any{"n": name},
		})
		require.NoError(t, err)
	}

	// Emit one that "order.*" must NOT match (multi-segment).
	err = app.EventBus().Emit(ctx, async.DomainEvent[any]{
		Name:    "order.v2.created",
		EventID: "order-v2-id",
		Data:    map[string]any{},
	})
	require.NoError(t, err)

	assert.Eventually(t, func() bool { return matched.Load() >= 2 }, 10*time.Second, 100*time.Millisecond,
		"wildcard handler did not receive both matching events")

	// Drain to confirm names came through correctly.
	close(matchedNames)
	seen := map[string]bool{}
	for n := range matchedNames {
		seen[n] = true
	}
	assert.True(t, seen["order.created"], "order.created should reach order.* handler")
	assert.True(t, seen["order.cancelled"], "order.cancelled should reach order.* handler")
	assert.False(t, seen["order.v2.created"], "order.* must not match multi-segment names")

	assert.Equal(t, int32(0), unmatched.Load(), "invoice.# handler must not fire for order.* events")
}
