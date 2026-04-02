package rabbit

import (
	"log/slog"
	"time"
)

// Config holds the resolved configuration used internally by the RabbitMQ implementation.
// It is populated from the public rabbit.RabbitConfig by the factory function.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	VHost    string
	AppName  string

	DomainEventsExchange   string
	DirectMessagesExchange string
	GlobalReplyExchange    string

	PrefetchCount      int
	ReplyTimeout       time.Duration
	PersistentEvents   bool
	PersistentCommands bool
	PersistentQueries  bool
	WithDLQRetry       bool
	RetryDelay         time.Duration

	Logger *slog.Logger
}
