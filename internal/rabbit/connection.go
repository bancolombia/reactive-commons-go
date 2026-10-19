package rabbit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Reconnect backoff schedule. Vars (not consts) so tests can shorten the
// schedule without changing the production defaults.
var (
	reconnectInitialBackoff = 1 * time.Second
	reconnectMaxBackoff     = 30 * time.Second
)

// modulePath is the Go module path used to look up the library's own version
// from the consumer binary's build info.
const modulePath = "github.com/bancolombia/reactive-commons-go"

// libVersion is the reactive-commons-go module version advertised in AMQP
// client-properties. Resolved once at init via runtime/debug.ReadBuildInfo so
// downstream binaries automatically expose the exact tag/pseudo-version they
// imported, without requiring -ldflags.
var libVersion = resolveLibVersion()

// resolveLibVersion walks the build info of the running binary to find this
// module's version. It returns:
//   - the dependency version when the library is imported (e.g. "v1.2.3" or
//     a "v0.0.0-YYYYMMDDHHMMSS-<sha>" pseudo-version);
//   - the main-module version when the library itself is built directly
//     (tests, examples);
//   - "devel" when running from a working copy without a tagged build;
//   - "unknown" if build info is unavailable.
func resolveLibVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if info.Main.Path == modulePath {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		return "devel"
	}
	for _, dep := range info.Deps {
		if dep != nil && dep.Path == modulePath {
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version
			}
			if dep.Version != "" {
				return dep.Version
			}
		}
	}
	return "unknown"
}

// clientProperties builds the AMQP client-properties table advertised during the
// connection handshake. RabbitMQ Management UI surfaces these (notably
// `connection_name`) in the Connections tab.
func clientProperties(cfg Config) amqp.Table {
	name := cfg.ConnectionName
	if name == "" {
		name = cfg.AppName
	}
	host, _ := os.Hostname()
	return amqp.Table{
		"connection_name": name,
		"product":         "reactive-commons-go",
		"platform":        "Go " + runtime.Version(),
		"version":         libVersion,
		"information":     "https://github.com/bancolombia/reactive-commons-go",
		"host":            host,
	}
}

// Connection manages a single AMQP connection and its reconnect lifecycle.
// Publisher and consumer channels are opened on demand by the Sender and the
// supervised consumers; the connection itself owns no channels.
type Connection struct {
	cfg  Config
	log  *slog.Logger
	mu   sync.RWMutex
	conn *amqp.Connection

	// stopped is set by Close and aborts any in-flight reconnectLoop.
	stopped atomic.Bool

	reconnectHooks []func()

	// dialFn opens the AMQP connection; a test seam overriding amqp.DialConfig.
	dialFn func(url string, cfg amqp.Config) (*amqp.Connection, error)

	// closeConnFn closes the underlying AMQP connection; a test seam overriding
	// amqp.Connection.CloseDeadline.
	closeConnFn func(*amqp.Connection) error

	// healthMu guards the connection health snapshot.
	healthMu sync.Mutex
	health   ConnectionHealth
}

// NewConnection creates a new Connection using cfg but does not dial yet.
// Call Dial to establish the AMQP connection.
func NewConnection(cfg Config) *Connection {
	c := &Connection{
		cfg: cfg,
		log: cfg.Logger,
	}
	c.setHealth(ConnectionIdle, nil)
	return c
}

// dialURL builds the AMQP connection URL. The scheme is "amqps" when a TLS
// configuration is set, otherwise "amqp" (clear-text).
func (c *Connection) dialURL() string {
	scheme := "amqp"
	if c.cfg.TLS != nil {
		scheme = "amqps"
	}
	return fmt.Sprintf("%s://%s:%s@%s:%d%s",
		scheme, c.cfg.Username, c.cfg.Password, c.cfg.Host, c.cfg.Port, c.cfg.VHost)
}

