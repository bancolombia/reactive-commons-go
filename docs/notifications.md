# Notifications

Notifications are **non-durable broadcasts** — a lighter-weight fan-out mechanism for
time-sensitive signals where missing a message is acceptable. They use the same `domainEvents`
exchange as domain events but route to **temporary, auto-delete, exclusive** queues that
exist only for the lifetime of the running subscriber.

## Key Characteristics

| Property | Value |
|----------|-------|
| Exchange | `domainEvents` (topic, durable) |
| Routing key | Notification name (e.g., `cache-invalidated`) |
| Queue | `{appName}.notification.{uuid}` (non-durable, auto-delete, exclusive) |
| Delivery | Transient (delivery-mode 1) |
| Guarantee | Best-effort — late-joining subscribers miss past notifications |
| Fan-out | Every currently-running instance with a handler receives the notification |

## Contrast with Domain Events

| | Domain Events | Notifications |
|---|---|---|
| Queue | Durable | Temp (auto-delete) |
| Late-joiner gets past messages | Yes | **No** |
| Use case | Business facts, auditing | Cache invalidation, live reload, UI push |
| Delivery mode | Persistent (2) | Transient (1) |

---

## Defining a Payload Type

```go
type CacheInvalidated struct {
    Key    string `json:"key"`
    Region string `json:"region"`
}
```

---

## Broadcasting a Notification

```go
import (
    "context"
    "github.com/bancolombia/reactive-commons-go/pkg/async"
    "github.com/google/uuid"
)

err := app.EventBus().EmitNotification(ctx, async.Notification[CacheInvalidated]{
    Name:    "cache-invalidated",
    EventID: uuid.New().String(),
    Data:    CacheInvalidated{Key: "product:SKU-999", Region: "eu-west"},
})
```

Unlike `Emit`, `EmitNotification` does **not** wait for a publisher confirm (fire-and-forget
at the broker level). It returns an error only if the broker connection is unavailable.

---

## Receiving a Notification

Register handlers **before** calling `app.Start()`:

```go
err := app.Registry().ListenNotification("cache-invalidated",
    func(ctx context.Context, n async.Notification[CacheInvalidated]) error {
        log.Printf("evict cache key %q in region %q", n.Data.Key, n.Data.Region)
        return nil // errors are logged; no redelivery
    },
)
```

---

## Behavior for Late-Joining Subscribers

Because notification queues are auto-delete and exclusive, a subscriber that starts **after**
a notification was broadcast will **not** receive that notification:

```
t=0: app-A starts, registers handler for "cache-invalidated"
t=1: notification broadcast → app-A handler fires ✓
t=2: app-B starts, registers handler for "cache-invalidated"
     app-B did NOT receive the t=1 notification ✗ (expected)
t=3: notification broadcast → both app-A and app-B handlers fire ✓
```

This is intentional. If your use case requires reliable delivery to all instances regardless
of timing, use [domain events](domain-events.md) with a durable queue instead.

---

## Broadcasting With Zero Subscribers

If no service is currently listening for a notification, the `domainEvents` exchange simply
has no bound queue for that routing key during that moment. The notification is silently
dropped — no error is returned to the broadcaster.

```go
// Safe even with no registered listeners
err := app.EventBus().EmitNotification(ctx, async.Notification[CacheInvalidated]{
    Name:    "cache-invalidated",
    EventID: uuid.New().String(),
    Data:    CacheInvalidated{Key: "product:GHOST"},
})
// err == nil
```

---

## Handler Error Semantics

Unlike domain events and commands, notification handler errors are **logged only** — there is
no redelivery, no nack, and no DLQ. This reflects the non-durable delivery guarantee.

| Return value | Action |
|--------------|--------|
| `nil` | Message acknowledged |
| `error` | Logged; message acknowledged (no redelivery) |

Panics are still caught by the panic recovery wrapper. See [resilience.md](resilience.md).

---

## Wire Format

Notifications use the same JSON envelope as domain events:

```json
{
  "name":    "cache-invalidated",
  "eventId": "550e8400-e29b-41d4-a716-446655440000",
  "data":    { "key": "product:SKU-999", "region": "eu-west" }
}
```

They are distinguished from domain events only by the queue type (non-durable/auto-delete)
and delivery mode (1 vs 2), not by the message format itself.

---

## Multiple Notification Types

A single application can register handlers for multiple notification types:

```go
_ = app.Registry().ListenNotification("cache-invalidated", cacheHandler)
_ = app.Registry().ListenNotification("config-reloaded", configHandler)
_ = app.Registry().ListenNotification("feature-flag-updated", flagHandler)
```

Each notification name creates a separate binding on the shared `{appName}.notification.{uuid}`
queue. All notifications arrive on the same queue and are dispatched by name.

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

type CacheInvalidated struct {
    Key string `json:"key"`
}

func main() {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName = "product-service"

    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        log.Fatal(err)
    }

    _ = app.Registry().ListenNotification("cache-invalidated",
        func(ctx context.Context, n async.Notification[CacheInvalidated]) error {
            log.Printf("[cache] evicting key: %s", n.Data.Key)
            return nil
        },
    )

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    go func() { _ = app.Start(ctx) }()
    <-app.Ready()

    // Broadcast notification
    _ = app.EventBus().EmitNotification(ctx, async.Notification[CacheInvalidated]{
        Name:    "cache-invalidated",
        EventID: uuid.New().String(),
        Data:    CacheInvalidated{Key: "product:SKU-999"},
    })

    time.Sleep(300 * time.Millisecond)
    stop()
}
```

---

## See Also

- [domain-events.md](domain-events.md) — durable fan-out for business facts
- [configuration.md](configuration.md) — exchange naming
- [java-interop.md](java-interop.md) — interoperability with Java notification publishers
