# Architecture

This document describes the internal structure of `reactive-commons-go`, how components fit
together, and the key design decisions behind the implementation.

---

## Package Layout

```
reactive-commons-go/
├── pkg/async/          # Public API — interfaces, types, errors (no implementation)
│   ├── types.go        # DomainEvent, Command, AsyncQuery, Notification, From
│   ├── handlers.go     # EventHandler, CommandHandler, QueryHandler, NotificationHandler
│   ├── registry.go     # HandlerRegistry interface
│   ├── eventbus.go     # DomainEventBus interface
│   ├── gateway.go      # DirectAsyncGateway interface
│   ├── errors.go       # ErrDuplicateHandler, ErrQueryTimeout, ErrBrokerUnavailable, ErrDeserialize
│   └── application.go  # Application interface
│
├── pkg/headers/
│   └── headers.go      # Wire header constant names (matches Java Headers.java)
│
├── rabbit/             # Public factory (importable by consumers)
│   ├── config.go       # RabbitConfig, NewConfigWithDefaults, Validate
│   └── builder.go      # NewApplication — thin wrapper over internal RabbitApp
│
├── internal/rabbit/    # RabbitMQ implementation (not exported)
│   ├── application.go  # RabbitApp — lifecycle coordinator
│   ├── connection.go   # AMQP connection + channel pool + reconnect
│   ├── topology.go     # Exchange/queue declaration
│   ├── sender.go       # Message publish (with/without confirm)
│   ├── eventbus.go     # DomainEventBus implementation
│   ├── gateway.go      # DirectAsyncGateway implementation
│   ├── eventlistener.go       # Domain event consumer goroutine
│   ├── commandlistener.go     # Command consumer goroutine
│   ├── querylistener.go       # Query consumer + auto-reply
│   ├── notificationlistener.go # Notification consumer goroutine
│   ├── replylistener.go       # Reply queue consumer
│   ├── replyrouter.go         # Correlation ID → channel routing
│   ├── registry.go            # In-memory handler registry
│   └── converter.go           # JSON two-phase serialization
│
├── internal/utils/
│   └── namegen.go      # Queue/exchange name generation
│
└── tests/
    ├── unit/           # No broker; pure Go logic tests
    └── integration/    # testcontainers-go; real RabbitMQ
```

---

## Layered Design

```
┌─────────────────────────────────────────────────────┐
│                   Application Code                   │
│  imports pkg/async interfaces + rabbit.NewApplication│
└───────────────────┬──────────────────────────────────┘
                    │ uses
┌───────────────────▼──────────────────────────────────┐
│              rabbit/  (public factory)                │
│   NewApplication(cfg) → *rabbit.Application          │
│   RabbitConfig struct + NewConfigWithDefaults()       │
└───────────────────┬──────────────────────────────────┘
                    │ delegates to
┌───────────────────▼──────────────────────────────────┐
│         internal/rabbit/  (RabbitMQ implementation)  │
│   RabbitApp, connection, topology, sender,            │
│   listeners, gateway, eventbus, replyrouter           │
└───────────────────┬──────────────────────────────────┘
                    │ wraps
┌───────────────────▼──────────────────────────────────┐
│         github.com/rabbitmq/amqp091-go               │
│   AMQP 0-9-1 client (official RabbitMQ Go client)    │
└──────────────────────────────────────────────────────┘
```

The `pkg/async` package has **no RabbitMQ dependency** — it contains only interface definitions
and type declarations. This makes it possible to add a Kafka backend in a future phase without
breaking existing consumers.

---

## Startup Sequence

`Start(ctx)` executes the following steps synchronously before closing the `Ready()` channel:

```
1. Dial AMQP connection (with context deadline)
2. Open topology channel
3. Declare exchanges:   domainEvents, directMessages, globalReply
4. Create sender (publisher confirm channel pool)
5. Create gateway (wraps sender)
6. Declare + bind temp reply queue  {appName}.replies.{uuid}
7. Register reconnect hook
8. Set app.bus, app.gw (protected by RWMutex)
9. startEventConsumer     → declare {appName}.subsEvents + bindings → launch goroutine
10. startCommandConsumer  → declare {appName} queue + binding → launch goroutine
11. startQueryConsumer    → declare {appName}.query + binding → launch goroutine
12. startReplyListener    → consume reply queue → launch goroutine
13. startNotificationConsumer → declare {appName}.notification.{uuid} + bindings → launch goroutine
14. close(ready)          ← Ready() unblocks here
15. <-ctx.Done()          ← block until shutdown
16. Graceful shutdown (drain in-flight handlers, close connection)
```

