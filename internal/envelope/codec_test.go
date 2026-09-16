package envelope

import (
	"encoding/json"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sample struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      async.DomainEvent[any]
		wantErr bool
	}{
		{
			name: "struct data",
			in: async.DomainEvent[any]{
				Name:    "user.created",
				EventID: "id-1",
				Data:    sample{UserID: "u-42", Email: "a@b"},
			},
		},
		{
			name: "nil data",
			in: async.DomainEvent[any]{
				Name:    "user.deleted",
				EventID: "id-2",
				Data:    nil,
			},
		},
		{
			name: "cloudevent-shaped data (opaque to codec)",
			in: async.DomainEvent[any]{
				Name:    "user.created",
				EventID: "id-3",
				Data: map[string]any{
					"specversion":     "1.0",
					"id":              "ce-1",
					"source":          "billing",
					"type":            "user.created",
					"datacontenttype": "application/json",
					"data":            map[string]any{"userId": "u-42"},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := Marshal(tc.in)
			require.NoError(t, err)

			env, err := UnmarshalRaw(b)
			require.NoError(t, err)
			assert.Equal(t, tc.in.Name, env.Name)
			assert.Equal(t, tc.in.EventID, env.EventID)

			if tc.in.Data == nil {
				// json.Marshal writes `"data":null`; UnmarshalRaw preserves that.
				assert.Contains(t, []string{"", "null"}, string(env.Data))
				return
			}

			var back any
			require.NoError(t, json.Unmarshal(env.Data, &back))
			assert.NotNil(t, back)
		})
	}
}

func TestUnmarshalRaw_InvalidJSON(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalRaw([]byte("not-json"))
	assert.Error(t, err)
}

func TestUnmarshalRaw_MissingName(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalRaw([]byte(`{"eventId":"id-1","data":{}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name")
}

func TestDecodeData(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"userId":"u-42","email":"a@b"}`)
	got, err := DecodeData[sample](raw)
	require.NoError(t, err)
	assert.Equal(t, sample{UserID: "u-42", Email: "a@b"}, got)
}
