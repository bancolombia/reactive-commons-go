package kafka_test

import (
	"context"
	"errors"
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

func TestGateway_AllMethodsReturnNotSupported(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	gw := app.Gateway()

	ctx := context.Background()

	err = gw.SendCommand(ctx, async.Command[any]{Name: "x"}, "target")
	assert.True(t, errors.Is(err, rckafka.ErrNotSupportedOnKafka), "SendCommand: %v", err)

	_, err = gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "x"}, "target")
	assert.True(t, errors.Is(err, rckafka.ErrNotSupportedOnKafka), "RequestReply: %v", err)

	err = gw.Reply(ctx, nil, async.From{})
	assert.True(t, errors.Is(err, rckafka.ErrNotSupportedOnKafka), "Reply: %v", err)
}

func TestRegistry_CommandAndQueryReturnNotSupported(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	reg := app.Registry()

	err = reg.ListenCommand("x", func(ctx context.Context, cmd async.Command[any]) error { return nil })
	assert.True(t, errors.Is(err, rckafka.ErrNotSupportedOnKafka))

	err = reg.ServeQuery("x", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
		return nil, nil
	})
	assert.True(t, errors.Is(err, rckafka.ErrNotSupportedOnKafka))
}

// TestUnsupportedOnKafka_SentinelWrapping covers T047: every method that
// returns ErrNotSupportedOnKafka must wrap the sentinel so callers can rely
// on errors.Is regardless of surrounding context (method name, cause chain).
func TestUnsupportedOnKafka_SentinelWrapping(t *testing.T) {
	t.Parallel()
	app, err := rckafka.NewApplication(minimalConfig())
	require.NoError(t, err)
	reg := app.Registry()
	gw := app.Gateway()
	ctx := context.Background()

	unsupported := []struct {
		name string
		err  error
	}{
		{"Gateway.SendCommand", gw.SendCommand(ctx, async.Command[any]{Name: "x"}, "t")},
		{"Gateway.Reply", gw.Reply(ctx, nil, async.From{})},
		{"Registry.ListenCommand", reg.ListenCommand("x", func(ctx context.Context, cmd async.Command[any]) error { return nil })},
		{"Registry.ServeQuery", reg.ServeQuery("x", func(ctx context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
			return nil, nil
		})},
	}
	// RequestReply returns two values; test it separately.
	_, rrErr := gw.RequestReply(ctx, async.AsyncQuery[any]{Resource: "x"}, "t")
	unsupported = append(unsupported, struct {
		name string
		err  error
	}{"Gateway.RequestReply", rrErr})

	for _, tc := range unsupported {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Error(t, tc.err, "%s must return an error", tc.name)
			assert.True(t, errors.Is(tc.err, rckafka.ErrNotSupportedOnKafka),
				"%s: errors.Is must find ErrNotSupportedOnKafka; got %v", tc.name, tc.err)
			assert.NotEqual(t, rckafka.ErrNotSupportedOnKafka, tc.err,
				"%s: error should WRAP the sentinel with context, not be the bare sentinel", tc.name)
		})
	}
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