// dialConfig returns the amqp.Config used for both the initial dial and
// reconnects, carrying the optional TLS client configuration.
func (c *Connection) dialConfig() amqp.Config {
	cfg := amqp.Config{
		Vhost:           c.cfg.VHost,
		Properties:      clientProperties(c.cfg),
		Heartbeat:       10 * time.Second,
		Locale:          "en_US",
		TLSClientConfig: c.cfg.TLS,
	}
	if c.cfg.WriteTimeout > 0 {
		// Bound every socket write so a wedged TCP write cannot hold amqp091's
		// connection-wide send lock forever. amqp091 applies TLS around the
		// returned conn itself.
		cfg.Dial = handshakeDial(c.cfg.WriteTimeout)
	}
	return cfg
}

// Dial establishes the AMQP connection and starts the close watcher.
func (c *Connection) Dial() error {
	if c.stopped.Load() {
		return ErrNotConnected
	}
	url := c.dialURL()

	dial := c.dialFn
	if dial == nil {
		dial = amqp.DialConfig
	}
	conn, err := dial(url, c.dialConfig())

	if err != nil {
		c.setHealth(ConnectionIdle, err)
		return fmt.Errorf("reactive-commons: dial %s:%d: %w", c.cfg.Host, c.cfg.Port, err)
	}

	c.mu.Lock()
	if c.stopped.Load() {
		// Close() (or a cancelled DialContext) raced the dial: discard the
		// fresh connection instead of leaking it and starting a watcher.
		c.mu.Unlock()
		_ = c.closeConn(conn)
		c.setHealth(ConnectionStopped, nil)
		return ErrNotConnected
	}
	c.conn = conn
	c.mu.Unlock()

	c.setHealth(ConnectionConnected, nil)
	go c.watchClose(url)
	return nil
}

// Channel returns a new AMQP channel opened on the current connection.
func (c *Connection) Channel() (*amqp.Channel, error) {
	if c.stopped.Load() {
		return nil, ErrNotConnected
	}
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return nil, ErrNotConnected
	}
	return conn.Channel()
}

// IsOpen reports whether the connection is established, not closed and not
// stopped. Used by the Sender to decide whether a dead publisher channel can
// be self-healed or must wait for the connection-level reconnect hook.
func (c *Connection) IsOpen() bool {
	if c.stopped.Load() {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.conn != nil && !c.conn.IsClosed()
}

// OnReconnect registers a hook to be called after a successful reconnection.
func (c *Connection) OnReconnect(fn func()) {
	c.mu.Lock()
	c.reconnectHooks = append(c.reconnectHooks, fn)
	c.mu.Unlock()
}

// Close gracefully closes the connection. It also stops any in-flight
// reconnectLoop, so a connection is never re-established after the caller has
// shut the library down. Channels opened by the Sender and the supervised
// consumers are closed by their owners when the connection closes.
func (c *Connection) Close() error {
	c.stopped.Store(true)
	c.setHealth(ConnectionStopped, nil)

	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	return c.closeConn(conn)
}

// closeConn closes conn with a bounded close handshake so a wedged broker
// cannot keep shutdown from returning. A test seam can override it.
func (c *Connection) closeConn(conn *amqp.Connection) error {
	if conn == nil || conn.IsClosed() {
		return nil
	}
	if c.closeConnFn != nil {
		return c.closeConnFn(conn)
	}
	return conn.CloseDeadline(time.Now().Add(closeHandshakeTimeout))
}

// watchClose monitors the connection and reconnects with exponential backoff.
// Single-shot: this goroutine watches exactly one connection and exits after
// either an intentional close or after handing off to reconnectLoop. The
// reconnectLoop spawns a fresh watchClose for the new connection, which keeps
// exactly one watcher alive at a time. Looping here would double the watcher
// count on every reconnect (the original goroutine would race with the one
// spawned by reconnectLoop), causing duplicate AMQP connections and N-way
// concurrent hook execution on the next disconnect.
func (c *Connection) watchClose(url string) {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return
	}
	closedErr := <-conn.NotifyClose(make(chan *amqp.Error, 1))
	if isIntentionalClose(closedErr, c.stopped.Load()) {
		// Close() shut the connection down — do not reconnect.
		return
	}
	if closedErr == nil {
		// A nil error means the AMQP library closed the listener channel
		// without a reason. That happens when the connection died before this
		// watcher could register its listener: the broker forced the close,
		// not Close(). Treating it as intentional would leave the app
		// permanently down with no watcher alive to recover it.
		closedErr = &amqp.Error{Code: amqp.ConnectionForced, Reason: "connection closed before watcher attached"}
	}
	c.log.Error("reactive-commons: broker connection lost", "error", closedErr)
	c.setHealth(ConnectionReconnecting, closedErr)
	c.reconnectLoop(url)
}

