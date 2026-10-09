package rabbit

import (
	"crypto/tls"
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

	// TLS enables a TLS (AMQPS) connection when non-nil. Nil means plain AMQP.
	TLS *tls.Config

	// ConnectionName advertised to RabbitMQ as the `connection_name` client
	// property. Empty falls back to AppName at dial time.
	ConnectionName string

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

	// QueueType is the RabbitMQ queue type ("classic" or "quorum") used for
	// durable consumer queues and their DLQs. Empty means classic.
	QueueType string

	Logger *slog.Logger
}
