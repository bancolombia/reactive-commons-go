package kafka

import (
	"time"

	"github.com/google/uuid"
)

// WrapAsCloudEvent returns a JSON-serializable value that follows the
// CloudEvents 1.0 structured-mode shape. It is an OPT-IN helper: the framework
// never invokes it implicitly. Pass the returned value as
// DomainEvent[any].Data (or Notification[any].Data) when your service wants
// downstream consumers to see CloudEvent metadata inside the reactive-commons
// envelope's data field.
//
// The outer wire object is unchanged — still
// {"name": "...", "eventId": "...", "data": <this value>}.
//
// Fields produced (per CloudEvents Core 1.0):
//
//   - specversion: "1.0"
//   - id:          UUIDv4 (new per call)
//   - source:      the source argument
//   - type:        the ceType argument
//   - time:        current time as RFC3339
//   - datacontenttype: "application/json"
//   - data:        the payload verbatim (JSON-marshalled by encoding/json)
//
// The returned value is a map[string]any so it round-trips through
// encoding/json without pulling in the CloudEvents SDK.
func WrapAsCloudEvent(source, ceType string, data any) any {
	return map[string]any{
		"specversion":     "1.0",
		"id":              uuid.NewString(),
		"source":          source,
		"type":            ceType,
		"time":            time.Now().UTC().Format(time.RFC3339),
		"datacontenttype": "application/json",
		"data":            data,
	}
}
