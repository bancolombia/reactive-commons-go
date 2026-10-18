// Package bench holds the configuration and output helpers shared by the
// RabbitMQ throughput benchmark examples.
package bench

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bancolombia/reactive-commons-go/rabbit"
)

// EnvString returns the value of key, or def when the variable is unset or empty.
func EnvString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// EnvInt parses an integer environment variable, returning def when unset.
func EnvInt(key string, def int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return v, nil
}

// EnvBool parses a boolean environment variable, returning def when unset.
func EnvBool(key string, def bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return v, nil
}

// EnvDuration parses a Go duration environment variable, returning def when unset.
func EnvDuration(key string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, raw, err)
	}
	return v, nil
}

// RabbitConfig builds a RabbitMQ config from the standard RABBITMQ_* variables.
func RabbitConfig(appName string) (rabbit.RabbitConfig, error) {
	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.Host = EnvString("RABBITMQ_HOST", cfg.Host)
	cfg.Username = EnvString("RABBITMQ_USERNAME", cfg.Username)
	cfg.Password = EnvString("RABBITMQ_PASSWORD", cfg.Password)
	cfg.VirtualHost = EnvString("RABBITMQ_VHOST", cfg.VirtualHost)

	port, err := EnvInt("RABBITMQ_PORT", cfg.Port)
	if err != nil {
		return rabbit.RabbitConfig{}, err
	}
	cfg.Port = port
	return cfg, nil
}

// ValidateEventName rejects empty event names and wildcards. A wildcard binding
// would consume unrelated traffic and corrupt the measurement.
func ValidateEventName(name string) error {
	if name == "" {
		return fmt.Errorf("event name must not be empty")
	}
	if strings.ContainsAny(name, "*#") {
		return fmt.Errorf("event name %q must not contain wildcards (* or #)", name)
	}
	return nil
}

// AppNameWithRunSuffix returns base with a unique suffix so every listener run
// starts with an empty queue.
func AppNameWithRunSuffix(base string) string {
	return base + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// Printf writes benchmark results to stdout. Diagnostics use the logger on
// stderr, so the result block stays clean and greppable.
func Printf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stdout, format, args...)
}
