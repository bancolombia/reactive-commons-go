package kafka

import (
	"context"

	ikafka "github.com/bancolombia/reactive-commons-go/internal/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// Application is the public Kafka-backed reactive-commons application. It
// implements async.Application and additionally provides Ready() so callers
// can wait for the broker connection and topology setup to complete.
type Application struct {
	inner *ikafka.KafkaApp
}

var _ async.Application = (*Application)(nil)

// NewApplication creates an Application backed by Kafka with the given config.
// Returns error if config is invalid (missing AppName, empty BootstrapBrokers,
// etc.). Call Start to connect; use Ready() to wait until consumers are
// running.
func NewApplication(cfg KafkaConfig) (*Application, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Application{inner: ikafka.NewKafkaApp(toInternalConfig(cfg))}, nil
}

func (a *Application) Registry() async.HandlerRegistry   { return a.inner.Registry() }
func (a *Application) EventBus() async.DomainEventBus    { return a.inner.EventBus() }
func (a *Application) Gateway() async.DirectAsyncGateway { return a.inner.Gateway() }
func (a *Application) Start(ctx context.Context) error   { return a.inner.Start(ctx) }

// Ready returns a channel that closes when Start has finished dialing the
// bootstrap brokers and consumers are running. Safe to call Emit only after
// this channel closes.
func (a *Application) Ready() <-chan struct{} { return a.inner.Ready() }

func toInternalConfig(cfg KafkaConfig) ikafka.Config {
	return ikafka.Config{
		AppName:                    cfg.AppName,
		BootstrapBrokers:           cfg.BootstrapBrokers,
		ClientID:                   cfg.ClientID,
		InstanceID:                 cfg.InstanceID,
		TLS:                        cfg.TLS,
		SASL:                       cfg.SASL,
		TopicNameFunc:              cfg.TopicNameFunc,
		NotificationTopicNameFunc:  cfg.NotificationTopicNameFunc,
		ConsumerGroupPrefix:        cfg.ConsumerGroupPrefix,
		ProducerAcks:               cfg.ProducerAcks,
		ProducerBatchTimeout:       cfg.ProducerBatchTimeout,
		ProducerCompression:        cfg.ProducerCompression,
		MaxMessageBytes:            cfg.MaxMessageBytes,
		HandlerTimeout:             cfg.HandlerTimeout,
		MaxRetryAttempts:           cfg.MaxRetryAttempts,
		RetryInitialDelay:          cfg.RetryInitialDelay,
		RetryMaxDelay:              cfg.RetryMaxDelay,
		DLQSuffix:                  cfg.DLQSuffix,
		AllowAutoCreateTopics:      cfg.AllowAutoCreateTopics,
		DefaultPartitions:          cfg.DefaultPartitions,
		DefaultReplicationFactor:   cfg.DefaultReplicationFactor,
		ConsumerSessionTimeout:     cfg.ConsumerSessionTimeout,
		ConsumerHeartbeatInterval:  cfg.ConsumerHeartbeatInterval,
		AutoGenerateMissingEventID: cfg.AutoGenerateMissingEventID,
		Logger:                     cfg.Logger,
		Tracer:                     cfg.Tracer,
		MeterProvider:              cfg.MeterProvider,
	}
}
