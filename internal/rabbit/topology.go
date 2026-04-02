package rabbit

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Topology declares all AMQP exchanges and queues required by reactive-commons.
type Topology struct {
	cfg Config
	ch  *amqp.Channel
}

// NewTopology creates a Topology that uses ch for all declarations.
func NewTopology(cfg Config, ch *amqp.Channel) *Topology {
	return &Topology{cfg: cfg, ch: ch}
}

// DeclareExchanges declares the three standard exchanges.
func (t *Topology) DeclareExchanges() error {
	exchanges := []struct {
		name    string
		kind    string
		durable bool
	}{
		{t.cfg.DomainEventsExchange, "topic", true},
		{t.cfg.DirectMessagesExchange, "direct", true},
		{t.cfg.GlobalReplyExchange, "direct", false},
	}
	for _, ex := range exchanges {
		if err := t.ch.ExchangeDeclare(ex.name, ex.kind, ex.durable, false, false, false, nil); err != nil {
			return fmt.Errorf("reactive-commons: declare exchange %q: %w", ex.name, err)
		}
	}
	return nil
}

// DeclareEventsQueue declares {appName}.subsEvents and returns the queue name.
func (t *Topology) DeclareEventsQueue() (string, error) {
	name := t.cfg.AppName + ".subsEvents"
	args := t.dlqArgs(t.cfg.DomainEventsExchange)
	_, err := t.ch.QueueDeclare(name, true, false, false, false, args)
	return name, wrapQueueErr(name, err)
}

// DeclareCommandsQueue declares {appName} for commands and returns the queue name.
func (t *Topology) DeclareCommandsQueue() (string, error) {
	name := t.cfg.AppName
	args := t.dlqArgs(t.cfg.DirectMessagesExchange)
	_, err := t.ch.QueueDeclare(name, true, false, false, false, args)
	return name, wrapQueueErr(name, err)
}

// DeclareQueriesQueue declares {appName}.query and returns the queue name.
func (t *Topology) DeclareQueriesQueue() (string, error) {
	name := t.cfg.AppName + ".query"
	_, err := t.ch.QueueDeclare(name, true, false, false, false, nil)
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

// DeclareDLQ declares a dead-letter exchange and queue for the given source queue.
// The DLQ exchange name follows the convention "{sourceExchange}.DLQ".
func (t *Topology) DeclareDLQ(sourceQueue, sourceExchange string) error {
	dlqExchange := sourceExchange + ".DLQ"
	dlqQueue := sourceQueue + ".DLQ"

	if err := t.ch.ExchangeDeclare(dlqExchange, "direct", true, false, false, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: declare DLQ exchange %q: %w", dlqExchange, err)
	}
	if _, err := t.ch.QueueDeclare(dlqQueue, true, false, false, false, nil); err != nil {
		return fmt.Errorf("reactive-commons: declare DLQ queue %q: %w", dlqQueue, err)
	}
	return t.ch.QueueBind(dlqQueue, sourceQueue, dlqExchange, false, nil)
}

// dlqArgs returns queue arguments that configure dead-lettering if WithDLQRetry is enabled.
func (t *Topology) dlqArgs(exchange string) amqp.Table {
	if !t.cfg.WithDLQRetry {
		return nil
	}
	dlqExchange := exchange + ".DLQ"
	return amqp.Table{
		"x-dead-letter-exchange": dlqExchange,
	}
}

func wrapQueueErr(name string, err error) error {
	if err != nil {
		return fmt.Errorf("reactive-commons: declare queue %q: %w", name, err)
	}
	return nil
}
