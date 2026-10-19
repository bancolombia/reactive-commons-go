package rabbit

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
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

// TestConnection_IsIntentionalClose is the regression test for the race where
// a connection that died before watchClose registered its listener delivers a
// nil error from NotifyClose — which watchClose used to mistake for an
// intentional Close(), leaving the app permanently disconnected.
func TestConnection_IsIntentionalClose(t *testing.T) {
	brokerErr := &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED"}

	// A real close reason always reconnects.
	assert.False(t, isIntentionalClose(brokerErr, true))
	assert.False(t, isIntentionalClose(brokerErr, false))

	// nil error + stopped: Close() shut the connection down on purpose.
	assert.True(t, isIntentionalClose(nil, true))

	// nil error + not stopped: the connection died before the watcher could
	// attach — this must reconnect, not be treated as intentional.
	assert.False(t, isIntentionalClose(nil, false))
}

// TestConnection_ReconnectLoop_RunsHooks guards the reconnect flow end to end:
// a successful redial must run the registered hooks without any publisher-pool
// step in between (the pool was removed — its failure used to gate recovery
// behind an unwatched, healthy connection).
func TestConnection_ReconnectLoop_RunsHooks(t *testing.T) {
	initial, max := reconnectInitialBackoff, reconnectMaxBackoff
	reconnectInitialBackoff, reconnectMaxBackoff = 10*time.Millisecond, 10*time.Millisecond
	defer func() { reconnectInitialBackoff, reconnectMaxBackoff = initial, max }()

	c := NewConnection(Config{Host: "localhost", Port: 5672, Username: "u", Password: "p", VHost: "/"})
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	c.dialFn = func(url string, cfg amqp.Config) (*amqp.Connection, error) {
		// A zero-value amqp.Connection reports open; enough for the hook test.
		return &amqp.Connection{}, nil
	}
	hookRan := make(chan struct{})
	c.OnReconnect(func() { close(hookRan) })

	done := make(chan struct{})
	go func() {
		c.reconnectLoop("amqp://localhost:5672/")
		close(done)
	}()

	select {
	case <-hookRan:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnect hooks did not run after a successful redial")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectLoop did not return after running the hooks")
	}
}

// recordingConn is a net.Conn that records write deadlines and accepts writes
// without touching a socket.
type recordingConn struct {
	net.Conn
	mu        sync.Mutex
	deadlines []time.Time
	writes    int
}

func (r *recordingConn) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.writes++
	r.mu.Unlock()
	return len(p), nil
}

func (r *recordingConn) SetWriteDeadline(t time.Time) error {
	r.mu.Lock()
	r.deadlines = append(r.deadlines, t)
	r.mu.Unlock()
	return nil
}

func (r *recordingConn) recordedDeadlines() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.deadlines...)
}

// TestWriteDeadlineConn_BoundsBlockedWrite proves a write that cannot make
// progress returns at the write timeout instead of hanging on the socket.
func TestWriteDeadlineConn_BoundsBlockedWrite(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	conn := &writeDeadlineConn{Conn: client, writeTimeout: 50 * time.Millisecond}
	start := time.Now()
	_, err := conn.Write([]byte("hello"))

	require.Error(t, err)
	assert.Less(t, time.Since(start), time.Second, "write was not bounded by the deadline")
}

// TestWriteDeadlineConn_ClampsToBaseDeadline proves a deadline installed by
// amqp091 (handshake or CloseDeadline) wins over the wrapper's longer timeout.
func TestWriteDeadlineConn_ClampsToBaseDeadline(t *testing.T) {
	rec := &recordingConn{}
	conn := &writeDeadlineConn{Conn: rec, writeTimeout: time.Hour}

	base := time.Now().Add(10 * time.Millisecond)
	require.NoError(t, conn.SetWriteDeadline(base))
	_, err := conn.Write([]byte("x"))
	require.NoError(t, err)

	deadlines := rec.recordedDeadlines()
	require.NotEmpty(t, deadlines)
	for _, d := range deadlines {
		assert.WithinDuration(t, base, d, time.Millisecond,
			"write deadline must be clamped to the amqp-managed base deadline")
	}
}

func TestConnection_DialConfig_InstallsWriteDeadlineDial(t *testing.T) {
	enabled := NewConnection(Config{Host: "h", Port: 5672, WriteTimeout: time.Second}).dialConfig()
	assert.NotNil(t, enabled.Dial, "WriteTimeout > 0 must install a custom dialer")

	disabled := NewConnection(Config{Host: "h", Port: 5672}).dialConfig()
	assert.Nil(t, disabled.Dial, "WriteTimeout <= 0 must leave amqp's default dialer")
}

func TestConnection_CloseConn_UsesSeam(t *testing.T) {
	c := NewConnection(Config{})
	called := false
	c.closeConnFn = func(*amqp.Connection) error {
		called = true
		return nil
	}

	require.NoError(t, c.closeConn(&amqp.Connection{}))
	assert.True(t, called)
}

// TestConnection_DialContextCancelsAndDiscardsLateConnection guards the leak:
// cancelling the startup context must not leave a connection (and its watcher)
// alive when the dial goroutine completes afterwards.
func TestConnection_DialContextCancelsAndDiscardsLateConnection(t *testing.T) {
	c := NewConnection(Config{Host: "localhost", Port: 5672, Username: "u", Password: "p", VHost: "/"})
	c.log = slog.New(slog.NewTextHandler(io.Discard, nil))

	started := make(chan struct{})
	release := make(chan struct{})
	c.dialFn = func(string, amqp.Config) (*amqp.Connection, error) {
		close(started)
		<-release
		return &amqp.Connection{}, nil
	}
	closed := make(chan struct{}, 1)
	c.closeConnFn = func(*amqp.Connection) error {
		closed <- struct{}{}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.DialContext(ctx) }()
	<-started
	cancel()

	require.ErrorIs(t, <-errCh, context.Canceled)
	close(release)

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("late dial connection was not discarded")
	}
	c.mu.RLock()
	assert.Nil(t, c.conn)
	c.mu.RUnlock()
	assert.Equal(t, ConnectionStopped, c.healthSnapshot().State)
}

// TestConnection_HealthStoppedIsTerminal: a late reconnect/close event must not
// move a stopped connection back to reconnecting.
func TestConnection_HealthStoppedIsTerminal(t *testing.T) {
	c := NewConnection(Config{})
	c.setHealth(ConnectionStopped, nil)
	c.setHealth(ConnectionReconnecting, errors.New("late close"))

	assert.Equal(t, ConnectionStopped, c.healthSnapshot().State)
	assert.Empty(t, c.healthSnapshot().LastError)
}
