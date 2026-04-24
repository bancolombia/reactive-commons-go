//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T037: Successful query/reply round-trip — two apps, caller sends "get-product"
// query, handler returns a Product struct, caller deserializes it correctly.
func TestQueries_SuccessfulRoundTrip(t *testing.T) {
	type product struct {
		ID    string `json:"id"`
		Name  string `json:"price"`
		Price int    `json:"amount"`
	}

	ts := time.Now().UnixNano()
	srvName := fmt.Sprintf("product-svc-%d", ts)
	callerName := fmt.Sprintf("caller-%d", ts)

	// --- server ---
	srvCfg := rabbit.NewConfigWithDefaults()
	srvCfg.AppName = srvName
	srvCfg.Host = rabbitHost
	srvCfg.Port = rabbitPort

	srv, err := rabbit.NewApplication(srvCfg)
	require.NoError(t, err)

	err = srv.Registry().ServeQuery("get-product", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		raw, ok := q.QueryData.(json.RawMessage)
		require.True(t, ok)
		var req struct {
			ProductID string `json:"productId"`
		}
		require.NoError(t, json.Unmarshal(raw, &req))
		return product{ID: req.ProductID, Name: "Widget", Price: 42}, nil
	})
	require.NoError(t, err)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)
	go func() { _ = srv.Start(srvCtx) }()
	select {
	case <-srv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("server not ready")
	}

	// --- caller ---
	callerCfg := rabbit.NewConfigWithDefaults()
	callerCfg.AppName = callerName
	callerCfg.Host = rabbitHost
	callerCfg.Port = rabbitPort

	caller, err := rabbit.NewApplication(callerCfg)
	require.NoError(t, err)

	callerCtx, callerCancel := context.WithCancel(context.Background())
	t.Cleanup(callerCancel)
	go func() { _ = caller.Start(callerCtx) }()
	select {
	case <-caller.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("caller not ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
		Resource:  "get-product",
		QueryData: map[string]any{"productId": "P-001"},
	}, srvName)
	require.NoError(t, err)

	var result product
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.Equal(t, "P-001", result.ID)
	assert.Equal(t, "Widget", result.Name)
	assert.Equal(t, 42, result.Price)
}

// T038: Query to a service with no handler must return ErrQueryTimeout (ctx deadline).
func TestQueries_NoHandler_TimesOut(t *testing.T) {
	ts := time.Now().UnixNano()
	srvName := fmt.Sprintf("no-query-svc-%d", ts)
	callerName := fmt.Sprintf("timeout-caller-%d", ts)

	// server with NO query handler
	srvCfg := rabbit.NewConfigWithDefaults()
	srvCfg.AppName = srvName
	srvCfg.Host = rabbitHost
	srvCfg.Port = rabbitPort

	srv, err := rabbit.NewApplication(srvCfg)
	require.NoError(t, err)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)
	go func() { _ = srv.Start(srvCtx) }()
	select {
	case <-srv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("server not ready")
	}

	callerCfg := rabbit.NewConfigWithDefaults()
	callerCfg.AppName = callerName
	callerCfg.Host = rabbitHost
	callerCfg.Port = rabbitPort

	caller, err := rabbit.NewApplication(callerCfg)
	require.NoError(t, err)

	callerCtx, callerCancel := context.WithCancel(context.Background())
	t.Cleanup(callerCancel)
	go func() { _ = caller.Start(callerCtx) }()
	select {
	case <-caller.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("caller not ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err = caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
		Resource:  "get-something",
		QueryData: map[string]any{},
	}, srvName)

	assert.ErrorIs(t, err, async.ErrQueryTimeout, "expected ErrQueryTimeout, got: %v", err)
}

