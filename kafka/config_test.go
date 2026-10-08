package kafka

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewConfigWithDefaults(t *testing.T) {
	t.Parallel()
	cfg := NewConfigWithDefaults()

	assert.NotEmpty(t, cfg.InstanceID, "InstanceID should be auto-populated")
	assert.Equal(t, 1_000_000, cfg.MaxMessageBytes)
	assert.Equal(t, 5, cfg.MaxRetryAttempts)
	assert.Equal(t, ".dlq", cfg.DLQSuffix)
	assert.False(t, cfg.AllowAutoCreateTopics)
	assert.Equal(t, 3, cfg.DefaultPartitions)
	assert.Equal(t, 1, cfg.DefaultReplicationFactor)
	assert.True(t, cfg.AutoGenerateMissingEventID)
	assert.NotNil(t, cfg.Logger)
}

func TestWithDefaultsIsIdempotent(t *testing.T) {
	t.Parallel()
	cfg := KafkaConfig{
		AppName:          "svc",
		BootstrapBrokers: []string{"localhost:9092"},
	}.WithDefaults()

	assert.Equal(t, "svc", cfg.ConsumerGroupPrefix)
	require.NotNil(t, cfg.TopicNameFunc)
	assert.Equal(t, "svc.user.created", cfg.TopicNameFunc("user.created"))

	require.NotNil(t, cfg.CommandsTopicNameFunc)
	assert.Equal(t, "svc.commands", cfg.CommandsTopicNameFunc("svc"))
	assert.Equal(t, "remote.commands", cfg.CommandsTopicNameFunc("remote"))
	require.NotNil(t, cfg.QueriesTopicNameFunc)
	assert.Equal(t, "remote.queries", cfg.QueriesTopicNameFunc("remote"))
	require.NotNil(t, cfg.RepliesTopicNameFunc)
	assert.Equal(t, "svc.replies", cfg.RepliesTopicNameFunc("svc"))
	assert.False(t, cfg.DisableReplyListener, "reply listener is enabled by default")

	cfg2 := cfg.WithDefaults()
	assert.Equal(t, cfg.ClientID, cfg2.ClientID)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	base := KafkaConfig{
		AppName:                  "svc",
		BootstrapBrokers:         []string{"localhost:9092"},
		MaxRetryAttempts:         5,
		RetryInitialDelay:        1,
		RetryMaxDelay:            2,
		HandlerTimeout:           1,
		MaxMessageBytes:          1024,
		DefaultPartitions:        3,
		DefaultReplicationFactor: 1,
	}

	tests := []struct {
		name    string
		mutate  func(c *KafkaConfig)
		wantSub string
	}{
		{"ok", func(c *KafkaConfig) {}, ""},
		{"missing AppName", func(c *KafkaConfig) { c.AppName = "" }, "AppName"},
		{"empty BootstrapBrokers", func(c *KafkaConfig) { c.BootstrapBrokers = nil }, "BootstrapBrokers"},
		{"zero MaxRetryAttempts", func(c *KafkaConfig) { c.MaxRetryAttempts = 0 }, "MaxRetryAttempts"},
		{"negative MaxRetryAttempts", func(c *KafkaConfig) { c.MaxRetryAttempts = -1 }, "MaxRetryAttempts"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantSub == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, strings.Contains(err.Error(), tc.wantSub),
				"error %q should mention %q", err.Error(), tc.wantSub)
		})
	}
}
