package rabbit

import (
	"encoding/json"
	"fmt"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

// ToMessage serializes any value to JSON bytes.
func ToMessage(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
	}
	return b, nil
}

// ReadDomainEvent deserializes raw bytes into a DomainEvent[T].
// Uses two-phase deserialization: first parses the envelope, then the typed payload.
func ReadDomainEvent[T any](body []byte) (async.DomainEvent[T], error) {
	var env async.RawEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return async.DomainEvent[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
	}
	var data T
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return async.DomainEvent[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
		}
	}
	return async.DomainEvent[T]{
		Name:    env.Name,
		EventID: env.EventID,
		Data:    data,
	}, nil
}

// ReadCommand deserializes raw bytes into a Command[T].
func ReadCommand[T any](body []byte) (async.Command[T], error) {
	var env async.RawEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return async.Command[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
	}
	var data T
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return async.Command[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
		}
	}
	return async.Command[T]{
		Name:      env.Name,
		CommandID: env.CommandID,
		Data:      data,
	}, nil
}

// ReadQuery deserializes raw bytes into an AsyncQuery[T].
func ReadQuery[T any](body []byte) (async.AsyncQuery[T], error) {
	var env async.RawEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return async.AsyncQuery[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
	}
	var data T
	if len(env.QueryData) > 0 && string(env.QueryData) != "null" {
		if err := json.Unmarshal(env.QueryData, &data); err != nil {
			return async.AsyncQuery[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
		}
	}
	return async.AsyncQuery[T]{
		Resource:  env.Resource,
		QueryData: data,
	}, nil
}

// ReadNotification deserializes raw bytes into a Notification[T].
func ReadNotification[T any](body []byte) (async.Notification[T], error) {
	var env async.RawEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return async.Notification[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
	}
	var data T
	if len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			return async.Notification[T]{}, fmt.Errorf("%w: %s", async.ErrDeserialize, err)
		}
	}
	return async.Notification[T]{
		Name:    env.Name,
		EventID: env.EventID,
		Data:    data,
	}, nil
}
