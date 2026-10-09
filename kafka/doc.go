// Package kafka is a drop-in replacement for the rabbit package for the
// messaging patterns of reactive-commons.
//
// Switching a service from RabbitMQ to Kafka requires replacing the
// constructor and config; every other call site (Registry, EventBus,
// Gateway, handlers, envelope shape) is unchanged.
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
//   - DirectAsyncGateway.SendCommand — commands with retry + DLQ semantics
//   - DirectAsyncGateway.RequestReply / Reply — async queries with replies
//   - HandlerRegistry.ListenEvent — retry + DLQ semantics
//   - HandlerRegistry.ListenNotification — per-instance consumer group
//   - HandlerRegistry.ListenCommand — retry + DLQ, wildcard names supported
//   - HandlerRegistry.ServeQuery — exact-name resources only
//
// Topology: each app owns a commands topic and a queries topic
// ({app}.commands, {app}.queries by default) and a reply topic
// ({app}.replies) consumed through a per-instance consumer group. Commands
// and notifications share one topic per app and are dispatched by name;
// events keep one topic per event name.
//
// Differences from the rabbit transport:
//
//   - SendCommand requires the target's commands topic to exist (or the
//     broker/topics to be auto-creatable); rabbit silently drops to a
//     missing exchange instead.
//   - Command names may contain wildcards, but all instances of an app share
//     the commands consumer group, so a command matched by a wildcard on only
//     some instances can be consumed and discarded by another (same caveat as
//     rabbit).
//   - Command and query handlers run under KafkaConfig.HandlerTimeout;
//     rabbit's command/query handlers are unbounded.
//   - Command Data and query QueryData are json.RawMessage (same as rabbit).
//
// Query replies are best-effort (like notifications): a reply produced while
// the requesting instance is down is lost and the caller sees
// async.ErrQueryTimeout. Set DisableReplyListener for apps that never call
// RequestReply to skip the {app}.replies topic and reply consumer.
package kafka
