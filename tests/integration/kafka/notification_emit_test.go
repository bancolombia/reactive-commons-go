//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
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

type cacheInvalidatedPayload struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func newNotifApp(t *testing.T, brokers []string, mutators ...func(*rckafka.KafkaConfig)) *runningApp {
	t.Helper()
	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	for _, m := range mutators {
		m(&cfg)
	}

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Start(ctx) }()
	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatalf("app did not become ready within 30s")
	}
	return &runningApp{app: app, appName: appName, cancel: cancel}
}

// TestEmitNotification_PublishesEnvelope covers T035 primary path: the framework
// publishes exactly one message with the envelope body, key = eventId, and the
// header reactive-commons-kind=notification.
func TestEmitNotification_PublishesEnvelope(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newNotifApp(t, brokers)
	defer ra.cancel()
	topic := ra.appName + ".cache.invalidated"
	createTopic(t, brokers, topic, 1, 1)

	n := async.Notification[any]{
		Name:    "cache.invalidated",
		EventID: uuid.NewString(),
		Data:    cacheInvalidatedPayload{Key: "user:42", Reason: "ttl-expired"},
	}
	emitCtx, emitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer emitCancel()
	require.NoError(t, ra.app.EventBus().EmitNotification(emitCtx, n))

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

	assert.Equal(t, n.EventID, string(msg.Key))

	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &body))
	assert.Equal(t, "cache.invalidated", body["name"])
	assert.Equal(t, n.EventID, body["eventId"])
	assert.NotNil(t, body["data"])

	assert.Equal(t, "application/json", headerValue(msg.Headers, "content-type"))
	assert.Equal(t, "notification", headerValue(msg.Headers, "reactive-commons-kind"))
	assert.Equal(t, "v1", headerValue(msg.Headers, "reactive-commons-envelope"))
}

// TestEmitNotification_ContextCancellation covers T035 cancellation path.
func TestEmitNotification_ContextCancellation(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newNotifApp(t, brokers)
	defer ra.cancel()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()

	n := async.Notification[any]{
		Name:    "cache.invalidated",
		EventID: uuid.NewString(),
		Data:    cacheInvalidatedPayload{Key: "user:99"},
	}
	err := ra.app.EventBus().EmitNotification(ctx, n)
	require.Error(t, err)
	assert.True(t,
		errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context"),
		"expected wrapped context error, got: %v", err)
}

// TestEmitNotification_OversizedPayload covers T035 oversized-payload path.
func TestEmitNotification_OversizedPayload(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newNotifApp(t, brokers, func(cfg *rckafka.KafkaConfig) {
		cfg.MaxMessageBytes = 128
	})
	defer ra.cancel()

	big := strings.Repeat("x", 4096)
	n := async.Notification[any]{
		Name:    "cache.invalidated",
		EventID: uuid.NewString(),
		Data:    map[string]any{"blob": big},
	}
	err := ra.app.EventBus().EmitNotification(context.Background(), n)
	require.Error(t, err)
	assert.True(t, errors.Is(err, rckafka.ErrPayloadTooLarge),
		"expected ErrPayloadTooLarge, got %v", err)
}

// TestEmitNotification_AutoGeneratesEventID covers T035 auto-eventId path.
func TestEmitNotification_AutoGeneratesEventID(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newNotifApp(t, brokers)
	defer ra.cancel()
	topic := ra.appName + ".cache.invalidated"
	createTopic(t, brokers, topic, 1, 1)

	n := async.Notification[any]{
		Name: "cache.invalidated",
		// EventID intentionally empty.
		Data: cacheInvalidatedPayload{Key: "user:1"},
	}
	require.NoError(t, ra.app.EventBus().EmitNotification(context.Background(), n))

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

	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &body))
	assert.NotEmpty(t, body["eventId"], "framework should auto-generate eventId")
	assert.Equal(t, body["eventId"], string(msg.Key))
}
