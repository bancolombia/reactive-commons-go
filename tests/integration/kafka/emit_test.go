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

type userCreatedPayload struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

type runningApp struct {
	app     *rckafka.Application
	appName string
	cancel  context.CancelFunc
}

func newTestApp(t *testing.T, brokers []string) *runningApp {
	t.Helper()
	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

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

// TestEmit_PublishesEnvelope covers T019: the framework publishes exactly one
// message with the envelope body, key = eventId, and required headers.
func TestEmit_PublishesEnvelope(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newTestApp(t, brokers)
	defer ra.cancel()
	topic := ra.appName + ".user.created"
	createTopic(t, brokers, topic, 1, 1)

	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    userCreatedPayload{UserID: "u-42", Email: "u42@example.com"},
	}
	emitCtx, emitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer emitCancel()
	require.NoError(t, ra.app.EventBus().Emit(emitCtx, ev))

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

	assert.Equal(t, ev.EventID, string(msg.Key))

	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &body))
	assert.Equal(t, "user.created", body["name"])
	assert.Equal(t, ev.EventID, body["eventId"])
	assert.NotNil(t, body["data"])

	assert.Equal(t, "application/json", headerValue(msg.Headers, "content-type"))
	assert.Equal(t, "event", headerValue(msg.Headers, "reactive-commons-kind"))
	assert.Equal(t, "v1", headerValue(msg.Headers, "reactive-commons-envelope"))
}

// TestEmit_ContextCancellation covers T020 case 1.
func TestEmit_ContextCancellation(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newTestApp(t, brokers)
	defer ra.cancel()

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()

	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    userCreatedPayload{UserID: "u-42"},
	}
	err := ra.app.EventBus().Emit(ctx, ev)
	require.Error(t, err)
	assert.True(t,
		errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "context"),
		"expected wrapped context error, got: %v", err)
}

// TestEmit_OversizedPayload covers T020 case 2: pre-flight size check returns
// ErrPayloadTooLarge before the message ever reaches the broker.
func TestEmit_OversizedPayload(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	cfg.MaxMessageBytes = 128

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	big := strings.Repeat("x", 4096)
	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    map[string]any{"blob": big},
	}
	err = app.EventBus().Emit(context.Background(), ev)
	require.Error(t, err)
	assert.True(t, errors.Is(err, rckafka.ErrPayloadTooLarge), "expected ErrPayloadTooLarge, got %v", err)
}

// TestEmit_AutoGeneratesEventID covers T020 case 3.
func TestEmit_AutoGeneratesEventID(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	ra := newTestApp(t, brokers)
	defer ra.cancel()
	topic := ra.appName + ".user.created"
	createTopic(t, brokers, topic, 1, 1)

	ev := async.DomainEvent[any]{
		Name: "user.created",
		// EventID intentionally empty.
		Data: userCreatedPayload{UserID: "u-99"},
	}
	require.NoError(t, ra.app.EventBus().Emit(context.Background(), ev))

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

func headerValue(hs []kgo.Header, k string) string {
	for _, h := range hs {
		if h.Key == k {
			return string(h.Value)
		}
	}
	return ""
}
