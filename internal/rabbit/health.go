package rabbit

import "time"

// ConnectionState is the lifecycle state of the AMQP connection.
type ConnectionState string

const (
	// ConnectionIdle means Start has not established a connection yet.
	ConnectionIdle ConnectionState = "idle"
	// ConnectionConnected means the AMQP connection is established.
	ConnectionConnected ConnectionState = "connected"
	// ConnectionReconnecting means the connection was lost and is being retried.
	ConnectionReconnecting ConnectionState = "reconnecting"
	// ConnectionStopped means the application has shut down. Terminal.
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
type Health struct {
	// Ready reports whether initial startup (topology + consumers) completed.
	// It is the same one-shot signal as Ready(), not a live liveness flag.
	Ready      bool
	Connection ConnectionHealth
	Consumers  map[string]ConsumerHealth
	// Healthy is true when the connection is connected and every configured
	// consumer is running.
	Healthy bool
}
