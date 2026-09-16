//go:build integration

package kafka_test

import (
	"context"
	"strings"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestObservability_EmitProducesSpanAndCounter covers T054: with a recording
// tracer + meter attached to the config, one Emit call must produce a produce
// span and a messages_emitted counter increment carrying the expected attrs.
func TestObservability_EmitProducesSpanAndCounter(t *testing.T) { //NOSONAR
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	topic := appName + ".user.created"
	createTopic(t, brokers, topic, 1, 1)

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("test")

	metricReader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(metricReader))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	cfg.Tracer = tracer
	cfg.MeterProvider = meterProvider

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    map[string]any{"userId": "u-42"},
	}
	emitCtx, emitCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer emitCancel()
	require.NoError(t, app.EventBus().Emit(emitCtx, ev))

	require.NoError(t, tracerProvider.ForceFlush(context.Background()))

	spans := spanRecorder.Ended()
	var produceSpan *sdktrace.ReadOnlySpan
	for i := range spans {
		if spans[i].Name() == "kafka.produce "+topic {
			s := spans[i]
			produceSpan = &s
			break
		}
	}
	require.NotNil(t, produceSpan, "expected a produce span for %s; got %d spans", topic, len(spans))

	attrs := map[string]string{}
	for _, kv := range (*produceSpan).Attributes() {
		attrs[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "kafka", attrs["messaging.system"])
	assert.Equal(t, topic, attrs["messaging.destination"])
	assert.Equal(t, "event", attrs["messaging.reactive_commons.kind"])

	var rm metricdata.ResourceMetrics
	require.NoError(t, metricReader.Collect(context.Background(), &rm))

	found := false
	var value int64
	var seenAttrs []attribute.KeyValue
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "reactive_commons_kafka_messages_emitted" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "counter must be Sum[int64]")
			for _, dp := range sum.DataPoints {
				topicAttr, _ := dp.Attributes.Value("messaging.destination")
				if topicAttr.AsString() == topic {
					found = true
					value = dp.Value
					seenAttrs = dp.Attributes.ToSlice()
				}
			}
		}
	}
	require.True(t, found, "messages_emitted counter with destination=%s not found", topic)
	assert.EqualValues(t, 1, value)

	attrSet := map[string]string{}
	for _, kv := range seenAttrs {
		attrSet[string(kv.Key)] = kv.Value.AsString()
	}
	assert.Equal(t, "kafka", attrSet["messaging.system"])
	assert.Equal(t, "event", attrSet["messaging.reactive_commons.kind"])
}
