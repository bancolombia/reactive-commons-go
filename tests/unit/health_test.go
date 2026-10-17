package unit_test

import (
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
