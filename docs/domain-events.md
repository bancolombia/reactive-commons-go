# Domain Events

Domain events represent immutable facts that happened in your system. They are published to
**all** subscribed services — a classic fan-out / pub-sub pattern using a durable topic exchange.
The topology and guarantees described below are RabbitMQ-specific. Kafka also supports domain
events, using topics and consumer groups with different fan-out semantics.

## RabbitMQ Characteristics

| Property | Value |
|----------|-------|
| Exchange | `domainEvents` (topic, durable) |
| Routing key | Event name (e.g., `order.created`) |
| Queue | `{appName}.subsEvents` (durable, one per subscriber service) |
| Delivery | Persistent (delivery-mode 2) by default |
| Guarantee | At-least-once (broker confirms on publish; nack triggers redelivery) |
| Fanout | Every service with a handler for the event name receives its own copy |

## Kafka Behavior and Limitations

Kafka publishes an event to `{AppName}.{eventName}` by default. Consumers for the same app and
event share a consumer group, so replicas compete to process each event; they do not each receive
a separate copy. Independent applications can each receive a copy only when they consume the
same topic in distinct groups.

| Behavior | Kafka |
|----------|-------|
| Topic | `{AppName}.{eventName}` by default; customize with `TopicNameFunc` |
| Replicas of one app | Share a consumer group and compete for messages |
| Different apps | Receive copies only if configured to consume the same topic |
| Wildcard subscriptions | RabbitMQ topic wildcards do not apply; Kafka listeners use per-event topics |
| Handler failure | Retried up to `MaxRetryAttempts`, then sent to `{topic}.dlq` |
| Topic provisioning | Required topics must exist unless `AllowAutoCreateTopics` is enabled |

Default topic names include the application name. A publisher and consumer with different
`AppName` values therefore do not automatically share an event topic. Configure compatible topic
functions on both sides for cross-application subscriptions. Register exact event names; a name
such as `order.*` is not a Kafka wildcard subscription. See [kafka.md](kafka.md) and
[kafka-vs-rabbit.md](kafka-vs-rabbit.md) for setup and backend comparison.

---

## Defining a Payload Type

```go
type OrderCreated struct {
    OrderID string  `json:"orderId"`
    Amount  float64 `json:"amount"`
}
```

JSON field names must match what the publisher sends. When interoperating with Java services,
use the camelCase conventions that Jackson produces.

---

## Publishing an Event

```go
import (
    "context"
    "github.com/bancolombia/reactive-commons-go/pkg/async"
)

err := app.EventBus().Emit(ctx, async.DomainEvent[OrderCreated]{
    Name:    "order.created",
    EventID: uuid.New().String(), // unique ID per event instance
    Data:    OrderCreated{OrderID: "42", Amount: 99.99},
})
if err != nil {
    // broker acknowledge failed or ctx deadline exceeded
}
```

`Emit` blocks until the broker sends a publisher confirm. It respects the deadline on `ctx`,
returning an error if the broker does not ack within that window.

---

## Subscribing to an Event

Register handlers **before** calling `app.Start()`:

```go
err := app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        log.Printf("order %s placed for %.2f", e.Data.OrderID, e.Data.Amount)
        return nil // return non-nil to nack (redelivery or DLQ if enabled)
    },
)
if err != nil {
    // async.ErrDuplicateHandler if "order.created" was already registered
}
```

---

## Wildcard Subscriptions

Event names may contain RabbitMQ topic-exchange wildcards so a single handler
covers a family of related events:

| Token | Matches |
|-------|---------|
| `*` | exactly one dot-separated segment |
| `#` | zero or more segments |

```go
app.Registry().ListenEvent("order.*", handler)        // order.created, order.cancelled
app.Registry().ListenEvent("inventory.#", handler)    // inventory, inventory.updated, inventory.v2.deleted
```

