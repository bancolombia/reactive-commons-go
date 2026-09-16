//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
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

// TestListen_RetryAndDLQ covers T027: a handler that always errors triggers
// exactly MaxRetryAttempts invocations, then the original envelope is routed
// to <topic>.dlq with the required x-dlq-* headers.
func TestListen_RetryAndDLQ(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	eventName := "order.failed"
	topic := appName + "." + eventName
	dlqTopic := topic + ".dlq"

	createTopic(t, brokers, topic, 1, 1)
	createTopic(t, brokers, dlqTopic, 1, 1)

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	// Keep retries fast for the test.
	cfg.MaxRetryAttempts = 3
	cfg.RetryInitialDelay = 50 * time.Millisecond
	cfg.RetryMaxDelay = 200 * time.Millisecond
	cfg.HandlerTimeout = 2 * time.Second

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	var callCount int32
	require.NoError(t, app.Registry().ListenEvent(eventName, func(_ context.Context, _ async.DomainEvent[any]) error {
		atomic.AddInt32(&callCount, 1)
		return errors.New("intentional failure")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	body := map[string]any{
		"name":    eventName,
		"eventId": uuid.NewString(),
		"data":    map[string]any{"orderId": "o-99"},
	}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	w := &kgo.Writer{Addr: kgo.TCP(brokers...), Balancer: &kgo.Hash{}}
	defer w.Close()
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pubCancel()
	require.NoError(t, w.WriteMessages(pubCtx, kgo.Message{
		Topic: topic,
		Key:   []byte(body["eventId"].(string)),
		Value: bodyBytes,
	}))

	dlqReader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       dlqTopic,
		StartOffset: kgo.FirstOffset,
		MaxWait:     2 * time.Second,
	})
	defer dlqReader.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer readCancel()
	dlqMsg, err := dlqReader.ReadMessage(readCtx)
	require.NoError(t, err, "DLQ message should arrive within 60s")

	assert.EqualValues(t, cfg.MaxRetryAttempts, atomic.LoadInt32(&callCount),
		"handler should be invoked exactly MaxRetryAttempts times")

	assert.Equal(t, bodyBytes, dlqMsg.Value, "DLQ payload must equal the original envelope bytes")

	getHeader := func(k string) string {
		for _, h := range dlqMsg.Headers {
			if h.Key == k {
				return string(h.Value)
			}
		}
		return ""
	}
	assert.NotEmpty(t, getHeader("x-dlq-reason"), "x-dlq-reason header required")
	assert.Equal(t, "3", getHeader("x-dlq-attempts"))
	assert.Equal(t, topic, getHeader("x-dlq-origin-topic"))
	assert.Equal(t, appName, getHeader("x-dlq-source-service"))
	firstSeen := getHeader("x-dlq-first-seen")
	require.NotEmpty(t, firstSeen, "x-dlq-first-seen header required")
	_, err = time.Parse(time.RFC3339, firstSeen)
	assert.NoError(t, err, "x-dlq-first-seen must be RFC3339")
}
