//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawData returns the query payload as JSON. The framework delivers
// json.RawMessage (rabbit parity); fall back to marshalling for safety.
func rawData(q async.AsyncQuery[any]) json.RawMessage {
	if raw, ok := q.QueryData.(json.RawMessage); ok {
		return raw
	}
	raw, _ := json.Marshal(q.QueryData)
	return raw
}

// TestQueries_RoundTripImmediatelyAfterReady sends the first query as soon as
// the client is Ready (no sleep), exercising the reply-listener priming gate.
func TestQueries_RoundTripImmediatelyAfterReady(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	serverName := newAppName()
	fromCh := make(chan async.From, 1)
	server := startAppWith(t, brokers, serverName, nil, func(app *rckafka.Application) error {
		return app.Registry().ServeQuery("get-product", func(_ context.Context, q async.AsyncQuery[any], from async.From) (any, error) {
			fromCh <- from
			return map[string]any{"productId": "p-1", "echo": rawData(q)}, nil
		})
	})
	defer server.cancel()

	client := startAppWith(t, brokers, newAppName(), nil, nil)
	defer client.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, err := client.app.Gateway().RequestReply(ctx,
		async.AsyncQuery[any]{Resource: "get-product", QueryData: json.RawMessage(`{"id":"p-1"}`)},
		serverName)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))
	assert.Equal(t, "p-1", got["productId"])
	assert.Equal(t, map[string]any{"id": "p-1"}, got["echo"])

	select {
	case from := <-fromCh:
		assert.NotEmpty(t, from.CorrelationID, "handler must receive the correlation id")
		assert.NotEmpty(t, from.ReplyID, "handler must receive the reply topic")
	case <-time.After(5 * time.Second):
		t.Fatal("server handler was not invoked")
	}
}

// TestQueries_NoHandlerTimesOut: a query with no registered handler is
// discarded and the caller sees async.ErrQueryTimeout.
func TestQueries_NoHandlerTimesOut(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	serverName := newAppName()
	// Register an unrelated handler so the server's queries topic exists.
	server := startAppWith(t, brokers, serverName, nil, func(app *rckafka.Application) error {
		return app.Registry().ServeQuery("other-query", func(_ context.Context, _ async.AsyncQuery[any], _ async.From) (any, error) {
			return nil, nil
		})
	})
	defer server.cancel()

	client := startAppWith(t, brokers, newAppName(), nil, nil)
	defer client.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.app.Gateway().RequestReply(ctx,
		async.AsyncQuery[any]{Resource: "missing-query"}, serverName)
	assert.ErrorIs(t, err, async.ErrQueryTimeout)
}

// TestQueries_ConcurrentCorrelated: concurrent queries each receive their own
// reply (reply routing keyed by correlation id).
func TestQueries_ConcurrentCorrelated(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	serverName := newAppName()
	server := startAppWith(t, brokers, serverName, nil, func(app *rckafka.Application) error {
		return app.Registry().ServeQuery("echo", func(_ context.Context, q async.AsyncQuery[any], _ async.From) (any, error) {
			return map[string]any{"echo": rawData(q)}, nil
		})
	})
	defer server.cancel()

	client := startAppWith(t, brokers, newAppName(), nil, nil)
	defer client.cancel()

	const total = 10
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := fmt.Sprintf(`{"n":%d}`, i)
			body, err := client.app.Gateway().RequestReply(ctx,
				async.AsyncQuery[any]{Resource: "echo", QueryData: json.RawMessage(payload)},
				serverName)
			if err != nil {
				errs <- fmt.Errorf("query %d: %w", i, err)
				return
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(body, &got); err != nil {
				errs <- fmt.Errorf("query %d: unmarshal: %w", i, err)
				return
			}
			if string(got["echo"]) != payload {
				errs <- fmt.Errorf("query %d: got %s, want %s", i, got["echo"], payload)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestQueries_HandlerErrorReturnedToCaller: a handler error surfaces as an
// error reply with the rabbit-compatible message.
func TestQueries_HandlerErrorReturnedToCaller(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	serverName := newAppName()
	server := startAppWith(t, brokers, serverName, nil, func(app *rckafka.Application) error {
		return app.Registry().ServeQuery("explode", func(_ context.Context, _ async.AsyncQuery[any], _ async.From) (any, error) {
			return nil, errors.New("boom")
		})
	})
	defer server.cancel()

	client := startAppWith(t, brokers, newAppName(), nil, nil)
	defer client.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := client.app.Gateway().RequestReply(ctx, async.AsyncQuery[any]{Resource: "explode"}, serverName)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reactive-commons: query handler error: boom")
}

// TestQueries_EmptyCompletion: a nil handler response is delivered as a
// completion-only signal → (nil, nil) on the caller.
func TestQueries_EmptyCompletion(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	serverName := newAppName()
	server := startAppWith(t, brokers, serverName, nil, func(app *rckafka.Application) error {
		return app.Registry().ServeQuery("fire-and-ack", func(_ context.Context, _ async.AsyncQuery[any], _ async.From) (any, error) {
			return nil, nil
		})
	})
	defer server.cancel()

	client := startAppWith(t, brokers, newAppName(), nil, nil)
	defer client.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	body, err := client.app.Gateway().RequestReply(ctx, async.AsyncQuery[any]{Resource: "fire-and-ack"}, serverName)
	require.NoError(t, err)
	assert.Nil(t, body)
}

// TestQueries_ReplyListenerDisabled: with DisableReplyListener the app starts
// without a replies topic; RequestReply fails fast and commands still work.
func TestQueries_ReplyListenerDisabled(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	app := startAppWith(t, brokers, newAppName(), func(cfg *rckafka.KafkaConfig) {
		cfg.DisableReplyListener = true
	}, nil)
	defer app.cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := app.app.Gateway().RequestReply(ctx, async.AsyncQuery[any]{Resource: "x"}, "somewhere")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DisableReplyListener")

	// Commands are unaffected.
	targetName := newAppName()
	createTopic(t, brokers, targetName+".commands", 1, 1)
	require.NoError(t, app.app.Gateway().SendCommand(ctx, async.Command[any]{
		Name:      "orders.create",
		CommandID: uuid.NewString(),
		Data:      json.RawMessage(`{}`),
	}, targetName))
}
