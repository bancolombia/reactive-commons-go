//go:build integration

package rabbit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealth_BeforeStart_ReportsIdle: the snapshot is usable before Start and
// reports an idle connection with no consumers.
func TestHealth_BeforeStart_ReportsIdle(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = fmt.Sprintf("health-idle-%d", time.Now().UnixNano())

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	h := app.Health()
	assert.False(t, h.Ready)
	assert.False(t, h.Healthy)
	assert.Equal(t, rabbit.ConnectionIdle, h.Connection.State)
	assert.Empty(t, h.Consumers)
}

// TestHealth_TransitionsAcrossReconnect: Health must report the outage and
// then return to healthy after the automatic reconnect/restore, proving an
// outage cannot look normal.
func TestHealth_TransitionsAcrossReconnect(t *testing.T) {
	if rabbitMgmtPort == 0 {
		t.Skip("management plugin not available; set RABBITMQ_MGMT_PORT or use a -management image")
	}

	ts := time.Now().UnixNano()
	appName := fmt.Sprintf("health-rc-%d", ts)
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.ConnectionName = appName
	cfg.Host = rabbitHost
	cfg.Port = rabbitPort

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	received := make(chan string, 8)
	require.NoError(t, app.Registry().ListenEvent("health.event", func(_ context.Context, e async.DomainEvent[any]) error {
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

	require.Eventually(t, func() bool { return app.Health().Healthy }, 10*time.Second, 25*time.Millisecond,
		"app never reported healthy after startup")

	h := app.Health()
	assert.True(t, h.Ready)
	assert.Equal(t, rabbit.ConnectionConnected, h.Connection.State)
	require.Contains(t, h.Consumers, "event")
	require.Contains(t, h.Consumers, "reply")
	assert.Equal(t, rabbit.ConsumerRunning, h.Consumers["event"].State)
	assert.Equal(t, rabbit.ConsumerRunning, h.Consumers["reply"].State)

	// Force a broker-side connection close. The 1s reconnect backoff makes the
	// reconnecting/unhealthy state observable.
	require.NoError(t, forceCloseConnection(t, appName))
	require.Eventually(t, func() bool {
		s := app.Health()
		return !s.Healthy || s.Connection.State == rabbit.ConnectionReconnecting
	}, 10*time.Second, 25*time.Millisecond, "Health never reflected the connection outage")

	// The reconnect/restore flow must return the app to healthy.
	require.Eventually(t, func() bool { return app.Health().Healthy }, 30*time.Second, 100*time.Millisecond,
		"Health never recovered after reconnect")

	// And processing must actually work again.
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    "health.event",
		EventID: "after",
	}))
	select {
	case id := <-received:
		assert.Equal(t, "after", id)
	case <-time.After(10 * time.Second):
		t.Fatal("event not consumed after health recovered")
	}
}
