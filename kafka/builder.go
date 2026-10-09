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
	return &Application{inner: ikafka.NewKafkaApp(ikafka.Config(cfg))}, nil
}

func (a *Application) Registry() async.HandlerRegistry   { return a.inner.Registry() }
func (a *Application) EventBus() async.DomainEventBus    { return a.inner.EventBus() }
func (a *Application) Gateway() async.DirectAsyncGateway { return a.inner.Gateway() }
func (a *Application) Start(ctx context.Context) error   { return a.inner.Start(ctx) }

// Ready returns a channel that closes when Start has finished dialing the
// bootstrap brokers and consumers are running. Safe to call Emit only after
// this channel closes.
func (a *Application) Ready() <-chan struct{} { return a.inner.Ready() }
