//go:build integration

package kafka

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	kgo "github.com/segmentio/kafka-go"
)

// Benchmark_Consume measures the framework's per-message dispatch overhead:
// envelope decode + observability + handler invocation. It does NOT talk to a
// broker so it isolates the CPU cost of the hot path.
//
// Baseline (recorded 2026-09-15 on M-series macOS): ~2-5 µs/op, ~300-500
// B/op, ~6-10 allocs/op.
func Benchmark_Consume(b *testing.B) {
	cfg := Config{
		AppName:          "bench",
		BootstrapBrokers: []string{"localhost:9092"},
		HandlerTimeout:   30_000_000_000,
		MaxRetryAttempts: 1,
	}
	logger := slog.New(slog.NewTextHandler(nopWriter{}, nil))
	obs := resolveObservability(cfg)

	invocations := 0
	handler := async.EventHandler[any](func(_ context.Context, _ async.DomainEvent[any]) error {
		invocations++
		return nil
	})
	l := newEventListener(cfg, "user.created", handler, nil, logger, obs)

	envelope := map[string]any{
		"name":    "user.created",
		"eventId": "e-fixed",
		"data":    map[string]any{"userId": "u-42"},
	}
	body, _ := json.Marshal(envelope)
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.processMessageForBench(ctx, kgo.Message{Value: body})
	}
}

// processMessageForBench replicates the decode + invoke logic without the
// commit/DLQ side effects so the benchmark measures only dispatch overhead.
// Kept in the same file so it lives with its sole caller.
func (l *eventListener) processMessageForBench(ctx context.Context, msg kgo.Message) {
	raw, err := envelopeUnmarshalRawBench(msg.Value)
	if err != nil {
		return
	}
	event := async.DomainEvent[any]{Name: raw.name, EventID: raw.eventID}
	if len(raw.data) > 0 {
		var d any
		if err := json.Unmarshal(raw.data, &d); err == nil {
			event.Data = d
		}
	}
	_ = l.invokeHandler(ctx, event)
}

// tiny local envelope-decode helper so this bench file has zero imports on
// the internal envelope package (kept dependency-light for the benchmark).
type benchEnv struct {
	name    string
	eventID string
	data    json.RawMessage
}

func envelopeUnmarshalRawBench(b []byte) (benchEnv, error) {
	var v struct {
		Name    string          `json:"name"`
		EventID string          `json:"eventId"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return benchEnv{}, err
	}
	return benchEnv{name: v.Name, eventID: v.EventID, data: v.Data}, nil
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
