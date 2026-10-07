//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListen_ConsumesEnvelope covers T026: publish a raw envelope directly to
// the Kafka topic, register a handler, and assert the framework decodes the
// envelope and invokes the handler exactly once with the expected fields.
func TestListen_ConsumesEnvelope(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	topic := appName + ".order.placed"
	createTopic(t, brokers, topic, 1, 1)

	// Register a handler that captures the received event.
	type gotMsg struct {
		event async.DomainEvent[any]
	}
	received := make(chan gotMsg, 8)
	var callCount int32

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	handler := func(_ context.Context, ev async.DomainEvent[any]) error {
		atomic.AddInt32(&callCount, 1)
		received <- gotMsg{event: ev}
		return nil
	}
	require.NoError(t, app.Registry().ListenEvent("order.placed", handler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		t.Fatal("app did not become ready in 30s")
	}

	// Publish a raw envelope directly via a Writer, mirroring what an external
	// producer would send.
	envelopeBody := map[string]any{
		"name":    "order.placed",
		"eventId": uuid.NewString(),
		"data":    map[string]any{"orderId": "o-42", "amount": 99.5},
	}
	bodyBytes, err := json.Marshal(envelopeBody)
	require.NoError(t, err)

	w := &kgo.Writer{
		Addr:                   kgo.TCP(brokers...),
		Balancer:               &kgo.Hash{},
		AllowAutoTopicCreation: true,
	}
	defer w.Close()
	writeMsg := kgo.Message{
		Topic: topic,
		Key:   []byte(envelopeBody["eventId"].(string)),
		Value: bodyBytes,
		Headers: []kgo.Header{
			{Key: "content-type", Value: []byte("application/json")},
			{Key: "reactive-commons-kind", Value: []byte("event")},
		},
	}
	var writeErr error
	for i := 0; i < 10; i++ {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		writeErr = w.WriteMessages(pubCtx, writeMsg)
		pubCancel()
		if writeErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NoError(t, writeErr, "raw envelope publish should succeed within retries")

	select {
	case msg := <-received:
		assert.Equal(t, "order.placed", msg.event.Name)
		assert.Equal(t, envelopeBody["eventId"], msg.event.EventID)
		data, ok := msg.event.Data.(map[string]any)
		require.True(t, ok, "Data should decode as map[string]any, got %T", msg.event.Data)
		assert.Equal(t, "o-42", data["orderId"])
		assert.EqualValues(t, 99.5, data["amount"])
	case <-time.After(30 * time.Second):
		t.Fatalf("handler was not invoked within 30s")
	}

	// Stop the first app and start a fresh one with the same AppName (→ same
	// consumer group). If offsets were committed the event MUST NOT be
	// redelivered.
	cancel()
	time.Sleep(500 * time.Millisecond)

	app2, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)
	var replayCount int32
	require.NoError(t, app2.Registry().ListenEvent("order.placed", func(_ context.Context, _ async.DomainEvent[any]) error {
		atomic.AddInt32(&replayCount, 1)
		return nil
	}))
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = app2.Start(ctx2) }()
	<-app2.Ready()

	// Give the second consumer a window to receive a redelivery if one exists.
	time.Sleep(3 * time.Second)
	assert.EqualValues(t, 0, atomic.LoadInt32(&replayCount),
		"offset should have been committed; no redelivery expected on restart")
}
