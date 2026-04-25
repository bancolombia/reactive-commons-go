package rabbit

import (
	"errors"
	"log/slog"
	"time"
)

// RabbitConfig holds all configuration for a RabbitMQ-backed Application instance.
// Exchange name defaults match reactive-commons-java for wire compatibility.
type RabbitConfig struct {
	// Connection settings
	Host        string // default: "localhost"
	Port        int    // default: 5672
	Username    string // default: "guest"
	Password    string // default: "guest"
	VirtualHost string // default: "/"

	// AppName is required. Used for queue naming (e.g. "{AppName}.subsEvents").
	AppName string

	// Exchange names — defaults match reactive-commons-java
	DomainEventsExchange   string // default: "domainEvents"
	DirectMessagesExchange string // default: "directMessages"
	GlobalReplyExchange    string // default: "globalReply"

	// Behaviour
	PrefetchCount      int           // default: 250
	ReplyTimeout       time.Duration // default: 15s
	PersistentEvents   bool          // default: true
	PersistentCommands bool          // default: true
	PersistentQueries  bool          // default: false
	WithDLQRetry       bool          // default: false
	RetryDelay         time.Duration // default: 1s (used only when WithDLQRetry=true)

	// Logger is an optional structured logger. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// WithDefaults returns a copy of cfg with all zero-value fields set to their defaults.
func (cfg RabbitConfig) WithDefaults() RabbitConfig {
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.Port == 0 {
		cfg.Port = 5672
	}
	if cfg.Username == "" {
		cfg.Username = "guest"
	}
	if cfg.Password == "" {
		cfg.Password = "guest" //NOSONAR go:S2068 - default RabbitMQ dev credential, not a real secret
	}
	if cfg.VirtualHost == "" {
		cfg.VirtualHost = "/"
	}
	if cfg.DomainEventsExchange == "" {
		cfg.DomainEventsExchange = "domainEvents"
	}
	if cfg.DirectMessagesExchange == "" {
		cfg.DirectMessagesExchange = "directMessages"
	}
	if cfg.GlobalReplyExchange == "" {
		cfg.GlobalReplyExchange = "globalReply"
	}
	if cfg.PrefetchCount == 0 {
		cfg.PrefetchCount = 250
	}
	if cfg.ReplyTimeout == 0 {
		cfg.ReplyTimeout = 15 * time.Second
	}
	if !cfg.WithDLQRetry || cfg.RetryDelay == 0 {
		cfg.RetryDelay = 1 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// PersistentEvents and PersistentCommands default to true
	// We can't distinguish "not set" from "false" for booleans, so callers must
	// explicitly set these or use NewConfigWithDefaults.
	return cfg
}

// NewConfigWithDefaults returns a RabbitConfig with all defaults pre-set.
// Callers only need to supply AppName (required) and any overrides.
func NewConfigWithDefaults() RabbitConfig {
	return RabbitConfig{
		Host:                   "localhost",
		Port:                   5672,
		Username:               "guest",
		Password:               "guest",
		VirtualHost:            "/",
		DomainEventsExchange:   "domainEvents",
		DirectMessagesExchange: "directMessages",
		GlobalReplyExchange:    "globalReply",
		PrefetchCount:          250,
		ReplyTimeout:           15 * time.Second,
		PersistentEvents:       true,
		PersistentCommands:     true,
		PersistentQueries:      false,
		WithDLQRetry:           false,
		RetryDelay:             1 * time.Second,
		Logger:                 slog.Default(),
	}
}

// Validate returns an error if cfg is missing required fields.
func (cfg RabbitConfig) Validate() error {
	if cfg.AppName == "" {
		return errors.New("reactive-commons: RabbitConfig.AppName is required")
	}
	return nil
}
