//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

// TestCommands_DeliveredToTarget: SendCommand on the caller reaches the
// target's registered handler with name, commandId, and raw payload intact.
func TestCommands_DeliveredToTarget(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	targetName := newAppName()
	received := make(chan async.Command[any], 1)
	target := startAppWith(t, brokers, targetName, nil, func(app *rckafka.Application) error {
		return app.Registry().ListenCommand("orders.create", func(_ context.Context, cmd async.Command[any]) error {
			received <- cmd
			return nil
		})
	})
	defer target.cancel()

	caller := startAppWith(t, brokers, newAppName(), nil, nil)
	defer caller.cancel()

	cmd := async.Command[any]{
		Name:      "orders.create",
		CommandID: uuid.NewString(),
		Data:      json.RawMessage(`{"orderId":"o-1"}`),
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, caller.app.Gateway().SendCommand(sendCtx, cmd, targetName))

	select {
	case got := <-received:
		assert.Equal(t, cmd.Name, got.Name)
		assert.Equal(t, cmd.CommandID, got.CommandID)
		raw, ok := got.Data.(json.RawMessage)
		require.True(t, ok, "Data should be json.RawMessage, got %T", got.Data)
		assert.JSONEq(t, `{"orderId":"o-1"}`, string(raw))
	case <-time.After(20 * time.Second):
		t.Fatal("command was not delivered to the target within 20s")
	}
}

// TestSendCommand_PublishesEnvelope: wire-level check of the command
// message: topic, key = commandId, envelope body, and headers.
func TestSendCommand_PublishesEnvelope(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	targetName := newAppName()
	topic := targetName + ".commands"
	createTopic(t, brokers, topic, 1, 1)

	caller := startAppWith(t, brokers, newAppName(), nil, nil)
	defer caller.cancel()

	cmd := async.Command[any]{
		Name:      "orders.create",
		CommandID: uuid.NewString(),
		Data:      json.RawMessage(`{"orderId":"o-2"}`),
	}
	require.NoError(t, caller.app.Gateway().SendCommand(context.Background(), cmd, targetName))

	reader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       topic,
		StartOffset: kgo.FirstOffset,
		MaxWait:     2 * time.Second,
	})
	defer reader.Close()

	readCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err)

	assert.Equal(t, cmd.CommandID, string(msg.Key))
	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &body))
	assert.Equal(t, "orders.create", body["name"])
	assert.Equal(t, cmd.CommandID, body["commandId"])
	assert.NotNil(t, body["data"])
	assert.Equal(t, "application/json", headerValue(msg.Headers, "content-type"))
	assert.Equal(t, "command", headerValue(msg.Headers, "reactive-commons-kind"))
	assert.Equal(t, "v1", headerValue(msg.Headers, "reactive-commons-envelope"))
	assert.Equal(t, caller.appName, headerValue(msg.Headers, "sourceApplication"))
}

// TestCommands_UnknownNameDiscarded: a command with no registered handler is
// consumed and committed (not retried, not DLQ'd), so the next command on the
// same partition still arrives; the DLQ stays empty.
func TestCommands_UnknownNameDiscarded(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	targetName := newAppName()
	received := make(chan async.Command[any], 1)
	target := startAppWith(t, brokers, targetName, nil, func(app *rckafka.Application) error {
		return app.Registry().ListenCommand("known.cmd", func(_ context.Context, cmd async.Command[any]) error {
			received <- cmd
			return nil
		})
	})
	defer target.cancel()

	caller := startAppWith(t, brokers, newAppName(), nil, nil)
	defer caller.cancel()

	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, caller.app.Gateway().SendCommand(sendCtx, async.Command[any]{
		Name:      "unknown.cmd",
		CommandID: uuid.NewString(),
		Data:      json.RawMessage(`{}`),
	}, targetName))

	knownID := uuid.NewString()
	require.NoError(t, caller.app.Gateway().SendCommand(sendCtx, async.Command[any]{
		Name:      "known.cmd",
		CommandID: knownID,
		Data:      json.RawMessage(`{}`),
	}, targetName))

	select {
	case got := <-received:
		assert.Equal(t, knownID, got.CommandID)
	case <-time.After(20 * time.Second):
		t.Fatal("known command was not delivered; unknown command may have blocked the consumer")
	}

	// The DLQ must stay empty: the unknown command was discarded, not failed.
	deadline := time.Now().Add(2 * time.Second)
	reader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       targetName + ".commands.dlq",
		StartOffset: kgo.FirstOffset,
		MaxWait:     500 * time.Millisecond,
	})
	defer reader.Close()
	readCtx, readCancel := context.WithDeadline(context.Background(), deadline)
	defer readCancel()
	_, err := reader.ReadMessage(readCtx)
	require.Error(t, err, "DLQ should be empty, but a message arrived")
}

