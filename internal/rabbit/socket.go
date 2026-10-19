package rabbit

import (
	"net"
	"sync"
	"time"
)

const (
	// amqpHandshakeTimeout mirrors amqp091.DefaultDial's connection timeout.
	// It is used when we install a custom dialer for socket write deadlines.
	amqpHandshakeTimeout = 30 * time.Second

	// closeHandshakeTimeout bounds Connection.Close's AMQP close handshake, so
	// a wedged broker cannot keep Start from returning.
	closeHandshakeTimeout = 5 * time.Second
)

// dialTimeout is a test seam for net.DialTimeout.
var dialTimeout = net.DialTimeout

// writeDeadlineConn bounds every socket write with writeTimeout, while honoring
// any deadline amqp091 installs itself (the handshake deadline and
// CloseDeadline). amqp091's PublishWithContext only checks the context before
// the write; an in-flight write is not context-aware, so without this bound a
// wedged TCP write can hold the connection's send lock indefinitely. When the
// write deadline fires, amqp091 shuts the connection down and the reconnect
// flow recovers.
//
// SetReadDeadline is promoted from the embedded net.Conn and is never touched,
// so amqp091's heartbeat read deadline keeps working unchanged.
type writeDeadlineConn struct {
	net.Conn
	writeTimeout time.Duration

	mu           sync.Mutex
	baseDeadline time.Time
	version      uint64
}

func (c *writeDeadlineConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	deadline := time.Now().Add(c.writeTimeout)
	if !c.baseDeadline.IsZero() && c.baseDeadline.Before(deadline) {
		deadline = c.baseDeadline
	}
	version := c.version
	c.mu.Unlock()

	if err := c.Conn.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)

	c.mu.Lock()
	if c.version == version {
		// No SetDeadline/SetWriteDeadline raced this write: restore the
		// amqp-managed deadline (zero after the handshake completes).
		_ = c.Conn.SetWriteDeadline(c.baseDeadline)
	}
	c.mu.Unlock()
	return n, err
}

func (c *writeDeadlineConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.baseDeadline = t
	c.version++
	c.mu.Unlock()
	return c.Conn.SetDeadline(t)
}

func (c *writeDeadlineConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.baseDeadline = t
	c.version++
	c.mu.Unlock()
	return c.Conn.SetWriteDeadline(t)
}

// handshakeDial returns an amqp.Config.Dial that replicates DefaultDial's
// connect/handshake behavior and then bounds every socket write with
// writeTimeout. amqp091 applies TLS around the returned conn itself, so AMQPS
// keeps working.
func handshakeDial(writeTimeout time.Duration) func(network, addr string) (net.Conn, error) {
	return func(network, addr string) (net.Conn, error) {
		raw, err := dialTimeout(network, addr, amqpHandshakeTimeout)
		if err != nil {
			return nil, err
		}
		conn := &writeDeadlineConn{Conn: raw, writeTimeout: writeTimeout}
		if err := conn.SetDeadline(time.Now().Add(amqpHandshakeTimeout)); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}
}
