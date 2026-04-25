package unit_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRabbitConfig_WithDefaults_FillsAllZeroFields(t *testing.T) {
	cfg := rabbit.RabbitConfig{AppName: "svc"}.WithDefaults()

	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5672, cfg.Port)
	assert.Equal(t, "guest", cfg.Username)
	assert.Equal(t, "guest", cfg.Password)
	assert.Equal(t, "/", cfg.VirtualHost)
	assert.Equal(t, "domainEvents", cfg.DomainEventsExchange)
	assert.Equal(t, "directMessages", cfg.DirectMessagesExchange)
	assert.Equal(t, "globalReply", cfg.GlobalReplyExchange)
	assert.Equal(t, 250, cfg.PrefetchCount)
	assert.Equal(t, 15*time.Second, cfg.ReplyTimeout)
	assert.Equal(t, 1*time.Second, cfg.RetryDelay)
	assert.NotNil(t, cfg.Logger)
}

func TestRabbitConfig_WithDefaults_PreservesExplicitValues(t *testing.T) {
	custom := slog.Default()
	cfg := rabbit.RabbitConfig{
		AppName:                "svc",
		Host:                   "broker",
		Port:                   5673,
		Username:               "u",
		Password:               "p",
		VirtualHost:            "/v",
		DomainEventsExchange:   "evt",
		DirectMessagesExchange: "dir",
		GlobalReplyExchange:    "rep",
		PrefetchCount:          10,
		ReplyTimeout:           5 * time.Second,
		WithDLQRetry:           true,
		RetryDelay:             3 * time.Second,
		Logger:                 custom,
	}.WithDefaults()

	assert.Equal(t, "broker", cfg.Host)
	assert.Equal(t, 5673, cfg.Port)
	assert.Equal(t, "u", cfg.Username)
	assert.Equal(t, "p", cfg.Password)
	assert.Equal(t, "/v", cfg.VirtualHost)
	assert.Equal(t, "evt", cfg.DomainEventsExchange)
	assert.Equal(t, "dir", cfg.DirectMessagesExchange)
	assert.Equal(t, "rep", cfg.GlobalReplyExchange)
	assert.Equal(t, 10, cfg.PrefetchCount)
	assert.Equal(t, 5*time.Second, cfg.ReplyTimeout)
	assert.True(t, cfg.WithDLQRetry)
	assert.Equal(t, 3*time.Second, cfg.RetryDelay)
	assert.Same(t, custom, cfg.Logger)
}

// When WithDLQRetry is false, RetryDelay is forced to the default 1s even if the caller
// supplied a custom value, mirroring the existing implementation in rabbit/config.go.
func TestRabbitConfig_WithDefaults_RetryDelayResetWhenDLQDisabled(t *testing.T) {
	cfg := rabbit.RabbitConfig{
		AppName:      "svc",
		WithDLQRetry: false,
		RetryDelay:   42 * time.Second,
	}.WithDefaults()

	assert.Equal(t, 1*time.Second, cfg.RetryDelay)
}

func TestRabbitConfig_NewConfigWithDefaults_ReturnsCompleteDefaults(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()

	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5672, cfg.Port)
	assert.True(t, cfg.PersistentEvents)
	assert.True(t, cfg.PersistentCommands)
	assert.False(t, cfg.PersistentQueries)
	assert.False(t, cfg.WithDLQRetry)
	assert.NotNil(t, cfg.Logger)
}

func TestRabbitConfig_Validate(t *testing.T) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "svc"
	require.NoError(t, cfg.Validate())

	cfg.AppName = ""
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AppName is required")
}
