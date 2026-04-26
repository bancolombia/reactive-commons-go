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

const publisherPoolSize = 4

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

// Connection manages a single AMQP connection and a pool of publisher channels.
type Connection struct {
	cfg     Config
	log     *slog.Logger
	mu      sync.RWMutex
	conn    *amqp.Connection
	pubPool [publisherPoolSize]*amqp.Channel
	robin   atomic.Uint64

	reconnectHooks []func()
}

// NewConnection creates a new Connection using cfg but does not dial yet.
// Call Dial to establish the AMQP connection.
func NewConnection(cfg Config) *Connection {
	return &Connection{
		cfg: cfg,
		log: cfg.Logger,
	}
}

// Dial establishes the AMQP connection and initialises the publisher channel pool.
func (c *Connection) Dial() error {
	url := fmt.Sprintf("amqp://%s:%s@%s:%d%s",
		c.cfg.Username, c.cfg.Password, c.cfg.Host, c.cfg.Port, c.cfg.VHost)

	conn, err := amqp.DialConfig(url, amqp.Config{
		Vhost:      c.cfg.VHost,
		Properties: clientProperties(c.cfg),
		Heartbeat:  10 * time.Second,
		Locale:     "en_US",
	})
	if err != nil {
		return fmt.Errorf("reactive-commons: dial %s:%d: %w", c.cfg.Host, c.cfg.Port, err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	if err := c.initPublisherPool(); err != nil {
		return err
	}

	go c.watchClose(url)
	return nil
}

func (c *Connection) initPublisherPool() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range publisherPoolSize {
		ch, err := c.conn.Channel()
		if err != nil {
			return fmt.Errorf("reactive-commons: open publisher channel %d: %w", i, err)
		}
		if err := ch.Confirm(false); err != nil {
			return fmt.Errorf("reactive-commons: enable confirms on channel %d: %w", i, err)
		}
		c.pubPool[i] = ch
	}
	return nil
}

// Channel returns a new AMQP channel for consuming (not from the publisher pool).
func (c *Connection) Channel() (*amqp.Channel, error) {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return nil, ErrNotConnected
	}
	return conn.Channel()
}

// PublisherChannel returns the next publisher channel from the pool (round-robin).
func (c *Connection) PublisherChannel() *amqp.Channel {
	idx := c.robin.Add(1) % publisherPoolSize
	c.mu.RLock()
	ch := c.pubPool[idx]
	c.mu.RUnlock()
	return ch
}

// OnReconnect registers a hook to be called after a successful reconnection.
func (c *Connection) OnReconnect(fn func()) {
	c.mu.Lock()
	c.reconnectHooks = append(c.reconnectHooks, fn)
	c.mu.Unlock()
}

// Close gracefully closes all channels and the connection.
func (c *Connection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ch := range c.pubPool {
		if ch != nil {
			_ = ch.Close()
			c.pubPool[i] = nil
		}
	}
	if c.conn != nil && !c.conn.IsClosed() {
		return c.conn.Close()
	}
	return nil
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
	if closedErr == nil {
		// Intentional close — do not reconnect.
		return
	}
	c.log.Error("reactive-commons: broker connection lost", "error", closedErr)
	c.reconnectLoop(url)
}

func (c *Connection) reconnectLoop(url string) {
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		c.log.Info("reactive-commons: attempting reconnect", "delay", backoff)
		time.Sleep(backoff)

		conn, err := amqp.DialConfig(url, amqp.Config{
			Vhost:      c.cfg.VHost,
			Properties: clientProperties(c.cfg),
			Heartbeat:  10 * time.Second,
			Locale:     "en_US",
		})
		if err != nil {
			c.log.Warn("reactive-commons: reconnect failed", "error", err)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()

		if err := c.initPublisherPool(); err != nil {
			c.log.Error("reactive-commons: failed to init publisher pool after reconnect", "error", err)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		c.log.Info("reactive-commons: reconnected successfully")
		c.mu.RLock()
		hooks := make([]func(), len(c.reconnectHooks))
		copy(hooks, c.reconnectHooks)
		c.mu.RUnlock()
		for _, h := range hooks {
			h()
		}
		go c.watchClose(url)
		return
	}
}

// ErrNotConnected is returned when an operation is attempted before Dial succeeds.
var ErrNotConnected = fmt.Errorf("reactive-commons: not connected to broker")

// DialContext establishes the connection and returns when ready or ctx is done.
func (c *Connection) DialContext(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- c.Dial() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
