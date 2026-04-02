package rabbit

import (
	"context"
	"log/slog"

	irabbit "github.com/bancolombia/reactive-commons-go/internal/rabbit"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// Application is the public RabbitMQ-backed reactive-commons application.
// It implements async.Application and additionally provides Ready() so callers
// can wait for the broker connection and topology setup to complete.
type Application struct {
	inner *irabbit.RabbitApp
}

var _ async.Application = (*Application)(nil)

// NewApplication creates an Application backed by RabbitMQ with the given config.
// Returns error if config is invalid (missing AppName, etc.).
// Call Start to connect; use Ready() to wait until consumers are running.
func NewApplication(cfg RabbitConfig) (*Application, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	icfg := toInternalConfig(cfg)
	return &Application{inner: irabbit.NewRabbitApp(icfg)}, nil
}

func (a *Application) Registry() async.HandlerRegistry   { return a.inner.Registry() }
func (a *Application) EventBus() async.DomainEventBus    { return a.inner.EventBus() }
func (a *Application) Gateway() async.DirectAsyncGateway { return a.inner.Gateway() }
func (a *Application) Start(ctx context.Context) error   { return a.inner.Start(ctx) }

// Ready returns a channel that closes when Start has finished setting up the broker
// topology and consumers are running. Safe to call EventBus().Emit() only after this.
func (a *Application) Ready() <-chan struct{} { return a.inner.Ready() }

// toInternalConfig converts the public RabbitConfig to the internal Config type.
func toInternalConfig(cfg RabbitConfig) irabbit.Config {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return irabbit.Config{
		Host:                   cfg.Host,
		Port:                   cfg.Port,
		Username:               cfg.Username,
		Password:               cfg.Password,
		VHost:                  cfg.VirtualHost,
		AppName:                cfg.AppName,
		DomainEventsExchange:   cfg.DomainEventsExchange,
		DirectMessagesExchange: cfg.DirectMessagesExchange,
		GlobalReplyExchange:    cfg.GlobalReplyExchange,
		PrefetchCount:          cfg.PrefetchCount,
		ReplyTimeout:           cfg.ReplyTimeout,
		PersistentEvents:       cfg.PersistentEvents,
		PersistentCommands:     cfg.PersistentCommands,
		PersistentQueries:      cfg.PersistentQueries,
		WithDLQRetry:           cfg.WithDLQRetry,
		RetryDelay:             cfg.RetryDelay,
		Logger:                 log,
	}
}
