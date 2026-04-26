//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReconnect_NotificationConsumerResumesAfterDrop verifies that when the
// receiver's broker connection is force-closed mid-run, the auto-delete
// notification queue is recreated and its listener is restarted, so a
// notification published after the reconnect still reaches the handler.
//
// This is the regression test for the bug where the OnReconnect hook only
// re-declared exchanges, leaving the per-instance temp queues
// (`<app>.notification.<rand>` and `<app>.replies.<rand>`) gone forever and
// every consumer goroutine permanently exited.
func TestReconnect_NotificationConsumerResumesAfterDrop(t *testing.T) {
	if rabbitMgmtPort == 0 {
		t.Skip("management plugin not available; set RABBITMQ_MGMT_PORT or use a -management image")
	}

	ts := time.Now().UnixNano()
	connName := fmt.Sprintf("notif-rcv-%d", ts)

	type payload struct {
		Key string `json:"key"`
	}

	var (
		preReconnect  atomic.Bool
		postReconnect = make(chan payload, 1)
	)

	// --- subscriber (will have its connection forcibly closed) ---
	subCfg := rabbit.NewConfigWithDefaults()
	subCfg.AppName = connName
	subCfg.ConnectionName = connName
	subCfg.Host = rabbitHost
	subCfg.Port = rabbitPort

	sub, err := rabbit.NewApplication(subCfg)
	require.NoError(t, err)

	err = sub.Registry().ListenNotification("cache.invalidated.reconnect", func(ctx context.Context, n async.Notification[any]) error {
		raw, ok := n.Data.(json.RawMessage)
		require.True(t, ok)
		var p payload
		require.NoError(t, json.Unmarshal(raw, &p))
		switch p.Key {
		case "before":
			preReconnect.Store(true)
		case "after":
			postReconnect <- p
		}
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

	// Sanity check: the handler runs on the original connection.
	require.NoError(t, snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name: "cache.invalidated.reconnect",
		Data: payload{Key: "before"},
	}))
	require.Eventually(t, preReconnect.Load, 10*time.Second, 50*time.Millisecond, "pre-reconnect notification not received")

	// Force-close the subscriber's connection from the broker side. The Go
	// process must reconnect, redeclare the temp notification queue under the
	// same generated name and restart the consumer goroutine.
	require.NoError(t, forceCloseConnection(t, connName), "could not force-close broker connection")

	// Give the auto-reconnect loop time to re-establish: backoff starts at
	// 1s and topology+listeners need to come back up.
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		// Repeatedly emit until the subscriber processes one — this avoids
		// a race where we publish before the new consumer is registered.
		emitErr := snd.EventBus().EmitNotification(context.Background(), async.Notification[any]{
			Name: "cache.invalidated.reconnect",
			Data: payload{Key: "after"},
		})
		if emitErr != nil {
			lastErr = emitErr
		}
		select {
		case p := <-postReconnect:
			assert.Equal(t, "after", p.Key)
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
	if lastErr != nil {
		t.Fatalf("post-reconnect notification not received within deadline; last emit error: %v", lastErr)
	}
	t.Fatal("post-reconnect notification not received within deadline")
}

// forceCloseConnection asks the RabbitMQ management plugin to delete (force
// close) the connection whose `connection_name` client property matches
// connName. The default broker creds are guest/guest.
func forceCloseConnection(t *testing.T, connName string) error {
	t.Helper()
	base := fmt.Sprintf("http://%s:%d", rabbitHost, rabbitMgmtPort)
	client := &http.Client{Timeout: 5 * time.Second}

	// Wait until the connection appears in /api/connections — the AMQP
	// handshake is async with respect to the management cache.
	deadline := time.Now().Add(10 * time.Second)
	var connRecordName string
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, base+"/api/connections", nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth("guest", "guest")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("management API GET /api/connections returned %d: %s", resp.StatusCode, body)
		}

		var conns []struct {
			Name             string `json:"name"`
			ClientProperties struct {
				ConnectionName string `json:"connection_name"`
			} `json:"client_properties"`
		}
		if err := json.Unmarshal(body, &conns); err != nil {
			return fmt.Errorf("decode connections response: %w", err)
		}
		for _, c := range conns {
			if c.ClientProperties.ConnectionName == connName {
				connRecordName = c.Name
				break
			}
		}
		if connRecordName != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if connRecordName == "" {
		return fmt.Errorf("connection with connection_name=%q not found in management API", connName)
	}

	delURL := base + "/api/connections/" + pathEscape(connRecordName)
	req, err := http.NewRequest(http.MethodDelete, delURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth("guest", "guest")
	req.Header.Set("X-Reason", "integration-test-force-close")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("management API DELETE returned %d: %s", resp.StatusCode, body)
	}
	return nil
}

// TestReconnect_QueryReplyResumesAfterDrop verifies that after a forced
// connection drop the receiver's publisher channel (used by gw.Reply) is
// rebuilt, so a subsequent query is answered correctly. Before the Sender.
// Reconnect fix the reply would fail with
// "Exception (504) Reason: \"channel/connection is not open\"".
func TestReconnect_QueryReplyResumesAfterDrop(t *testing.T) {
	if rabbitMgmtPort == 0 {
		t.Skip("management plugin not available; set RABBITMQ_MGMT_PORT or use a -management image")
	}

	ts := time.Now().UnixNano()
	srvConnName := fmt.Sprintf("query-srv-%d", ts)
	callerName := fmt.Sprintf("query-caller-%d", ts)

	type product struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	// --- server (will have its connection forcibly closed) ---
	srvCfg := rabbit.NewConfigWithDefaults()
	srvCfg.AppName = srvConnName
	srvCfg.ConnectionName = srvConnName
	srvCfg.Host = rabbitHost
	srvCfg.Port = rabbitPort

	srv, err := rabbit.NewApplication(srvCfg)
	require.NoError(t, err)

	err = srv.Registry().ServeQuery("get-thing", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return product{ID: "T-1", Name: "thing"}, nil
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

	// Sanity check: round-trip works on the original connection.
	{
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
			Resource:  "get-thing",
			QueryData: map[string]any{},
		}, srvConnName)
		require.NoError(t, err, "pre-reconnect query failed")
		var p product
		require.NoError(t, json.Unmarshal(raw, &p))
		require.Equal(t, "T-1", p.ID)
	}

	// Force-close the SERVER's connection from the broker side.
	require.NoError(t, forceCloseConnection(t, srvConnName), "could not force-close broker connection")

	// Repeatedly retry the query — the reconnect loop has 1s base backoff and
	// listeners need time to re-Start. Each attempt uses its own short context;
	// only the final one fails the test.
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		raw, qErr := caller.Gateway().RequestReply(ctx, async.AsyncQuery[any]{
			Resource:  "get-thing",
			QueryData: map[string]any{},
		}, srvConnName)
		cancel()
		if qErr == nil {
			var p product
			require.NoError(t, json.Unmarshal(raw, &p))
			assert.Equal(t, "T-1", p.ID)
			return
		}
		lastErr = qErr
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("post-reconnect query never succeeded; last error: %v", lastErr)
}

// pathEscape percent-encodes RabbitMQ connection names which may contain
// spaces, colons, and slashes (e.g. "172.17.0.1:54012 -> 172.17.0.2:5672").
// We deliberately use a strict whitelist instead of url.PathEscape so '/' is
// also encoded as %2F.
func pathEscape(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
