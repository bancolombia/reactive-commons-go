package unit_test

import (
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/rabbit"
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
