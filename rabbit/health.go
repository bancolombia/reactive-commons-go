package rabbit

import (
	"time"

	irabbit "github.com/bancolombia/reactive-commons-go/internal/rabbit"
)

// ConnectionState is the lifecycle state of the AMQP connection.
type ConnectionState string

const (
	// ConnectionIdle means Start has not established a connection yet.
	ConnectionIdle ConnectionState = "idle"
	// ConnectionConnected means the AMQP connection is established.
	ConnectionConnected ConnectionState = "connected"
	// ConnectionReconnecting means the connection was lost and is being retried.
	ConnectionReconnecting ConnectionState = "reconnecting"
	// ConnectionStopped means the application has shut down.
	ConnectionStopped ConnectionState = "stopped"
)

// ConsumerState is the lifecycle state of one supervised consumer.
type ConsumerState string

const (
	// ConsumerStopped means the supervisor is not consuming.
	ConsumerStopped ConsumerState = "stopped"
	// ConsumerRebuilding means the supervisor is opening or reopening its channel.
	ConsumerRebuilding ConsumerState = "rebuilding"
	// ConsumerRunning means the supervisor is consuming deliveries.
	ConsumerRunning ConsumerState = "running"
)

// ConnectionHealth is a point-in-time snapshot of the AMQP connection.
type ConnectionHealth struct {
	State     ConnectionState
	Since     time.Time
	LastError string
}

// ConsumerHealth is a point-in-time snapshot of one supervised consumer.
type ConsumerHealth struct {
	State     ConsumerState
	Since     time.Time
	LastError string
}

// Health is a point-in-time snapshot of the application's connection and
// consumer health. It is safe to poll from an HTTP health endpoint.
//
// Ready reports that initial startup completed; it is the same one-shot signal
// as Ready(), not a live liveness flag. Healthy is the live derived signal:
// true when the connection is connected and every configured consumer is
// running.
type Health struct {
	Ready      bool
	Connection ConnectionHealth
	// Consumers is keyed by listener kind: "event", "command", "query",
	// "reply", and "notification". Only configured listeners are present;
	// "reply" is always present after Start.
	Consumers map[string]ConsumerHealth
	Healthy   bool
}

// Health returns a point-in-time snapshot of the connection and every
// configured consumer. Safe to call before Start (idle, not healthy) and after
// shutdown (stopped).
func (a *Application) Health() Health {
	return healthFromInternal(a.inner.Health())
}

func healthFromInternal(h irabbit.Health) Health {
	out := Health{
		Ready: h.Ready,
		Connection: ConnectionHealth{
			State:     ConnectionState(h.Connection.State),
			Since:     h.Connection.Since,
			LastError: h.Connection.LastError,
		},
		Consumers: make(map[string]ConsumerHealth, len(h.Consumers)),
		Healthy:   h.Healthy,
	}
	for name, c := range h.Consumers {
		out.Consumers[name] = ConsumerHealth{
			State:     ConsumerState(c.State),
			Since:     c.Since,
			LastError: c.LastError,
		}
	}
	return out
}
