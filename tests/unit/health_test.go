package unit_test

import (
	"encoding/json"
	"testing"

	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRabbitApplication_Health_BeforeStart: Health is callable before Start and
// reports an idle connection with no consumers.
func TestRabbitApplication_Health_BeforeStart(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "health-test"
	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	h := app.Health()
	assert.False(t, h.Ready)
	assert.False(t, h.Healthy)
	assert.Equal(t, rabbit.ConnectionIdle, h.Connection.State)
	assert.Empty(t, h.Consumers)
}

// TestRabbitApplication_Health_ReturnsCopies: mutating a returned snapshot must
// not affect later snapshots.
func TestRabbitApplication_Health_ReturnsCopies(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "health-test"
	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	h := app.Health()
	h.Consumers["bogus"] = rabbit.ConsumerHealth{State: rabbit.ConsumerRunning}

	h2 := app.Health()
	assert.NotContains(t, h2.Consumers, "bogus")
}

// TestRabbitApplication_Health_JSONShape locks the JSON keys used by the
// /health endpoint in the async-sender-client example.
func TestRabbitApplication_Health_JSONShape(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "health-test"
	app, err := rabbit.NewApplication(cfg)
	require.NoError(t, err)

	raw, err := json.Marshal(app.Health())
	require.NoError(t, err)

	assert.Contains(t, string(raw), `"ready":false`)
	assert.Contains(t, string(raw), `"healthy":false`)
	assert.Contains(t, string(raw), `"connection":{"state":"idle"`)
	assert.Contains(t, string(raw), `"consumers":{}`)
}
