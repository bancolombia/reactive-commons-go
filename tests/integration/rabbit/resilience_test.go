//go:build integration

package rabbit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminChannel opens a raw AMQP channel for manipulating broker topology
// (deleting and re-declaring exchanges) independently of the library under test.
func adminChannel(t *testing.T) *amqp.Channel {
	t.Helper()
	conn, err := amqp.Dial(fmt.Sprintf("amqp://guest:guest@%s:%d/", rabbitHost, rabbitPort))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

// hasConnectionNamed reports whether the management API currently lists a
// connection whose connection_name client property matches name.
func hasConnectionNamed(t *testing.T, name string) bool {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s:%d/api/connections", rabbitHost, rabbitMgmtPort), nil)
	require.NoError(t, err)
	req.SetBasicAuth("guest", "guest")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var conns []struct {
		ClientProperties struct {
			ConnectionName string `json:"connection_name"`
		} `json:"client_properties"`
	}
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &conns))
	for _, c := range conns {
		if c.ClientProperties.ConnectionName == name {
			return true
		}
	}
	return false
}

// TestResilience_SendFailsFastAndSelfHealsAfterChannelKill: a broker-killed
// publisher channel (404 from a deleted exchange) must fail the in-flight
// Emit promptly — not at the caller's context deadline — and the sender must
// rebuild its channel so publishing recovers without an app restart.
func TestResilience_SendFailsFastAndSelfHealsAfterChannelKill(t *testing.T) {
	ts := time.Now().UnixNano()
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("resil-failfast-%d", ts)
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()
	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	admin := adminChannel(t)
	require.NoError(t, admin.ExchangeDelete("domainEvents", false, false))

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer sendCancel()
	start := time.Now()
	err = app.EventBus().Emit(sendCtx, async.DomainEvent[any]{Name: "resil.failfast.event", EventID: "1"})
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, async.ErrBrokerUnavailable)
	assert.Less(t, elapsed, 4*time.Second, "Emit must fail fast on a broker-killed channel, got %v", elapsed)

	// Restore the exchange; the sender self-heals its channel (~1s delay)
	// without a connection drop or app restart.
	require.NoError(t, admin.ExchangeDeclare("domainEvents", "topic", true, false, false, false, nil))

	require.Eventually(t, func() bool {
		return app.EventBus().Emit(context.Background(), async.DomainEvent[any]{Name: "resil.failfast.event", EventID: "2"}) == nil
	}, 20*time.Second, 250*time.Millisecond, "Emit did not recover after channel self-heal")
}

// TestResilience_ConsumersRecoverAfterTopologyConflict: a reconnect whose
// topology re-declaration fails (exchange type conflict) must not leave the
// consumers dead forever — the library retries with backoff and recovers once
// the broker-side conflict is resolved, while Emit keeps succeeding (messages
// queue broker-side, not lost with a fake success).
func TestResilience_ConsumersRecoverAfterTopologyConflict(t *testing.T) {
	if rabbitMgmtPort == 0 {
		t.Skip("management plugin not available; set RABBITMQ_MGMT_PORT or use a -management image")
	}

	ts := time.Now().UnixNano()
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("resil-topo-%d", ts)
	cfg.ConnectionName = cfg.AppName
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	received := make(chan string, 8)
	require.NoError(t, app.Registry().ListenEvent("resil.topo.event", func(ctx context.Context, e async.DomainEvent[any]) error {
		received <- e.EventID
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = app.Start(ctx) }()
	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{Name: "resil.topo.event", EventID: "before"}))
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("pre-reconnect event not received")
	}

	// Drop the connection from the broker side and, while the app is in its
	// reconnect backoff, replace the events exchange with a fanout so the
	// post-reconnect topology declaration fails with PRECONDITION_FAILED.
	require.NoError(t, forceCloseConnection(t, cfg.AppName))
	admin := adminChannel(t)
	require.NoError(t, admin.ExchangeDelete("domainEvents", false, false))
	require.NoError(t, admin.ExchangeDeclare("domainEvents", "fanout", true, false, false, false, nil))
	t.Log("replaced domainEvents with fanout during reconnect window")

	// Let the reconnect land and the consumer-restore retry loop fail at least
	// once (first retry attempt happens ~1s after the reconnect succeeds).
	time.Sleep(4 * time.Second)

	// Restore a compatible exchange. The retry loop must pick it up and bring
	// the consumers back — no crash, no restart.
	require.NoError(t, admin.ExchangeDelete("domainEvents", false, false))
	require.NoError(t, admin.ExchangeDeclare("domainEvents", "topic", true, false, false, false, nil))

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		emitErr := app.EventBus().Emit(context.Background(), async.DomainEvent[any]{Name: "resil.topo.event", EventID: "after"})
		require.NoError(t, emitErr, "Emit must keep working while consumers are down")
		select {
		case id := <-received:
			assert.Equal(t, "after", id)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatal("consumers never recovered after the topology conflict was resolved")
}

// TestResilience_CloseStopsReconnectLoop: shutting the app down while the
// reconnect loop is inside its backoff must abort the reconnect — no
// connection may be re-established after Close.
func TestResilience_CloseStopsReconnectLoop(t *testing.T) {
	if rabbitMgmtPort == 0 {
		t.Skip("management plugin not available; set RABBITMQ_MGMT_PORT or use a -management image")
	}

	ts := time.Now().UnixNano()
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("resil-close-%d", ts)
	cfg.ConnectionName = cfg.AppName
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Start(ctx) }()
	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app not ready")
	}

	// Force-close the broker connection, then shut the app down while the
	// reconnect loop is inside its first 1s backoff.
	require.NoError(t, forceCloseConnection(t, cfg.AppName))
	time.Sleep(300 * time.Millisecond)
	cancel()

	// Poll the management API: the reconnect loop must have aborted on Close.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if hasConnectionNamed(t, cfg.AppName) {
			t.Fatal("reconnect loop re-established a connection after shutdown")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
