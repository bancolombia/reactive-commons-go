package kafka

import (
	"fmt"
	"log/slog"
	"time"

	ikafka "github.com/bancolombia/reactive-commons-go/internal/kafka"
	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
)

// KafkaConfig configures a Kafka-backed reactive-commons Application.
// See specs/001-kafka-broker-support/data-model.md for field-level defaults
// and semantics. The internal Kafka config is the canonical field layout.
type KafkaConfig ikafka.Config

// NewConfigWithDefaults returns a KafkaConfig with production-safe defaults.
// Callers MUST set AppName and BootstrapBrokers before use.
func NewConfigWithDefaults() KafkaConfig {
	return KafkaConfig{
		InstanceID:                 uuid.NewString(),
		TopicNameFunc:              nil,
		NotificationTopicNameFunc:  nil,
		ProducerAcks:               kgo.RequireAll,
		ProducerBatchTimeout:       10 * time.Millisecond,
		ProducerCompression:        kgo.Snappy,
		MaxMessageBytes:            1_000_000,
		HandlerTimeout:             30 * time.Second,
		MaxRetryAttempts:           5,
		RetryInitialDelay:          1 * time.Second,
		RetryMaxDelay:              30 * time.Second,
		DLQSuffix:                  ".dlq",
		AllowAutoCreateTopics:      false,
		DefaultPartitions:          3,
		DefaultReplicationFactor:   1,
		ConsumerSessionTimeout:     30 * time.Second,
		ConsumerHeartbeatInterval:  3 * time.Second,
		AutoGenerateMissingEventID: true,
		Logger:                     slog.Default(),
	}
}

// WithDefaults returns a copy of cfg with any zero-valued fields replaced by
// defaults. Idempotent.
func (cfg KafkaConfig) WithDefaults() KafkaConfig {
	cfg = cfg.applyIdentityDefaults()
	cfg = cfg.applyRoutingDefaults()
	if cfg.ProducerAcks == 0 {
		cfg.ProducerAcks = kgo.RequireAll
	}
	if cfg.ProducerBatchTimeout == 0 {
		cfg.ProducerBatchTimeout = 10 * time.Millisecond
	}
	if cfg.ProducerCompression == 0 {
		cfg.ProducerCompression = kgo.Snappy
	}
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 1_000_000
	}
	if cfg.HandlerTimeout == 0 {
		cfg.HandlerTimeout = 30 * time.Second
	}
	if cfg.MaxRetryAttempts == 0 {
		cfg.MaxRetryAttempts = 5
	}
	if cfg.RetryInitialDelay == 0 {
		cfg.RetryInitialDelay = 1 * time.Second
	}
	if cfg.RetryMaxDelay == 0 {
		cfg.RetryMaxDelay = 30 * time.Second
	}
	if cfg.DLQSuffix == "" {
		cfg.DLQSuffix = ".dlq"
	}
	if cfg.DefaultPartitions == 0 {
		cfg.DefaultPartitions = 3
	}
	if cfg.DefaultReplicationFactor == 0 {
		cfg.DefaultReplicationFactor = 1
	}
	if cfg.ConsumerSessionTimeout == 0 {
		cfg.ConsumerSessionTimeout = 30 * time.Second
	}
	if cfg.ConsumerHeartbeatInterval == 0 {
		cfg.ConsumerHeartbeatInterval = 3 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg
}

func (cfg KafkaConfig) applyIdentityDefaults() KafkaConfig {
	if cfg.InstanceID == "" {
		cfg.InstanceID = uuid.NewString()
	}
	if cfg.ClientID == "" && cfg.AppName != "" {
		cfg.ClientID = cfg.AppName + "-" + shortID(cfg.InstanceID)
	}
	return cfg
}

func (cfg KafkaConfig) applyRoutingDefaults() KafkaConfig {
	if cfg.TopicNameFunc == nil {
		appName := cfg.AppName
		cfg.TopicNameFunc = func(n string) string { return appName + "." + n }
	}
	if cfg.NotificationTopicNameFunc == nil {
		appName := cfg.AppName
		cfg.NotificationTopicNameFunc = func(n string) string { return appName + "." + n }
	}
	if cfg.CommandsTopicNameFunc == nil {
		cfg.CommandsTopicNameFunc = func(appName string) string { return appName + ".commands" }
	}
	if cfg.QueriesTopicNameFunc == nil {
		cfg.QueriesTopicNameFunc = func(appName string) string { return appName + ".queries" }
	}
	if cfg.RepliesTopicNameFunc == nil {
		cfg.RepliesTopicNameFunc = func(appName string) string { return appName + ".replies" }
	}
	if cfg.ConsumerGroupPrefix == "" {
		cfg.ConsumerGroupPrefix = cfg.AppName
	}
	return cfg
}

// Validate returns nil if cfg is usable, or an error naming the first invalid
// field. Called automatically by NewApplication.
func (cfg KafkaConfig) Validate() error {
	if cfg.AppName == "" {
		return fmt.Errorf("kafka: KafkaConfig.AppName is required")
	}
	if len(cfg.BootstrapBrokers) == 0 {
		return fmt.Errorf("kafka: KafkaConfig.BootstrapBrokers must contain at least one entry")
	}
	if cfg.MaxRetryAttempts <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.MaxRetryAttempts must be > 0, got %d", cfg.MaxRetryAttempts)
	}
	if cfg.RetryInitialDelay <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.RetryInitialDelay must be > 0, got %s", cfg.RetryInitialDelay)
	}
	if cfg.RetryMaxDelay < cfg.RetryInitialDelay {
		return fmt.Errorf("kafka: KafkaConfig.RetryMaxDelay (%s) must be >= RetryInitialDelay (%s)", cfg.RetryMaxDelay, cfg.RetryInitialDelay)
	}
	if cfg.HandlerTimeout <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.HandlerTimeout must be > 0, got %s", cfg.HandlerTimeout)
	}
	if cfg.MaxMessageBytes <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.MaxMessageBytes must be > 0, got %d", cfg.MaxMessageBytes)
	}
	if cfg.DefaultPartitions <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.DefaultPartitions must be > 0, got %d", cfg.DefaultPartitions)
	}
	if cfg.DefaultReplicationFactor <= 0 {
		return fmt.Errorf("kafka: KafkaConfig.DefaultReplicationFactor must be > 0, got %d", cfg.DefaultReplicationFactor)
	}
	return nil
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