// T039: Multiple concurrent queries are correctly correlated — each response
// must match its own request payload, not a different query's reply.
func TestQueries_ConcurrentQueriesCorrelated(t *testing.T) {
	const N = 10

	ts := time.Now().UnixNano()
	srvName := fmt.Sprintf("echo-svc-%d", ts)
	callerName := fmt.Sprintf("conc-caller-%d", ts)

	// server echoes back the queryData payload
	srvCfg := rabbit.NewConfigWithDefaults()
	srvCfg.AppName = srvName
	srvCfg.Host = rabbitHost
	srvCfg.Port = rabbitPort

	srv, err := rabbit.NewApplication(srvCfg)
	require.NoError(t, err)

	err = srv.Registry().ServeQuery("echo", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		// Echo queryData back unchanged
		return q.QueryData, nil
	})
	require.NoError(t, err)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)
	go func() { _ = srv.Start(srvCtx) }()
	select {
	case <-srv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("server not ready")
	}

	callerCfg := rabbit.NewConfigWithDefaults()
	callerCfg.AppName = callerName
	callerCfg.Host = rabbitHost
	callerCfg.Port = rabbitPort

	caller, err := rabbit.NewApplication(callerCfg)
	require.NoError(t, err)

	callerCtx, callerCancel := context.WithCancel(context.Background())
	t.Cleanup(callerCancel)
	go func() { _ = caller.Start(callerCtx) }()
	select {
	case <-caller.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("caller not ready")
	}

	// Fire N concurrent queries
	type result struct {
		idx int
		raw json.RawMessage
		err error
	}
	results := make([]result, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			raw, err := caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
				Resource:  "echo",
				QueryData: map[string]any{"seq": idx},
			}, srvName)
			results[idx] = result{idx: idx, raw: raw, err: err}
		}(i)
	}
	wg.Wait()

	for _, r := range results {
		require.NoError(t, r.err, "query %d failed", r.idx)
		// The echo handler returns the queryData as-is (json.RawMessage wrapping map)
		// After double round-trip through JSON it may be a nested raw message.
		// Just assert the seq number is present in the body.
		assert.Contains(t, string(r.raw), fmt.Sprintf("%d", r.idx),
			"response for query %d should contain seq=%d", r.idx, r.idx)
	}
}

// TestQueries_HandlerError_ReturnedToCaller: when the query handler returns an error,
// the caller should receive a non-nil error (covering gateway.replyError path).
func TestQueries_HandlerError_ReturnedToCaller(t *testing.T) {
	ts := time.Now().UnixNano()
	srvName := fmt.Sprintf("err-svc-%d", ts)
	callerName := fmt.Sprintf("err-caller-%d", ts)

	srvCfg := rabbit.NewConfigWithDefaults()
	srvCfg.AppName = srvName
	srvCfg.Host = rabbitHost
	srvCfg.Port = rabbitPort

	srv, err := rabbit.NewApplication(srvCfg)
	require.NoError(t, err)

	require.NoError(t, srv.Registry().ServeQuery("failing-query", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, fmt.Errorf("intentional handler error")
	}))

	srvCtx, srvCancel := context.WithCancel(context.Background())
	t.Cleanup(srvCancel)
	go func() { _ = srv.Start(srvCtx) }()
	select {
	case <-srv.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("server not ready")
	}

	callerCfg := rabbit.NewConfigWithDefaults()
	callerCfg.AppName = callerName
	callerCfg.Host = rabbitHost
	callerCfg.Port = rabbitPort

	caller, err := rabbit.NewApplication(callerCfg)
	require.NoError(t, err)

	callerCtx, callerCancel := context.WithCancel(context.Background())
	t.Cleanup(callerCancel)
	go func() { _ = caller.Start(callerCtx) }()
	select {
	case <-caller.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("caller not ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
		Resource:  "failing-query",
		QueryData: map[string]any{"x": 1},
	}, srvName)
	assert.Error(t, err, "caller should receive an error when handler fails")
}

// TestEvents_WithDLQRetry_TopologyDeclared: when WithDLQRetry is true, the app
// should start successfully (exercises Topology.DeclareEventsDLQRetry and
// DeclareDirectDLQRetry via wire-up in rabbit/builder).
func TestEvents_WithDLQRetry_TopologyDeclared(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("test-dlq-%d", time.Now().UnixNano())
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort
	cfg.WithDLQRetry = true

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	require.NoError(t, app.Registry().ListenEvent("dlq.test", func(ctx context.Context, e async.DomainEvent[any]) error {
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
		// successfully started with DLQ topology
	case <-time.After(30 * time.Second):
		t.Fatal("app with DLQ config did not become ready in time")
	}
}
