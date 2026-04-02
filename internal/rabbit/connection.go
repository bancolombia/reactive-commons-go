package rabbit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const publisherPoolSize = 4

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

	conn, err := amqp.Dial(url)
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
func (c *Connection) watchClose(url string) {
	for {
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
}

func (c *Connection) reconnectLoop(url string) {
	backoff := 1 * time.Second
	const maxBackoff = 30 * time.Second
	for {
		c.log.Info("reactive-commons: attempting reconnect", "delay", backoff)
		time.Sleep(backoff)

		conn, err := amqp.Dial(url)
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
