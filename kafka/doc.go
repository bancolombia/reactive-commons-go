// Package kafka is a drop-in replacement for the rabbit package for the event
// and notification patterns of reactive-commons.
//
// Switching a service from RabbitMQ to Kafka requires replacing the
// constructor and config; every other call site (Registry, EventBus,
// handlers, envelope shape) is unchanged.
//
//	// Before (RabbitMQ):
//	app, err := rabbit.NewApplication(rabbit.RabbitConfig{...})
//
//	// After (Kafka):
//	app, err := kafka.NewApplication(kafka.KafkaConfig{
//	    AppName:          "user-service",
//	    BootstrapBrokers: []string{"kafka-1:9092"},
//	})
//
// Supported patterns:
//
//   - DomainEventBus.Emit — durable, competing-consumer events
//   - DomainEventBus.EmitNotification — non-durable fan-out notifications
//   - HandlerRegistry.ListenEvent — retry + DLQ semantics
//   - HandlerRegistry.ListenNotification — per-instance consumer group
//
// Unsupported patterns return ErrNotSupportedOnKafka (wrapped with context):
//
//   - DirectAsyncGateway.SendCommand, RequestReply, Reply
//   - HandlerRegistry.ListenCommand, ServeQuery
//
// See docs/kafka-vs-rabbit.md for the full migration matrix and
// specs/001-kafka-broker-support/ for the feature contract.
package kafka