---

## Connection Pool

`Connection` maintains:

- **1 AMQP connection** — shared across all channels
- **4 publisher channels** — round-robin selection for concurrent publishes
- **N consumer channels** — one per listener goroutine (consumers get dedicated channels)

The 4-channel publisher pool prevents the bottleneck of a single publish queue while staying
within RabbitMQ's default per-connection channel limit.

---

## Message Serialization

`converter.go` uses **two-phase JSON deserialization** to avoid requiring callers to deal
with `interface{}` type assertions:

1. **Phase 1** — unmarshal the message envelope into `RawEnvelope` (always known shape):
   ```go
   type RawEnvelope struct {
       Name      string          `json:"name,omitempty"`
       EventID   string          `json:"eventId,omitempty"`
       CommandID string          `json:"commandId,omitempty"`
       Resource  string          `json:"resource,omitempty"`
       Data      json.RawMessage `json:"data,omitempty"`
       QueryData json.RawMessage `json:"queryData,omitempty"`
   }
   ```

2. **Phase 2** — when the handler type is known (from the registry), unmarshal
   `RawEnvelope.Data` (or `.QueryData`) directly into the handler's generic type `T`.

This pattern avoids double-marshaling and defers payload deserialization until the handler
type is resolved.

---

## Reply Router

The `ReplyRouter` manages in-flight query correlations. It is backed by a `sync.Map` for
lock-free concurrent access.

```
RequestReply call      ReplyRouter         ReplyListener consumer
──────────────────     ───────────────     ──────────────────────
Generate correlationID
Register(id) → chan []byte ─────────────┐
Publish query ──────────────────────────┼──► broker ──► query handler
                                         │              ──► publishes reply
                                         │         ◄────── reply arrives
                           Route(id, body)│ ◄─────────────
                         send body to chan │
◄── chan receives body ──────────────────┘
Unmarshal + return
```

After a timeout, `RequestReply` calls `Deregister(id)`, removing the channel. Any late reply
arriving for a deregistered ID is silently dropped — no goroutine leak, no panic.

---

## Goroutine Model

Each consumer listener runs as a **single long-lived goroutine** that processes messages
from an AMQP delivery channel sequentially:

```
listener goroutine
  loop:
    delivery := <-amqpCh.Consume(...)
    recover() wrapper
      handler(ctx, message)
    ack/nack based on error
```

Handler execution is **synchronous** within each listener. For truly parallel processing,
handlers should spawn their own goroutines:

```go
app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        go func() {
            heavyWork(e.Data)
        }()
        return nil // ack immediately; work continues in background
    },
)
```

---

## Queue / Exchange Name Generation

`internal/utils/namegen.go` mirrors the Java `NameGenerator`:

| Pattern | Formula | Example |
|---------|---------|---------|
| Subscription events queue | `{appName}.subsEvents` | `order-svc.subsEvents` |
| Commands queue | `{appName}` | `order-svc` |
| Queries queue | `{appName}.query` | `order-svc.query` |
| Reply queue | `{appName}.replies.{base64UUID}` | `order-svc.replies.abc123==` |
| Notification queue | `{appName}.notification.{base64UUID}` | `order-svc.notification.xyz789==` |

`{base64UUID}` is a URL-safe base64-encoded random UUID generated **once per application
instance**. This ensures temporary queues are unique across restarts.

---

## Concurrency Safety

| Component | Thread safety |
|-----------|--------------|
| `HandlerRegistry` | Safe for concurrent reads after `Start()`; writes before `Start()` only |
| `ReplyRouter` | Uses `sync.Map`; safe for concurrent register/route/deregister |
| `RabbitApp.bus` / `.gw` | Protected by `sync.RWMutex`; set once during `Start()` |
| Publisher channels | Round-robin pool; each channel used by one goroutine at a time |
| Consumer goroutines | One per listener; sequential delivery within each |

---

## Future Extension Points

The public API is intentionally broker-agnostic:

- `pkg/async.Application` interface — implementable by any broker backend
- `pkg/async.DomainEventBus` — publishable without a specific connection type
- Handler registry — pure in-memory; no broker dependency

A Kafka implementation could be added under `kafka/` providing `kafka.NewApplication(cfg)`,
returning the same `async.Application` interface without any changes to consumer code.

---

## See Also

- [configuration.md](configuration.md) — all configuration fields
- [resilience.md](resilience.md) — reconnect and graceful shutdown details
- [testing.md](testing.md) — how the test suite is structured