// isIntentionalClose disambiguates a nil error from NotifyClose: nil means the
// listener channel was closed without an error, which happens both on an
// intentional Close() (stopped is set) and when the connection died before the
// listener was registered. Only the former must skip the reconnect.
func isIntentionalClose(closedErr *amqp.Error, stopped bool) bool {
	return closedErr == nil && stopped
}

func (c *Connection) reconnectLoop(url string) {
	backoff := reconnectInitialBackoff
	maxBackoff := reconnectMaxBackoff
	for {
		if c.stopped.Load() {
			// Close() aborted the reconnect; do not re-establish.
			return
		}
		c.log.Info("reactive-commons: attempting reconnect", "delay", backoff)
		time.Sleep(backoff)

		if c.stopped.Load() {
			// Close() arrived while waiting out the backoff.
			return
		}

		dial := c.dialFn
		if dial == nil {
			dial = amqp.DialConfig
		}
		conn, err := dial(url, c.dialConfig())
		if err != nil {
			c.log.Warn("reactive-commons: reconnect failed", "error", err)
			c.setHealth(ConnectionReconnecting, err)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()

		// Close() may have raced the successful dial: discard the fresh
		// connection instead of leaving a zombie behind.
		if c.stopped.Load() {
			_ = c.closeConn(conn)
			return
		}

		c.setHealth(ConnectionConnected, nil)
		c.log.Info("reactive-commons: reconnected successfully")
		// Watch the fresh connection before running the hooks. If it dies
		// first, watchClose's nil-error path still reconnects, but attaching
		// early shrinks the window in which a loss would go unnoticed.
		go c.watchClose(url)

		c.mu.RLock()
		hooks := make([]func(), len(c.reconnectHooks))
		copy(hooks, c.reconnectHooks)
		c.mu.RUnlock()
		for _, h := range hooks {
			h()
		}
		return
	}
}

// ErrNotConnected is returned when an operation is attempted before Dial succeeds.
var ErrNotConnected = fmt.Errorf("reactive-commons: not connected to broker")

// DialContext establishes the connection and returns when ready or ctx is done.
// On cancellation it marks the connection stopped and closes any connection
// the dial goroutine managed to store, so a cancelled Start cannot leak a live
// broker connection.
func (c *Connection) DialContext(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- c.Dial() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		c.stopped.Store(true)
		c.mu.Lock()
		conn := c.conn
		c.conn = nil
		c.mu.Unlock()
		if conn != nil {
			_ = c.closeConn(conn)
		}
		c.setHealth(ConnectionStopped, ctx.Err())
		return ctx.Err()
	}
}

// setHealth records a connection health transition. Stopped is terminal: a
// late dial failure or close event cannot move the state back to reconnecting.
func (c *Connection) setHealth(state ConnectionState, err error) {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()

	if c.health.State == ConnectionStopped && state != ConnectionStopped {
		return
	}
	if c.health.State != state {
		c.health.State = state
		c.health.Since = time.Now()
	}
	if err != nil {
		c.health.LastError = err.Error()
	} else if state == ConnectionConnected {
		c.health.LastError = ""
	}
}

// healthSnapshot returns a copy of the current connection health.
func (c *Connection) healthSnapshot() ConnectionHealth {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	return c.health
}