Resolution rules at dispatch time mirror
[reactive-commons-java](https://github.com/reactive-commons/reactive-commons-java)'s
`HandlerResolver`:

1. An exact-name match always wins over any wildcard match.
2. When only patterns match, the most specific is chosen (fewer wildcards
   first; `*` is more specific than `#`; longer patterns break ties).
3. Each concrete name resolves to a handler at most once and is then cached
   for the life of the registry.

You can mix exact handlers and wildcard handlers freely; the exact one always
takes precedence:

```go
_ = app.Registry().ListenEvent("order.created", specificHandler) // wins for order.created
_ = app.Registry().ListenEvent("order.*",       fallbackHandler) // fires for everything else under order.*
```

> Wildcards are not supported on `ServeQuery` — see [async-queries.md](async-queries.md).

---

## Multiple Subscribers

Each service that registers a handler gets its own durable queue bound to the `domainEvents`
exchange. All handlers fire for every published event.

```
Publisher ──► domainEvents exchange
                    │
          ┌─────────┴──────────┐
          ▼                    ▼
  order-service.subsEvents  shipping-service.subsEvents
  (OrderCreated handler)    (OrderShipped handler)
```

Two independent services registering the **same** event name each receive an independent copy
of every event. This is the standard reactive-commons fan-out model.

---

## Handler Error Semantics

| Return value | Broker action |
|--------------|---------------|
| `nil` | Message acknowledged (`ack`) |
| `error` | Message negatively acknowledged (`nack`); requeued or moved to DLQ if `WithDLQRetry: true` |

Panics inside handlers are **caught automatically** and treated as nack. The consumer goroutine
continues processing subsequent messages. See [resilience.md](resilience.md) for details.

---

## Wire Format

```json
{
  "name":    "order.created",
  "eventId": "550e8400-e29b-41d4-a716-446655440000",
  "data":    { "orderId": "42", "amount": 99.99 }
}
```

This matches the `reactive-commons-java` serialization format exactly. A Java service
calling `domainEventBus.emit(event)` is received by Go `ListenEvent` handlers, and vice-versa.

---

## Persistent vs. Transient Events

By default, events are published with delivery-mode 2 (persistent). They survive a broker restart.

To publish transient events (delivery-mode 1, slightly faster):

```go
cfg := rabbit.NewConfigWithDefaults()
cfg.PersistentEvents = false
```

---

## Dead-Letter Queue and Delayed Retry (Optional)

Enable DLQ retry to delay-redeliver failed events instead of looping immediately:

```go
cfg.WithDLQRetry = true
cfg.RetryDelay   = 5 * time.Second
```

When enabled, the topology mirrors `reactive-commons-java`'s
`ApplicationEventListener`:

| Object | Type | Notes |
|--------|------|-------|
| `{appName}.{domainEventsExchange}` | topic, durable | Per-app retry exchange |
| `{appName}.{domainEventsExchange}.DLQ` | topic, durable | Per-app DLQ exchange |
| `{appName}.subsEvents` | durable | Origin queue, dead-letters to the DLQ exchange |
| `{appName}.subsEvents.DLQ` | durable | DLQ queue, has `x-message-ttl=RetryDelay` and dead-letters back to the retry exchange |

Flow on handler failure:

1. The handler returns an error → the listener nacks with `requeue=false`.
2. The broker dead-letters the message to `{appName}.{domainEventsExchange}.DLQ`,
   which routes it to `{appName}.subsEvents.DLQ`.
3. After `RetryDelay` ms the message TTL expires; the broker dead-letters it
   to `{appName}.{domainEventsExchange}` (the per-app retry exchange), which
   re-routes it to `{appName}.subsEvents` for a fresh attempt.

Without `WithDLQRetry` the listener requeues failed deliveries immediately
(infinite retry loop), so set `WithDLQRetry=true` for any non-trivial
production deployment.

---

## Complete Example

```go
package main

import (
    "context"
    "log"
    "os/signal"
    "syscall"
    "time"

    "github.com/bancolombia/reactive-commons-go/pkg/async"
    "github.com/bancolombia/reactive-commons-go/rabbit"
    "github.com/google/uuid"
)

type OrderCreated struct {
    OrderID string  `json:"orderId"`
    Amount  float64 `json:"amount"`
}

func main() {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName = "order-processor"

    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // Register before Start
    if err := app.Registry().ListenEvent("order.created",
        func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
            log.Printf("[handler] order %s for $%.2f received",
                e.Data.OrderID, e.Data.Amount)
            return nil
        },
    ); err != nil {
        log.Fatal(err)
    }

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    go func() {
        if err := app.Start(ctx); err != nil {
            log.Fatal(err)
        }
    }()
    <-app.Ready() // wait for topology + consumers

    // Publish events
    for i := 0; i < 3; i++ {
        _ = app.EventBus().Emit(ctx, async.DomainEvent[OrderCreated]{
            Name:    "order.created",
            EventID: uuid.New().String(),
            Data:    OrderCreated{OrderID: "order-" + string(rune('A'+i)), Amount: float64(i+1) * 10},
        })
        time.Sleep(100 * time.Millisecond)
    }

    <-ctx.Done()
}
```

---

## See Also

- [commands.md](commands.md) — point-to-point instructions
- [notifications.md](notifications.md) — non-durable broadcasts
- [configuration.md](configuration.md) — exchange/queue tuning
- [java-interop.md](java-interop.md) — interoperability with reactive-commons-java
