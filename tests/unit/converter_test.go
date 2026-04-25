package unit_test

import (
	"testing"

	"github.com/bancolombia/reactive-commons-go/internal/rabbit"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Java fixture bytes from contracts/wire-format.md
const javaEventFixture = `{"name":"order.created","eventId":"d290f1ee-6c54-4b01-90e6-d701748f0851","data":{"orderId":"42","amount":100}}`
const javaCmdFixture = `{"name":"send-invoice","commandId":"a3bb189e-8bf9-3888-9912-ace4e6543002","data":{"invoiceId":"INV-001"}}`
const javaQueryFixture = `{"resource":"get-product","queryData":{"productId":"SKU-999"}}`

type orderPayload struct {
	OrderID string `json:"orderId"`
	Amount  int    `json:"amount"`
}

type invoicePayload struct {
	InvoiceID string `json:"invoiceId"`
}

type productQuery struct {
	ProductID string `json:"productId"`
}

func TestToMessage_SerializesEvent(t *testing.T) {
	event := async.DomainEvent[any]{
		Name:    "order.created",
		EventID: "d290f1ee-6c54-4b01-90e6-d701748f0851",
		Data:    map[string]any{"orderId": "42", "amount": float64(100)},
	}
	b, err := rabbit.ToMessage(event)
	require.NoError(t, err)
	assert.JSONEq(t, javaEventFixture, string(b))
}

func TestToMessage_SerializesCommand(t *testing.T) {
	cmd := async.Command[any]{
		Name:      "send-invoice",
		CommandID: "a3bb189e-8bf9-3888-9912-ace4e6543002",
		Data:      map[string]any{"invoiceId": "INV-001"},
	}
	b, err := rabbit.ToMessage(cmd)
	require.NoError(t, err)
	assert.JSONEq(t, javaCmdFixture, string(b))
}

func TestToMessage_SerializesQuery(t *testing.T) {
	q := async.AsyncQuery[any]{
		Resource:  "get-product",
		QueryData: map[string]any{"productId": "SKU-999"},
	}
	b, err := rabbit.ToMessage(q)
	require.NoError(t, err)
	assert.JSONEq(t, javaQueryFixture, string(b))
}

func TestReadDomainEvent_DeserializesJavaFixture(t *testing.T) {
	event, err := rabbit.ReadDomainEvent[orderPayload]([]byte(javaEventFixture))
	require.NoError(t, err)
	assert.Equal(t, "order.created", event.Name)
	assert.Equal(t, "d290f1ee-6c54-4b01-90e6-d701748f0851", event.EventID)
	assert.Equal(t, "42", event.Data.OrderID)
	assert.Equal(t, 100, event.Data.Amount)
}

func TestReadCommand_DeserializesJavaFixture(t *testing.T) {
	cmd, err := rabbit.ReadCommand[invoicePayload]([]byte(javaCmdFixture))
	require.NoError(t, err)
	assert.Equal(t, "send-invoice", cmd.Name)
	assert.Equal(t, "a3bb189e-8bf9-3888-9912-ace4e6543002", cmd.CommandID)
	assert.Equal(t, "INV-001", cmd.Data.InvoiceID)
}

func TestReadQuery_DeserializesJavaFixture(t *testing.T) {
	q, err := rabbit.ReadQuery[productQuery]([]byte(javaQueryFixture))
	require.NoError(t, err)
	assert.Equal(t, "get-product", q.Resource)
	assert.Equal(t, "SKU-999", q.QueryData.ProductID)
}

func TestReadDomainEvent_RoundTrip(t *testing.T) {
	original := async.DomainEvent[orderPayload]{
		Name:    "test.event",
		EventID: "abc-123",
		Data:    orderPayload{OrderID: "ORD-1", Amount: 50},
	}
	b, err := rabbit.ToMessage(original)
	require.NoError(t, err)

	got, err := rabbit.ReadDomainEvent[orderPayload](b)
	require.NoError(t, err)
	assert.Equal(t, original, got)
}

func TestReadDomainEvent_InvalidJSON(t *testing.T) {
	_, err := rabbit.ReadDomainEvent[orderPayload]([]byte(`not-json`))
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadDomainEvent_NullData_ZeroValue(t *testing.T) {
	event, err := rabbit.ReadDomainEvent[orderPayload]([]byte(`{"name":"x","eventId":"1","data":null}`))
	require.NoError(t, err)
	assert.Equal(t, "x", event.Name)
	assert.Equal(t, orderPayload{}, event.Data)
}

func TestReadDomainEvent_EmptyData_ZeroValue(t *testing.T) {
	event, err := rabbit.ReadDomainEvent[orderPayload]([]byte(`{"name":"x","eventId":"1"}`))
	require.NoError(t, err)
	assert.Equal(t, "x", event.Name)
	assert.Equal(t, orderPayload{}, event.Data)
}

func TestReadDomainEvent_BadInnerJSON(t *testing.T) {
	// data is the wrong shape for orderPayload (Amount expects int).
	body := []byte(`{"name":"x","eventId":"1","data":{"amount":"not-a-number"}}`)
	_, err := rabbit.ReadDomainEvent[orderPayload](body)
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadCommand_InvalidJSON(t *testing.T) {
	_, err := rabbit.ReadCommand[invoicePayload]([]byte(`not-json`))
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadCommand_NullData_ZeroValue(t *testing.T) {
	cmd, err := rabbit.ReadCommand[invoicePayload]([]byte(`{"name":"x","commandId":"1","data":null}`))
	require.NoError(t, err)
	assert.Equal(t, invoicePayload{}, cmd.Data)
}

func TestReadCommand_BadInnerJSON(t *testing.T) {
	body := []byte(`{"name":"x","commandId":"1","data":"not-an-object"}`)
	_, err := rabbit.ReadCommand[invoicePayload](body)
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadQuery_InvalidJSON(t *testing.T) {
	_, err := rabbit.ReadQuery[productQuery]([]byte(`not-json`))
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadQuery_NullData_ZeroValue(t *testing.T) {
	q, err := rabbit.ReadQuery[productQuery]([]byte(`{"resource":"r","queryData":null}`))
	require.NoError(t, err)
	assert.Equal(t, productQuery{}, q.QueryData)
}

func TestReadQuery_BadInnerJSON(t *testing.T) {
	body := []byte(`{"resource":"r","queryData":"oops"}`)
	_, err := rabbit.ReadQuery[productQuery](body)
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadNotification_InvalidJSON(t *testing.T) {
	type cachePayload struct {
		Region string `json:"region"`
	}
	_, err := rabbit.ReadNotification[cachePayload]([]byte(`not-json`))
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

func TestReadNotification_NullData_ZeroValue(t *testing.T) {
	type cachePayload struct {
		Region string `json:"region"`
	}
	n, err := rabbit.ReadNotification[cachePayload]([]byte(`{"name":"n","eventId":"1","data":null}`))
	require.NoError(t, err)
	assert.Equal(t, cachePayload{}, n.Data)
}

func TestReadNotification_BadInnerJSON(t *testing.T) {
	type cachePayload struct {
		Region string `json:"region"`
	}
	body := []byte(`{"name":"n","eventId":"1","data":42}`)
	_, err := rabbit.ReadNotification[cachePayload](body)
	assert.ErrorIs(t, err, async.ErrDeserialize)
}

// ToMessage wraps json.Marshal errors with ErrDeserialize. Channels are
// not JSON-marshalable, so they trigger the error branch.
func TestToMessage_MarshalError_ReturnsErrDeserialize(t *testing.T) {
	bad := async.DomainEvent[any]{
		Name:    "x",
		EventID: "1",
		Data:    make(chan int),
	}
	_, err := rabbit.ToMessage(bad)
	assert.ErrorIs(t, err, async.ErrDeserialize)
}
