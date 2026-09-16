package kafka_test

import (
	"encoding/json"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type userCreatedCE struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// TestWrapAsCloudEvent_ProducesCloudEventShape covers T056: the helper output,
// once JSON-marshalled, contains every required CloudEvents Core 1.0 field.
func TestWrapAsCloudEvent_ProducesCloudEventShape(t *testing.T) {
	t.Parallel()

	payload := userCreatedCE{UserID: "u-42", Email: "u42@example.com"}
	wrapped := rckafka.WrapAsCloudEvent("billing", "user.created", payload)

	b, err := json.Marshal(wrapped)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))

	assert.Equal(t, "1.0", got["specversion"])
	assert.Equal(t, "billing", got["source"])
	assert.Equal(t, "user.created", got["type"])
	assert.Equal(t, "application/json", got["datacontenttype"])

	// id must be a UUIDv4.
	idStr, ok := got["id"].(string)
	require.True(t, ok, "id must be a string")
	parsedID, err := uuid.Parse(idStr)
	require.NoError(t, err, "id must be a UUID")
	assert.Equal(t, uuid.Version(4), parsedID.Version(), "id must be UUIDv4")

	// time must be RFC3339-parseable.
	tStr, ok := got["time"].(string)
	require.True(t, ok, "time must be a string")
	_, err = time.Parse(time.RFC3339, tStr)
	assert.NoError(t, err, "time must parse as RFC3339")

	// data must round-trip the original struct.
	data, ok := got["data"].(map[string]any)
	require.True(t, ok, "data must be a JSON object")
	assert.Equal(t, "u-42", data["userId"])
	assert.Equal(t, "u42@example.com", data["email"])
}

// TestWrapAsCloudEvent_MarshalsInsideEnvelope covers T056's second clause:
// dropping the wrapped value into DomainEvent[any].Data produces a clean
// nested-JSON envelope with no shape surprises.
func TestWrapAsCloudEvent_MarshalsInsideEnvelope(t *testing.T) {
	t.Parallel()

	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    rckafka.WrapAsCloudEvent("billing", "user.created", userCreatedCE{UserID: "u-1"}),
	}
	b, err := json.Marshal(ev)
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(b, &wire))

	assert.Equal(t, "user.created", wire["name"])
	assert.Equal(t, ev.EventID, wire["eventId"])

	data, ok := wire["data"].(map[string]any)
	require.True(t, ok, "envelope's data must be an object")
	assert.Equal(t, "1.0", data["specversion"])
	assert.Equal(t, "billing", data["source"])
	assert.Equal(t, "user.created", data["type"])
	nested, ok := data["data"].(map[string]any)
	require.True(t, ok, "cloudevent's data must be an object")
	assert.Equal(t, "u-1", nested["userId"])
}

// TestWrapAsCloudEvent_UniqueIDs covers a minor but important property:
// consecutive calls MUST produce distinct ids.
func TestWrapAsCloudEvent_UniqueIDs(t *testing.T) {
	t.Parallel()
	a := rckafka.WrapAsCloudEvent("s", "t", 1).(map[string]any)
	b := rckafka.WrapAsCloudEvent("s", "t", 1).(map[string]any)
	assert.NotEqual(t, a["id"], b["id"])
}
