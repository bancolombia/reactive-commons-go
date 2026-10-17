package rabbit

import (
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnection_dialURL_PlainAMQPWithoutTLS(t *testing.T) {
	c := NewConnection(Config{
		Host:     "broker.example.com",
		Port:     5672,
		Username: "user",
		Password: "s3cret",
		VHost:    "/prod",
	})

	assert.Equal(t, "amqp://user:s3cret@broker.example.com:5672/prod", c.dialURL())
}

func TestConnection_dialURL_AMQPSWithTLS(t *testing.T) {
	c := NewConnection(Config{
		Host:     "broker.example.com",
		Port:     5671,
		Username: "user",
		Password: "s3cret",
		VHost:    "/prod",
		TLS:      &tls.Config{},
	})

	assert.Equal(t, "amqps://user:s3cret@broker.example.com:5671/prod", c.dialURL())
}

func TestConnection_dialConfig_PlumbsTLSClientConfig(t *testing.T) {
	tlsCfg := &tls.Config{ServerName: "broker.example.com"}
	c := NewConnection(Config{
		Host: "broker.example.com",
		Port: 5671,
		TLS:  tlsCfg,
	})

	assert.Same(t, tlsCfg, c.dialConfig().TLSClientConfig)

	plain := NewConnection(Config{Host: "localhost", Port: 5672}).dialConfig()
	assert.Nil(t, plain.TLSClientConfig)
}

// TestConnection_CloseStopsReconnectLoop is the regression test for a
// reconnectLoop that kept dialing after Close() because it never checked a
// stopped flag. The dial seam always fails, so the loop iterates over the
// backoff schedule without ever needing a real *amqp.Connection.
func TestConnection_CloseStopsReconnectLoop(t *testing.T) {
	// Shorten the backoff schedule so the test runs in milliseconds; restore
	// the production defaults afterwards.
	initial, max := reconnectInitialBackoff, reconnectMaxBackoff
	reconnectInitialBackoff, reconnectMaxBackoff = 50*time.Millisecond, 200*time.Millisecond
	defer func() { reconnectInitialBackoff, reconnectMaxBackoff = initial, max }()

	c := NewConnection(Config{Host: "localhost", Port: 5672, Username: "u", Password: "p", VHost: "/"})
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	var dials atomic.Int64
	c.dialFn = func(url string, cfg amqp.Config) (*amqp.Connection, error) {
		dials.Add(1)
		return nil, errors.New("dial refused")
	}

	done := make(chan struct{})
	go func() {
		c.reconnectLoop("amqp://localhost:5672/")
		close(done)
	}()

	// Wait until the loop has made at least two attempts.
	require.Eventually(t, func() bool { return dials.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, c.Close())
	count := dials.Load()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectLoop did not exit after Close")
	}
	// No dial attempt may happen after Close.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, count, dials.Load())
}

func TestConnection_IsOpen(t *testing.T) {
	c := NewConnection(Config{Host: "localhost", Port: 5672})

	// Never dialed: no connection, but not stopped.
	assert.False(t, c.IsOpen())

	c.stopped.Store(true)
	assert.False(t, c.IsOpen())

	c.stopped.Store(false)
	assert.False(t, c.IsOpen(), "no underlying connection means not open")
}
