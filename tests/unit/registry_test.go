package unit_test

import (
	"context"
	"testing"

	irabbit "github.com/bancolombia/reactive-commons-go/internal/rabbit"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildApp creates an app without connecting (no Start call).
func buildApp(t *testing.T, appName string) *rabbit.Application {
	t.Helper()
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = appName
	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	return app
}

func TestRegistry_DuplicateCommandHandler_ReturnsError(t *testing.T) {
	app := buildApp(t, "svc-dup-cmd")

	noop := func(ctx context.Context, cmd async.Command[any]) error { return nil }

	err := app.Registry().ListenCommand("create-invoice", noop)
	require.NoError(t, err)

	err = app.Registry().ListenCommand("create-invoice", noop)
	assert.ErrorIs(t, err, async.ErrDuplicateHandler)
}

func TestRegistry_DuplicateEventHandler_ReturnsError(t *testing.T) {
	app := buildApp(t, "svc-dup-evt")

	noop := func(ctx context.Context, e async.DomainEvent[any]) error { return nil }

	err := app.Registry().ListenEvent("order.created", noop)
	require.NoError(t, err)

	err = app.Registry().ListenEvent("order.created", noop)
	assert.ErrorIs(t, err, async.ErrDuplicateHandler)
}

func TestRegistry_DifferentCommandNames_BothRegistered(t *testing.T) {
	app := buildApp(t, "svc-multi-cmd")

	noop := func(ctx context.Context, cmd async.Command[any]) error { return nil }

	require.NoError(t, app.Registry().ListenCommand("cmd-a", noop))
	require.NoError(t, app.Registry().ListenCommand("cmd-b", noop))
}

func TestRegistry_QueryNames_ReturnsRegisteredResources(t *testing.T) {
	app := buildApp(t, "svc-query-names")

	noopQuery := func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	}

	require.NoError(t, app.Registry().ServeQuery("q1", noopQuery))
	require.NoError(t, app.Registry().ServeQuery("q2", noopQuery))

	// Duplicate registration should fail
	err := app.Registry().ServeQuery("q1", noopQuery)
	assert.ErrorIs(t, err, async.ErrDuplicateHandler)
}

func TestRegistry_ServeQuery_RejectsWildcardNames(t *testing.T) {
	app := buildApp(t, "svc-query-wildcard")

	noopQuery := func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	}

	err := app.Registry().ServeQuery("get-product.*", noopQuery)
	assert.ErrorIs(t, err, async.ErrWildcardNotSupported)

	err = app.Registry().ServeQuery("orders.#", noopQuery)
	assert.ErrorIs(t, err, async.ErrWildcardNotSupported)

	// Plain names still work after a rejection.
	assert.NoError(t, app.Registry().ServeQuery("plain-query", noopQuery))
}

func TestRegistry_ListenEvent_AcceptsWildcardNames(t *testing.T) {
	app := buildApp(t, "svc-event-wildcard")

	noop := func(ctx context.Context, e async.DomainEvent[any]) error { return nil }

	require.NoError(t, app.Registry().ListenEvent("order.*", noop))
	require.NoError(t, app.Registry().ListenEvent("inventory.#", noop))
}

func TestRegistry_ListenCommand_AcceptsWildcardNames(t *testing.T) {
	app := buildApp(t, "svc-cmd-wildcard")

	noop := func(ctx context.Context, c async.Command[any]) error { return nil }

	require.NoError(t, app.Registry().ListenCommand("invoice.*", noop))
}

func TestRegistry_ListenNotification_AcceptsWildcardNames(t *testing.T) {
	app := buildApp(t, "svc-notif-wildcard")

	noop := func(ctx context.Context, n async.Notification[any]) error { return nil }

	require.NoError(t, app.Registry().ListenNotification("alerts.#", noop))
}

func TestConnection_OnReconnect_HookRegistered(t *testing.T) {
	conn := irabbit.NewConnection(irabbit.Config{
		Host:   "localhost",
		Port:   5672,
		Logger: nil,
	})

	called := false
	conn.OnReconnect(func() { called = true })
	// Hook is registered; we don't dial so the hook is never invoked,
	// but registration itself should not panic.
	assert.False(t, called, "hook must not fire before reconnect occurs")
}

func TestConnection_PublisherChannel_ReturnsNilWhenUnconnected(t *testing.T) {
	conn := irabbit.NewConnection(irabbit.Config{
		Host:   "localhost",
		Port:   5672,
		Logger: nil,
	})
	// The publisher pool is nil before Dial — PublisherChannel returns nil entry.
	ch := conn.PublisherChannel()
	assert.Nil(t, ch, "publisher channel must be nil when not connected")
}
