package unit_test

import (
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/rabbit"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
)

// Topology DLQ/retry exchange names MUST match reactive-commons-java exactly so
// that a Go and a Java app can share the same broker without colliding.
//
// Java references (master):
// - ApplicationEventListener.java   →  retry: "{appName}.{eventsExchange}"
//                                       DLQ:   "{appName}.{eventsExchange}.DLQ"
// - ApplicationCommandListener.java →  DLQ:   "{directExchange}.DLQ"
// - ApplicationQueryListener.java   →  DLQ:   "{directExchange}.DLQ"
func TestTopology_DLQNamesMatchJava(t *testing.T) {
	cfg := rabbit.Config{
		AppName:                "order-service",
		DomainEventsExchange:   "domainEvents",
		DirectMessagesExchange: "directMessages",
		GlobalReplyExchange:    "globalReply",
	}
	topo := rabbit.NewTopology(cfg, nil)

	assert.Equal(t, "order-service.domainEvents", topo.EventsRetryExchange(),
		"events retry exchange must be {appName}.{eventsExchange} (Java parity)")
	assert.Equal(t, "order-service.domainEvents.DLQ", topo.EventsDLQExchange(),
		"events DLQ exchange must be {appName}.{eventsExchange}.DLQ (Java parity)")
	assert.Equal(t, "directMessages.DLQ", topo.DirectDLQExchange(),
		"direct DLQ exchange must be {directExchange}.DLQ (Java parity)")
}

// QueueArgs must include x-queue-type whenever QueueType is set, must merge
// caller-supplied entries on top, and must return nil when there is nothing
// to declare (empty type and no extras).
func TestTopology_QueueArgs(t *testing.T) {
	t.Run("returns nil when no type and no extras", func(t *testing.T) {
		topo := rabbit.NewTopology(rabbit.Config{}, nil)
		assert.Nil(t, topo.QueueArgs(nil))
	})

	t.Run("includes x-queue-type alone when no extras", func(t *testing.T) {
		topo := rabbit.NewTopology(rabbit.Config{QueueType: "quorum"}, nil)
		args := topo.QueueArgs(nil)
		assert.Equal(t, "quorum", args["x-queue-type"])
		assert.Len(t, args, 1)
	})

	t.Run("merges extras with x-queue-type", func(t *testing.T) {
		topo := rabbit.NewTopology(rabbit.Config{QueueType: "quorum"}, nil)
		args := topo.QueueArgs(amqp.Table{
			"x-dead-letter-exchange": "foo.DLQ",
			"x-message-ttl":          int32(1000),
		})
		assert.Equal(t, "quorum", args["x-queue-type"])
		assert.Equal(t, "foo.DLQ", args["x-dead-letter-exchange"])
		assert.Equal(t, int32(1000), args["x-message-ttl"])
	})

	t.Run("returns extras unchanged when no type", func(t *testing.T) {
		topo := rabbit.NewTopology(rabbit.Config{}, nil)
		args := topo.QueueArgs(amqp.Table{"x-dead-letter-exchange": "foo.DLQ"})
		assert.Equal(t, "foo.DLQ", args["x-dead-letter-exchange"])
		_, hasType := args["x-queue-type"]
		assert.False(t, hasType)
	})
}
