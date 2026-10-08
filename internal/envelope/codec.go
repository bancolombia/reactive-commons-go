package envelope

import (
	"encoding/json"
	"fmt"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// Marshal serializes v to the reactive-commons wire envelope JSON. v MUST be
// an async.DomainEvent[T] or async.Notification[T]. Field names on the wire
// (`name`, `eventId`, `data`) come from the struct tags in pkg/async/types.go
// so the output is structurally interchangeable with the RabbitMQ path.
func Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("kafka: envelope marshal: %w", err)
	}
	return b, nil
}

// UnmarshalRaw performs the first-phase decode of a wire envelope into
// async.RawEnvelope, keeping `data` as json.RawMessage so the handler-side
// second phase can decode into the concrete type. Returns an error when the
// bytes are not JSON or the envelope carries neither a `name` (events,
// notifications, commands) nor a `resource` (queries). Callers enforce the
// field required by their pattern.
func UnmarshalRaw(b []byte) (async.RawEnvelope, error) {
	var env async.RawEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return async.RawEnvelope{}, fmt.Errorf("kafka: envelope decode: %w", err)
	}
	if env.Name == "" && env.Resource == "" {
		return async.RawEnvelope{}, fmt.Errorf("kafka: envelope decode: missing required fields %q and %q", "name", "resource")
	}
	return env, nil
}

// DecodeData performs the second-phase decode of a RawEnvelope's `data` field
// into the concrete handler payload type T.
func DecodeData[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("kafka: envelope data decode: %w", err)
	}
	return out, nil
}
