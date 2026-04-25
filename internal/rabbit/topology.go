package rabbit

import (
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// argDeadLetterExchange is the AMQP queue argument that routes dead-lettered
// messages to the named exchange. See RabbitMQ's DLX documentation.
const argDeadLetterExchange = "x-dead-letter-exchange"

// Topology declares all AMQP exchanges and queues required by reactive-commons.
type Topology struct {
	cfg Config
	ch  *amqp.Channel
}

// NewTopology creates a Topology that uses ch for all declarations.
func NewTopology(cfg Config, ch *amqp.Channel) *Topology {
	return &Topology{cfg: cfg, ch: ch}
}

// DeclareExchanges declares the three standard exchanges with the same
// type/durability as reactive-commons-java so a Go and a Java service can
// safely share the same broker.
func (t *Topology) DeclareExchanges() error {
	exchanges := []struct {
		name    string
		kind    string
		durable bool
	}{
		{t.cfg.DomainEventsExchange, "topic", true},
		{t.cfg.DirectMessagesExchange, "direct", true},
		// globalReply must be topic + durable to match
		// reactive-commons-java's ApplicationReplyListener; otherwise the
		// first app to declare wins and the other fails with
		// PRECONDITION_FAILED.
		{t.cfg.GlobalReplyExchange, "topic", true},
	}
	for _, ex := range exchanges {
		if err := t.ch.ExchangeDeclare(ex.name, ex.kind, ex.durable, false, false, false, nil); err != nil {
			return fmt.Errorf("reactive-commons: declare exchange %q: %w", ex.name, err)
		}
	}
	return nil
}

// EventsDLQExchange returns the per-app events DLQ exchange name. Matches the
// Java naming `{appName}.{eventsExchange}.DLQ`.
func (t *Topology) EventsDLQExchange() string {
	return t.cfg.AppName + "." + t.cfg.DomainEventsExchange + ".DLQ"
}

// EventsRetryExchange returns the per-app events retry exchange name. Matches
// the Java naming `{appName}.{eventsExchange}`.
func (t *Topology) EventsRetryExchange() string {
	return t.cfg.AppName + "." + t.cfg.DomainEventsExchange
}

// DirectDLQExchange returns the shared commands/queries DLQ exchange name.
// Matches the Java naming `{directExchange}.DLQ`.
func (t *Topology) DirectDLQExchange() string {
	return t.cfg.DirectMessagesExchange + ".DLQ"
}

// DeclareEventsQueue declares {appName}.subsEvents and returns the queue name.
// When WithDLQRetry is enabled, the queue dead-letters to the per-app events
// DLQ exchange so failed deliveries flow into the retry loop.
func (t *Topology) DeclareEventsQueue() (string, error) {
	name := t.cfg.AppName + ".subsEvents"
	var args amqp.Table
	if t.cfg.WithDLQRetry {
		args = amqp.Table{argDeadLetterExchange: t.EventsDLQExchange()}
	}
	_, err := t.ch.QueueDeclare(name, true, false, false, false, args)
	return name, wrapQueueErr(name, err)
}

// DeclareCommandsQueue declares {appName} for commands. When WithDLQRetry is
// enabled, the queue dead-letters to the shared direct DLQ exchange.
func (t *Topology) DeclareCommandsQueue() (string, error) {
	name := t.cfg.AppName
	var args amqp.Table
	if t.cfg.WithDLQRetry {
		args = amqp.Table{argDeadLetterExchange: t.DirectDLQExchange()}
	}
	_, err := t.ch.QueueDeclare(name, true, false, false, false, args)
	return name, wrapQueueErr(name, err)
}

// DeclareQueriesQueue declares {appName}.query for queries. When WithDLQRetry
// is enabled, the queue dead-letters to the shared direct DLQ exchange.
func (t *Topology) DeclareQueriesQueue() (string, error) {
	name := t.cfg.AppName + ".query"
	var args amqp.Table
	if t.cfg.WithDLQRetry {
		args = amqp.Table{argDeadLetterExchange: t.DirectDLQExchange()}
	}
	_, err := t.ch.QueueDeclare(name, true, false, false, false, args)
	return name, wrapQueueErr(name, err)
}

// DeclareTempQueue declares a non-durable exclusive auto-delete queue.
func (t *Topology) DeclareTempQueue(name string) (string, error) {
	_, err := t.ch.QueueDeclare(name, false, true, true, false, nil)
	return name, wrapQueueErr(name, err)
}

// BindQueue binds queue to exchange with the given routing key.
func (t *Topology) BindQueue(queue, exchange, routingKey string) error {
	if err := t.ch.QueueBind(queue, routingKey, exchange, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: bind queue %q to %q/%q: %w", queue, exchange, routingKey, err)
	}
	return nil
}

// DeclareEventsDLQRetry declares the topic retry exchange, the topic DLQ
// exchange and the DLQ queue used to redeliver failed events after a delay.
// Mirrors reactive-commons-java's ApplicationEventListener retry topology.
func (t *Topology) DeclareEventsDLQRetry(eventsQueue string) error {
	retryExchange := t.EventsRetryExchange()
	dlqExchange := t.EventsDLQExchange()
	dlqQueue := eventsQueue + ".DLQ"

	if err := t.ch.ExchangeDeclare(retryExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: declare events retry exchange %q: %w", retryExchange, err)
	}
	if err := t.ch.ExchangeDeclare(dlqExchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: declare events DLQ exchange %q: %w", dlqExchange, err)
	}

	dlqArgs := amqp.Table{
		argDeadLetterExchange: retryExchange,
		"x-message-ttl":       int32(t.cfg.RetryDelay / time.Millisecond),
	}
	if _, err := t.ch.QueueDeclare(dlqQueue, true, false, false, false, dlqArgs); err != nil {
		return fmt.Errorf("reactive-commons: declare events DLQ queue %q: %w", dlqQueue, err)
	}

	if err := t.ch.QueueBind(dlqQueue, "#", dlqExchange, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: bind events DLQ queue: %w", err)
	}
	if err := t.ch.QueueBind(eventsQueue, "#", retryExchange, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: bind events queue to retry exchange: %w", err)
	}
	return nil
}

// DeclareDirectDLQRetry declares the shared direct DLQ exchange and a per-queue
// DLQ queue. The DLQ queue uses x-message-ttl to dead-letter back to the
// directMessages exchange after the configured retry delay, where it routes
// back to the origin queue using the original routing key.
//
// originQueue is the consumer queue name (e.g. "{appName}" for commands or
// "{appName}.query" for queries). routingKey is the binding routing key the
// origin queue uses on directMessages — it is also used to bind the DLQ queue
// so dead-lettered messages flow into it.
func (t *Topology) DeclareDirectDLQRetry(originQueue, routingKey string) error {
	dlqExchange := t.DirectDLQExchange()
	dlqQueue := originQueue + ".DLQ"

	if err := t.ch.ExchangeDeclare(dlqExchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: declare direct DLQ exchange %q: %w", dlqExchange, err)
	}

	dlqArgs := amqp.Table{
		argDeadLetterExchange: t.cfg.DirectMessagesExchange,
		"x-message-ttl":       int32(t.cfg.RetryDelay / time.Millisecond),
	}
	if _, err := t.ch.QueueDeclare(dlqQueue, true, false, false, false, dlqArgs); err != nil {
		return fmt.Errorf("reactive-commons: declare direct DLQ queue %q: %w", dlqQueue, err)
	}

	if err := t.ch.QueueBind(dlqQueue, routingKey, dlqExchange, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: bind direct DLQ queue: %w", err)
	}
	return nil
}

func wrapQueueErr(name string, err error) error {
	if err != nil {
		return fmt.Errorf("reactive-commons: declare queue %q: %w", name, err)
	}
	return nil
}
