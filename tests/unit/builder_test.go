package unit_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewApplication_RejectsInvalidConfig(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	// AppName missing — should fail validation.
	cfg.AppName = ""

	app, err := rabbit.NewApplication(cfg)
	require.Error(t, err)
	assert.Nil(t, app)
	assert.Contains(t, err.Error(), "AppName is required")
}

func TestNewApplication_AcceptsValidConfigWithCustomLogger(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "svc-builder"
	cfg.Logger = slog.Default()
	cfg.RetryDelay = 2 * time.Second

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	require.NotNil(t, app)
	require.NotNil(t, app.Registry())
}

// Exercises toInternalConfig's nil-Logger fallback path: when caller passes a
// RabbitConfig with no Logger, the builder must populate one before handing
// off to the internal app, and NewApplication must succeed.
func TestNewApplication_NilLogger_FallsBackToDefault(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "svc-nil-logger"
	cfg.Logger = nil

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)
	require.NotNil(t, app)
}

// Before Start has run, EventBus and Gateway have not been wired yet and
// must return nil. Ready() must return a non-nil channel that is still open.
func TestApplication_AccessorsBeforeStart(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "svc-accessors"

	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	assert.Nil(t, app.EventBus(), "EventBus must be nil before Start")
	assert.Nil(t, app.Gateway(), "Gateway must be nil before Start")

	ready := app.Ready()
	require.NotNil(t, ready)
	select {
	case <-ready:
		t.Fatal("Ready channel must remain open before Start completes")
	default:
	}
}
