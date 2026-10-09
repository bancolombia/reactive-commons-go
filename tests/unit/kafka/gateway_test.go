package kafka_test

import (
	"context"
	"testing"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func minimalConfig() rckafka.KafkaConfig {
	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "svc-test"
	cfg.BootstrapBrokers = []string{"localhost:9092"}
	return cfg
}

func TestNewApplication_MinimalConfig(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	assert.NotNil(t, app.Registry())
	assert.NotNil(t, app.EventBus())
	assert.NotNil(t, app.Gateway())
}

func TestNewApplication_InvalidConfig(t *testing.T) {
	t.Parallel()
	_, err := rckafka.NewApplication(rckafka.KafkaConfig{})
	require.Error(t, err)
}

// Before Start the gateway is a not-ready stub: every method must error
// telling the caller to wait for <-app.Ready().
func TestGateway_PreStart_ReturnsNotReady(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	gw := app.Gateway()

	ctx := context.Background()

	err = gw.SendCommand(ctx, async.Command[any]{Name: "x"}, "target")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")

	_, err = gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "x"}, "target")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")

	err = gw.Reply(ctx, nil, async.From{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")
}

func TestRegistry_CommandAndQueryRegistration(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	reg := app.Registry()

	cmdHandler := func(ctx context.Context, cmd async.Command[any]) error { return nil }
	queryHandler := func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	}

	require.NoError(t, reg.ListenCommand("orders.create", cmdHandler))
	require.NoError(t, reg.ServeQuery("get-product", queryHandler))

	// Duplicate registration returns ErrDuplicateHandler (existing sentinel).
	assert.ErrorIs(t, reg.ListenCommand("orders.create", cmdHandler), async.ErrDuplicateHandler)
	assert.ErrorIs(t, reg.ServeQuery("get-product", queryHandler), async.ErrDuplicateHandler)

	// Query names cannot contain wildcards; command names can.
	assert.ErrorIs(t, reg.ServeQuery("order.*", queryHandler), async.ErrWildcardNotSupported)
	require.NoError(t, reg.ListenCommand("order.#", cmdHandler))
}

func TestRegistry_EventAndNotificationSucceed(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	reg := app.Registry()

	require.NoError(t, reg.ListenEvent("evt", func(ctx context.Context, e async.DomainEvent[any]) error {
		return nil
	}))
	require.NoError(t, reg.ListenNotification("ntf", func(ctx context.Context, n async.Notification[any]) error {
		return nil
	}))

	// Duplicate registration returns ErrDuplicateHandler (existing sentinel).
	err = reg.ListenEvent("evt", func(ctx context.Context, e async.DomainEvent[any]) error { return nil })
	assert.ErrorIs(t, err, async.ErrDuplicateHandler)
}
