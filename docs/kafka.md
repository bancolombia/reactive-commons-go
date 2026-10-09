# Kafka Support (Alpha)

The `kafka` package provides a Kafka backend for the shared `pkg/async` application, registry,
event bus, and gateway interfaces. It supports domain events, notifications, commands, and
request/reply queries. The shared API makes call sites similar, but Kafka delivery, routing, and
failure semantics differ from RabbitMQ. See [Kafka vs RabbitMQ](kafka-vs-rabbit.md) and the
[pattern guides](#pattern-guides).

## Create an Application

Provide an application name and at least one bootstrap broker. Register handlers before starting
the application:

```go
package main

import (
    "context"
    "log"

    "github.com/bancolombia/reactive-commons-go/kafka"
)

func main() {
    cfg := kafka.NewConfigWithDefaults()
    cfg.AppName = "catalog-service"
    cfg.BootstrapBrokers = []string{"localhost:9092"}

    app, err := kafka.NewApplication(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // Register event, command, query, or notification handlers here.

    if err := app.Start(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

For TLS or SASL, configure `KafkaConfig.TLS` or `KafkaConfig.SASL`. Runnable examples are under
[`examples/kafka`](../examples/kafka/).

## Configuration

Start with `kafka.NewConfigWithDefaults()`. `AppName` and `BootstrapBrokers` are required. The
most relevant settings are:

| Field | Default | Purpose |
|-------|---------|---------|
| `AppName` | required | Application identity and default topic/group naming |
| `BootstrapBrokers` | required | Kafka bootstrap broker addresses |
| `InstanceID` | generated UUID | Per-instance identity; affects notification/reply consumer groups |
| `ProducerAcks` | `RequireAll` | Producer acknowledgement policy |
| `MaxRetryAttempts` | `5` | Maximum event/command handler attempts |
| `RetryInitialDelay` | `1s` | Initial handler retry delay |
| `RetryMaxDelay` | `30s` | Maximum handler retry delay |
| `HandlerTimeout` | `30s` | Handler and shutdown-drain timeout |
| `AllowAutoCreateTopics` | `false` | Allow the backend to create required topics |
| `DefaultPartitions` | `3` | Partition count when creating topics |
| `DefaultReplicationFactor` | `1` | Replication factor when creating topics |
| `DisableReplyListener` | `false` | Skip the replies topic and listener; `RequestReply` then returns an error |

`KafkaConfig` has no RabbitMQ exchange/queue settings, `ReplyTimeout`, persistent-message flags,
`QueueType`, or delayed-command setting. Use `context.WithTimeout` to bound `RequestReply`.
For the RabbitMQ-specific configuration reference, see [configuration.md](configuration.md).

## Topic Provisioning

By default, topic auto-creation is disabled. At startup the application verifies topics required
by registered handlers and by the reply listener. Startup fails if required topics are missing
and `AllowAutoCreateTopics` is false. The configured defaults are three partitions and replication
factor one when the application creates a topic.

A sending application cannot verify an arbitrary target service's commands or queries topic at
startup, because the target is only known when `SendCommand` or `RequestReply` is called. Ensure
target topics exist before sending, or enable topic creation where appropriate for your broker.

## Default Topology

| Pattern | Default topic | Consumer behavior |
|---------|---------------|-------------------|
| Domain events | `{AppName}.{eventName}` | Replicas of one application share a group and compete; distinct groups can each consume the topic |
| Notifications | `{AppName}.{notificationName}` | Per-instance groups provide fan-out to current listeners; default generated instance IDs do not replay missed notifications after restart |
| Commands | `{targetApp}.commands` | Target application's replicas compete in a consumer group |
| Queries | `{targetApp}.queries` | Target application's replicas consume requests; replies go to `{callerApp}.replies` and are correlated per request |

The default event and notification topics include the publishing application's name. For
cross-application event or notification delivery, configure compatible `TopicNameFunc` or
`NotificationTopicNameFunc` functions on publisher and consumers. Commands, queries, and replies
have separate topic-name functions for customizing their naming contracts.

## Pattern Behavior and Limitations

- **Commands:** handler failures retry with backoff and eventually go to `{topic}.dlq`. Unknown
  command names are committed and discarded. Wildcard names are supported at dispatch, but every
  target replica should register compatible handlers. Delayed commands are not supported.
- **Queries:** `RequestReply`, `ServeQuery`, and `Reply` are implemented. Replies are best-effort:
  if the caller's reply listener is unavailable when the reply is produced, the caller can time
  out. Missing handlers are committed without a reply; query handlers have no retry or DLQ path.
  Query names require exact matches. See [Async Queries](async-queries.md#kafka-behavior-and-limitations).
- **Events:** event handler failures retry and then go to a topic DLQ. Event names map to
  per-event topics; RabbitMQ topic wildcard subscriptions do not carry over. See
  [Domain Events](domain-events.md#kafka-behavior-and-limitations).
- **Notifications:** handler failures are logged and dropped, without retry or DLQ. Publishing
  waits for the configured producer acknowledgement, unlike RabbitMQ's fire-and-forget
  notification publish. See [Notifications](notifications.md#kafka-behavior-and-limitations).

## Pattern Guides

- [Commands](commands.md#kafka-behavior-and-limitations)
- [Async Queries](async-queries.md#kafka-behavior-and-limitations)
- [Domain Events](domain-events.md#kafka-behavior-and-limitations)
- [Notifications](notifications.md#kafka-behavior-and-limitations)
- [Resilience](resilience.md#kafka-resilience)
- [Testing](testing.md#running-the-tests)
