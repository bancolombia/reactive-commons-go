# reactive-commons-go

A Go implementation of the [reactive-commons](https://github.com/reactive-commons/reactive-commons-java)
messaging patterns for RabbitMQ, wire-compatible with `reactive-commons-java`.

Provides four async messaging patterns — **domain events**, **commands**, **async queries (request/reply)**, and **notifications** — over a single RabbitMQ broker, with zero extra infrastructure.

**DISCLAIMER**: This project its currently in a alpha stage and not ready to use in production-grade components. 

## Installation

```bash
go get github.com/bancolombia/reactive-commons-go
```

Requires Go 1.22+ and RabbitMQ 3.x.

---

## Quick Start

```go
package main

import (
    "context"
    "log"
    "os/signal"
    "syscall"

    "github.com/bancolombia/reactive-commons-go/rabbit"
)

func main() {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName  = "my-service"
    cfg.Password = "guest"

    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // Register handlers before Start (see patterns below)

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    if err := app.Start(ctx); err != nil {
        log.Fatal(err)
    }
}
```

`Start` blocks until the context is cancelled, then performs a graceful shutdown
(waits up to 30 s for in-flight handlers before closing the connection).
Use `app.Ready()` to wait until topology setup and consumers are running.

---

## Patterns

### 1 — Domain Events (fan-out, durable)

**Publish**

```go
err := app.EventBus().Emit(ctx, async.DomainEvent[OrderCreated]{
    Name:    "order.created",
    EventID: uuid.New().String(),
    Data:    OrderCreated{OrderID: "42", Amount: 99.99},
})
```

**Subscribe** (register before `Start`)

```go
err := app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        log.Printf("order %s for %.2f", e.Data.OrderID, e.Data.Amount)
        return nil // non-nil → nack + redeliver
    },
)
```

---

### 2 — Commands (point-to-point, durable)

**Send**

```go
err := app.Gateway().SendCommand(ctx,
    async.Command[SendInvoice]{
        Name:      "send-invoice",
        CommandID: uuid.New().String(),
        Data:      SendInvoice{InvoiceID: "INV-001"},
    },
    "invoice-service", // target AppName
)
```

**Handle** (register before `Start`)

```go
err := app.Registry().ListenCommand("send-invoice",
    func(ctx context.Context, cmd async.Command[SendInvoice]) error {
        log.Printf("sending invoice %s", cmd.Data.InvoiceID)
        return nil
    },
)
```

---

### 3 — Async Queries (request/reply)

**Query** (blocks until response or context deadline)

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

raw, err := app.Gateway().RequestReply(ctx,
    async.AsyncQuery[GetProductRequest]{
        Resource:  "get-product",
        QueryData: GetProductRequest{ProductID: "SKU-999"},
    },
    "catalog-service",
)
if err != nil {
    log.Println("query failed:", err) // async.ErrQueryTimeout on deadline
    return
}

var product Product
json.Unmarshal(raw, &product)
```

**Serve** (register before `Start`)

```go
err := app.Registry().ServeQuery("get-product",
    func(ctx context.Context, q async.AsyncQuery[GetProductRequest], from async.From) (Product, error) {
        return Product{SKU: q.QueryData.ProductID, Name: "Widget", Price: 9.99}, nil
    },
)
```

---

### 4 — Notifications (fan-out, non-durable)

**Broadcast**

```go
err := app.EventBus().EmitNotification(ctx, async.Notification[CacheInvalidated]{
    Name:    "cache-invalidated",
    EventID: uuid.New().String(),
    Data:    CacheInvalidated{Key: "product:SKU-999"},
})
```

**Receive** (register before `Start`)

```go
err := app.Registry().ListenNotification("cache-invalidated",
    func(ctx context.Context, n async.Notification[CacheInvalidated]) error {
        log.Printf("evict key %s", n.Data.Key)
        return nil
    },
)
```

Notifications use auto-delete exclusive queues — late-joining subscribers will **not** receive missed notifications.

---

## Java Interoperability

No special configuration is required. Both Go and Java services use the same default exchange
names (`domainEvents`, `directMessages`, `globalReply`), so they communicate transparently:

| Java side | Go side |
|-----------|---------|
| `domainEventBus.emit(event)` | `ListenEvent` handler fires |
| `@HandleEvent` | `EventBus().Emit(...)` |
| `asyncGateway.sendCommand(...)` | `ListenCommand` handler fires |
| `@HandleCommand` | `Gateway().SendCommand(...)` |
| `asyncGateway.requestReply(...)` | `Gateway().RequestReply(...)` |
| `@HandleQuery` | `Registry().ServeQuery(...)` |

JSON field names match the Java Jackson serialization format:  
`eventId`, `commandId`, `resource`, `queryData`, `data`.

---

## Configuration Reference

```go
rabbit.RabbitConfig{
    // Required
    AppName: "my-service",

    // Connection (defaults shown)
    Host:        "localhost",
    Port:        5672,
    Username:    "guest",
    Password:    "guest",
    VirtualHost: "/",

    // Exchange names — defaults match reactive-commons-java
    DomainEventsExchange:   "domainEvents",
    DirectMessagesExchange: "directMessages",
    GlobalReplyExchange:    "globalReply",

    // Behaviour
    PrefetchCount:      250,
    ReplyTimeout:       15 * time.Second,
    PersistentEvents:   true,
    PersistentCommands: true,
    PersistentQueries:  false,

    // Queue type for durable consumer queues — "classic" or "quorum"
    QueueType: "classic",

    // Dead-letter queue retry (optional)
    WithDLQRetry: false,
    RetryDelay:   1 * time.Second,

    // Structured logger (defaults to slog.Default())
    Logger: slog.Default(),
}
```

Use `rabbit.NewConfigWithDefaults()` to get a config pre-populated with all defaults.

---

## Resilience

- **Auto-reconnect**: on broker disconnection the library retries with exponential backoff
  (1 s → 2 s → … → 30 s cap) and re-declares topology + restarts consumers automatically.
- **Panic recovery**: a panic inside any handler is caught, logged with a stack trace, and
  the consumer goroutine continues processing subsequent messages.
- **Dead-letter queues**: set `WithDLQRetry: true` to automatically declare a DLQ exchange
  and queue (`{exchange}.DLQ`) so failed messages can be inspected or replayed.

---

## Running Tests

```bash
# Unit tests (no broker required)
make test-unit

# Integration tests (requires Docker or a running RabbitMQ)
make test-integration

# Or set RABBITMQ_HOST / RABBITMQ_PORT to point at an existing broker
RABBITMQ_HOST=localhost go test -tags integration ./tests/integration/...
```

---

## Documentation

| Guide | Description |
|-------|-------------|
| [Getting Started](docs/getting-started.md) | Installation, minimal setup, your first event |
| [Domain Events](docs/domain-events.md) | Durable fan-out pub/sub — publish and subscribe |
| [Commands](docs/commands.md) | Point-to-point instructions to a named service |
| [Async Queries](docs/async-queries.md) | Request/reply with correlation and timeout |
| [Notifications](docs/notifications.md) | Non-durable broadcast for cache invalidation and live signals |
| [Java Interoperability](docs/java-interop.md) | Mix Go and Java services with zero configuration |
| [Configuration Reference](docs/configuration.md) | All `RabbitConfig` fields with defaults and examples |
| [Resilience & Error Handling](docs/resilience.md) | Auto-reconnect, graceful shutdown, panic recovery, DLQ |
| [Testing Guide](docs/testing.md) | Unit and integration test patterns, CI setup |
| [Architecture](docs/architecture.md) | Internal design, package layout, concurrency model |