// TestCommands_HandlerErrorRetriesThenDLQ: a failing handler is retried
// MaxRetryAttempts times, then the message lands on {app}.commands.dlq with
// the standard x-dlq-* headers.
func TestCommands_HandlerErrorRetriesThenDLQ(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	targetName := newAppName()
	var calls int32
	target := startAppWith(t, brokers, targetName, func(cfg *rckafka.KafkaConfig) {
		cfg.MaxRetryAttempts = 3
		cfg.RetryInitialDelay = 50 * time.Millisecond
		cfg.RetryMaxDelay = 100 * time.Millisecond
	}, func(app *rckafka.Application) error {
		return app.Registry().ListenCommand("flaky.cmd", func(_ context.Context, _ async.Command[any]) error {
			atomic.AddInt32(&calls, 1)
			return errors.New("boom")
		})
	})
	defer target.cancel()

	caller := startAppWith(t, brokers, newAppName(), nil, nil)
	defer caller.cancel()

	cmd := async.Command[any]{
		Name:      "flaky.cmd",
		CommandID: uuid.NewString(),
		Data:      json.RawMessage(`{"n":1}`),
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, caller.app.Gateway().SendCommand(sendCtx, cmd, targetName))

	reader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       targetName + ".commands.dlq",
		StartOffset: kgo.FirstOffset,
		MaxWait:     2 * time.Second,
	})
	defer reader.Close()

	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()
	msg, err := reader.ReadMessage(readCtx)
	require.NoError(t, err, "expected the failed command on the DLQ")

	assert.Equal(t, cmd.CommandID, string(msg.Key))
	assert.Equal(t, "3", headerValue(msg.Headers, "x-dlq-attempts"))
	assert.Equal(t, targetName+".commands", headerValue(msg.Headers, "x-dlq-origin-topic"))
	assert.Equal(t, targetName, headerValue(msg.Headers, "x-dlq-source-service"))
	assert.Contains(t, headerValue(msg.Headers, "x-dlq-reason"), "boom")

	var body map[string]any
	require.NoError(t, json.Unmarshal(msg.Value, &body))
	assert.Equal(t, "flaky.cmd", body["name"])

	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "handler should run once per attempt")
}

// TestCommands_CompetingConsumers: two apps sharing an AppName (hence a
// consumer group) process each command exactly once.
func TestCommands_CompetingConsumers(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := newAppName()
	createTopic(t, brokers, appName+".commands", 2, 1)

	makeApp := func(seen chan<- string, wg *sync.WaitGroup) *runningApp {
		return startAppWith(t, brokers, appName, func(cfg *rckafka.KafkaConfig) {
			cfg.DefaultPartitions = 2
		}, func(app *rckafka.Application) error {
			return app.Registry().ListenCommand("orders.create", func(_ context.Context, cmd async.Command[any]) error {
				seen <- cmd.CommandID
				wg.Done()
				return nil
			})
		})
	}

	const total = 10
	var wg sync.WaitGroup
	wg.Add(total)
	seen := make(chan string, total*2)

	app1 := makeApp(seen, &wg)
	defer app1.cancel()
	app2 := makeApp(seen, &wg)
	defer app2.cancel()

	// Give both consumers a moment to join the group before publishing.
	time.Sleep(2 * time.Second)

	caller := startAppWith(t, brokers, newAppName(), nil, nil)
	defer caller.cancel()

	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	published := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := uuid.NewString()
		published[id] = true
		require.NoError(t, caller.app.Gateway().SendCommand(sendCtx, async.Command[any]{
			Name:      "orders.create",
			CommandID: id,
			Data:      json.RawMessage(`{}`),
		}, appName))
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("did not receive all %d commands within 60s", total)
	}

	close(seen)
	got := make(map[string]int)
	for id := range seen {
		got[id]++
	}
	assert.Len(t, got, total, "each commandId should appear exactly once across both consumers")
	for id := range published {
		assert.Equal(t, 1, got[id], "commandId %s delivered %d times, expected 1", id, got[id])
	}
}
