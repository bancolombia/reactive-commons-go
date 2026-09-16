package kafka

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	instrumentationName              = "github.com/bancolombia/reactive-commons-go/kafka"
	messagingSystemAttr              = "messaging.system"
	messagingDestinationAttr         = "messaging.destination"
	messagingReactiveCommonsKindAttr = "messaging.reactive_commons.kind"
)

// metricAttrs builds the standard label set for all reactive-commons Kafka
// counters/histograms so span attributes and metric labels stay in sync.
func metricAttrs(system, topic, kind string) metric.MeasurementOption {
	return metric.WithAttributes(
		attribute.String(messagingSystemAttr, system),
		attribute.String(messagingDestinationAttr, topic),
		attribute.String(messagingReactiveCommonsKindAttr, kind),
	)
}

// observability groups the OpenTelemetry handles a running KafkaApp uses to
// emit spans and record counters. All fields are non-nil after resolve: nil
// tracer/meter provider values in the config are replaced with noop
// implementations so the hot path is zero-branching.
type observability struct {
	tracer            trace.Tracer
	messagesEmitted   metric.Int64Counter
	messagesConsumed  metric.Int64Counter
	handlerErrors     metric.Int64Counter
	handlerDurationMs metric.Float64Histogram
}

func resolveObservability(cfg Config) *observability {
	tracer := cfg.Tracer
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer(instrumentationName)
	}
	mp := cfg.MeterProvider
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	meter := mp.Meter(instrumentationName)

	emitted, _ := meter.Int64Counter("reactive_commons_kafka_messages_emitted",
		metric.WithDescription("Number of messages emitted by the reactive-commons Kafka producer."))
	consumed, _ := meter.Int64Counter("reactive_commons_kafka_messages_consumed",
		metric.WithDescription("Number of messages dispatched to a handler by a reactive-commons Kafka consumer."))
	errs, _ := meter.Int64Counter("reactive_commons_kafka_handler_errors",
		metric.WithDescription("Number of handler invocations that returned an error."))
	hist, _ := meter.Float64Histogram("reactive_commons_kafka_handler_duration_seconds",
		metric.WithDescription("Handler invocation duration in seconds."),
		metric.WithUnit("s"))

	return &observability{
		tracer:            tracer,
		messagesEmitted:   emitted,
		messagesConsumed:  consumed,
		handlerErrors:     errs,
		handlerDurationMs: hist,
	}
}

// startProduceSpan opens a producer span with the messaging.* attributes the
// OpenTelemetry semantic conventions define. Returns the child context and a
// finalizer that ends the span with the error status.
func (o *observability) startProduceSpan(ctx context.Context, topic, kind string) (context.Context, func(err error)) {
	spanCtx, span := o.tracer.Start(ctx, "kafka.produce "+topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String(messagingSystemAttr, "kafka"),
			attribute.String(messagingDestinationAttr, topic),
			attribute.String(messagingReactiveCommonsKindAttr, kind),
			attribute.String("messaging.operation", "publish"),
		))
	return spanCtx, func(err error) {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}
}

// startConsumeSpan opens a consumer span. Symmetric to startProduceSpan.
func (o *observability) startConsumeSpan(ctx context.Context, topic, kind string) (context.Context, func(err error)) {
	spanCtx, span := o.tracer.Start(ctx, "kafka.consume "+topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String(messagingSystemAttr, "kafka"),
			attribute.String(messagingDestinationAttr, topic),
			attribute.String(messagingReactiveCommonsKindAttr, kind),
			attribute.String("messaging.operation", "process"),
		))
	return spanCtx, func(err error) {
		if err != nil {
			span.RecordError(err)
		}
		span.End()
	}
}
