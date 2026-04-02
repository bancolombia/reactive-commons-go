package async

import "encoding/json"

// DomainEvent is a named, immutable record of something that happened.
// Wire JSON: {"name":"...","eventId":"...","data":{...}}
type DomainEvent[T any] struct {
	Name    string `json:"name"`
	EventID string `json:"eventId"`
	Data    T      `json:"data"`
}

// Command is a point-to-point instruction sent to a named target service.
// Wire JSON: {"name":"...","commandId":"...","data":{...}}
type Command[T any] struct {
	Name      string `json:"name"`
	CommandID string `json:"commandId"`
	Data      T      `json:"data"`
}

// AsyncQuery is a request/reply message. The resource identifies the query type.
// Wire JSON: {"resource":"...","queryData":{...}}
type AsyncQuery[T any] struct {
	Resource  string `json:"resource"`
	QueryData T      `json:"queryData"`
}

// Notification is a non-durable broadcast message shared with all running instances.
// Wire JSON: {"name":"...","eventId":"...","data":{...}}
type Notification[T any] struct {
	Name    string `json:"name"`
	EventID string `json:"eventId"`
	Data    T      `json:"data"`
}

// From holds reply-routing metadata extracted from an incoming query message.
type From struct {
	CorrelationID string // x-correlation-id header value
	ReplyID       string // x-reply_id header value (reply queue routing key)
}

// RawEnvelope is used internally for two-phase JSON deserialization.
// The Data/QueryData fields are deferred until the handler type is known.
type RawEnvelope struct {
	Name      string          `json:"name,omitempty"`
	EventID   string          `json:"eventId,omitempty"`
	CommandID string          `json:"commandId,omitempty"`
	Resource  string          `json:"resource,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	QueryData json.RawMessage `json:"queryData,omitempty"`
}
