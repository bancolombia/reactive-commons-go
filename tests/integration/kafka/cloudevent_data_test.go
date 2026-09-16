//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ceUserPayload struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

// TestEmit_CloudEventInData covers T057 / quickstart Scenario 7: emitting an
// event whose Data is built via kafka.WrapAsCloudEvent produces a wire object
// of shape {"name":..., "eventId":..., "data":{"specversion":"1.0",...}}.
func TestEmit_CloudEventInData(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	topic := appName + ".user.created"
	createTopic(t, brokers, topic, 1, 1)

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	payload := ceUserPayload{UserID: "u-42", Email: "u42@example.com"}
	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    rckafka.WrapAsCloudEvent("billing", "user.created", payload),
	}
	emitCtx, emitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer emitCancel()
	require.NoError(t, app.EventBus().Emit(emitCtx, ev))

	reader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       topic,
		StartOffset: kgo.FirstOffset,
		MaxWait:     2 * time.Second,
	})
	defer reader.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()
	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err)

	var wire map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &wire))

	assert.Equal(t, "user.created", wire["name"])
	assert.Equal(t, ev.EventID, wire["eventId"])
	data, ok := wire["data"].(map[string]any)
	require.True(t, ok, "envelope.data must be an object")
	assert.Equal(t, "1.0", data["specversion"])
	assert.Equal(t, "billing", data["source"])
	assert.Equal(t, "user.created", data["type"])
	assert.Equal(t, "application/json", data["datacontenttype"])
	assert.NotEmpty(t, data["id"])
	assert.NotEmpty(t, data["time"])

	nested, ok := data["data"].(map[string]any)
	require.True(t, ok, "cloudevent.data must be an object")
	assert.Equal(t, "u-42", nested["userId"])
	assert.Equal(t, "u42@example.com", nested["email"])
}
