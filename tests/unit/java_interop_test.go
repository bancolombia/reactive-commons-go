package unit_test

import (
	"encoding/json"
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/rabbit"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Java-produced fixture bytes from contracts/wire-format.md.
// These exact bytes represent what reactive-commons-java publishes to the broker.
const (
	javaInteropEventFixture = `{"name":"order.created","eventId":"d290f1ee-6c54-4b01-90e6-d701748f0851","data":{"orderId":"42","amount":100}}`
	javaInteropCmdFixture   = `{"name":"send-invoice","commandId":"a3bb189e-8bf9-3888-9912-ace4e6543002","data":{"invoiceId":"INV-001"}}`
	javaInteropQueryFixture = `{"resource":"get-product","queryData":{"productId":"SKU-999"}}`
	// Notification shares the same wire format as DomainEvent.
	javaInteropNotifFixture = `{"name":"cache-invalidated","eventId":"f47ac10b-58cc-4372-a567-0e02b2c3d479","data":{"region":"us-east-1"}}`
)

// T052 — Deserialize a Java-produced DomainEvent.
func TestJavaInterop_DomainEvent_Deserialize(t *testing.T) {
	type orderPayload struct {
		OrderID string `json:"orderId"`
		Amount  int    `json:"amount"`
	}

	event, err := rabbit.ReadDomainEvent[orderPayload]([]byte(javaInteropEventFixture))
	require.NoError(t, err)

	assert.Equal(t, "order.created", event.Name)
	assert.Equal(t, "d290f1ee-6c54-4b01-90e6-d701748f0851", event.EventID)
	assert.Equal(t, "42", event.Data.OrderID)
	assert.Equal(t, 100, event.Data.Amount)
}

// T053 — Deserialize a Java-produced Command.
func TestJavaInterop_Command_Deserialize(t *testing.T) {
	type invoicePayload struct {
		InvoiceID string `json:"invoiceId"`
	}

	cmd, err := rabbit.ReadCommand[invoicePayload]([]byte(javaInteropCmdFixture))
	require.NoError(t, err)

	assert.Equal(t, "send-invoice", cmd.Name)
	assert.Equal(t, "a3bb189e-8bf9-3888-9912-ace4e6543002", cmd.CommandID)
	assert.Equal(t, "INV-001", cmd.Data.InvoiceID)
}

// T054 — Deserialize a Java-produced Query.
func TestJavaInterop_Query_Deserialize(t *testing.T) {
	type productQuery struct {
		ProductID string `json:"productId"`
	}

	q, err := rabbit.ReadQuery[productQuery]([]byte(javaInteropQueryFixture))
	require.NoError(t, err)

	assert.Equal(t, "get-product", q.Resource)
	assert.Equal(t, "SKU-999", q.QueryData.ProductID)
}

// T055 — Go-serialized DomainEvent must produce Java-compatible JSON field names.
// Verifies: "name", "eventId" (not "event_id"), "data" (not "payload").
func TestJavaInterop_DomainEvent_SerializedFieldNames(t *testing.T) {
	event := async.DomainEvent[any]{
		Name:    "order.created",
		EventID: "d290f1ee-6c54-4b01-90e6-d701748f0851",
		Data:    map[string]any{"orderId": "42", "amount": float64(100)},
	}

	b, err := rabbit.ToMessage(event)
	require.NoError(t, err)

	// Parse into raw map to verify exact field names.
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &raw))

	assert.Contains(t, raw, "name", "must have 'name' field")
	assert.Contains(t, raw, "eventId", "must have 'eventId' (not 'event_id')")
	assert.Contains(t, raw, "data", "must have 'data' (not 'payload')")
	assert.NotContains(t, raw, "event_id", "must NOT have Java-incompatible 'event_id'")
	assert.NotContains(t, raw, "payload", "must NOT have Java-incompatible 'payload'")

	assert.JSONEq(t, javaInteropEventFixture, string(b))
}

// T056 — Go-serialized Command must produce Java-compatible JSON field names.
// Verifies: "name", "commandId" (not "command_id"), "data" (not "body").
func TestJavaInterop_Command_SerializedFieldNames(t *testing.T) {
	cmd := async.Command[any]{
		Name:      "send-invoice",
		CommandID: "a3bb189e-8bf9-3888-9912-ace4e6543002",
		Data:      map[string]any{"invoiceId": "INV-001"},
	}

	b, err := rabbit.ToMessage(cmd)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &raw))

	assert.Contains(t, raw, "name")
	assert.Contains(t, raw, "commandId", "must have 'commandId' (not 'command_id')")
	assert.Contains(t, raw, "data", "must have 'data' (not 'body')")
	assert.NotContains(t, raw, "command_id")
	assert.NotContains(t, raw, "body")

	assert.JSONEq(t, javaInteropCmdFixture, string(b))
}

// T057 — Go-serialized Query must produce Java-compatible JSON field names.
// Verifies: "resource" (not "name"), "queryData" (not "query_data").
func TestJavaInterop_Query_SerializedFieldNames(t *testing.T) {
	q := async.AsyncQuery[any]{
		Resource:  "get-product",
		QueryData: map[string]any{"productId": "SKU-999"},
	}

	b, err := rabbit.ToMessage(q)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &raw))

	assert.Contains(t, raw, "resource", "must have 'resource' (not 'name')")
	assert.Contains(t, raw, "queryData", "must have 'queryData' (not 'query_data')")
	assert.NotContains(t, raw, "name")
	assert.NotContains(t, raw, "query_data")

	assert.JSONEq(t, javaInteropQueryFixture, string(b))
}

// T058 — Notification uses same wire format as DomainEvent.
func TestJavaInterop_Notification_Deserialize(t *testing.T) {
	type cachePayload struct {
		Region string `json:"region"`
	}

	n, err := rabbit.ReadNotification[cachePayload]([]byte(javaInteropNotifFixture))
	require.NoError(t, err)

	assert.Equal(t, "cache-invalidated", n.Name)
	assert.Equal(t, "f47ac10b-58cc-4372-a567-0e02b2c3d479", n.EventID)
	assert.Equal(t, "us-east-1", n.Data.Region)
}

// T058b — Go-serialized Notification field names match Java wire format.
func TestJavaInterop_Notification_SerializedFieldNames(t *testing.T) {
	n := async.Notification[any]{
		Name:    "cache-invalidated",
		EventID: "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		Data:    map[string]any{"region": "us-east-1"},
	}

	b, err := rabbit.ToMessage(n)
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(b, &raw))

	assert.Contains(t, raw, "name")
	assert.Contains(t, raw, "eventId", "Notification must use 'eventId' (same as DomainEvent)")
	assert.Contains(t, raw, "data")
	assert.NotContains(t, raw, "event_id")

	assert.JSONEq(t, javaInteropNotifFixture, string(b))
}
