# Getting Started

This guide walks you through installing `reactive-commons-go`, creating your first application
instance, and sending and receiving your first message in under five minutes.

## Prerequisites

- **Go 1.22+** — generics are required
- **A supported broker:** RabbitMQ 3.x or Kafka
- A Go module initialized in your project: `go mod init <your-module>`

The runnable examples in this guide use RabbitMQ. To use Kafka, provide bootstrap broker
addresses and review the Kafka-specific topic and delivery semantics in [kafka.md](kafka.md).

### Start RabbitMQ with Docker

```bash
docker run -d --name rabbitmq \
  -p 5672:5672 -p 15672:15672 \
  rabbitmq:3-management
```

The management UI is available at `http://localhost:15672` (default credentials: `guest` / `guest`).

---

## Installation

```bash
go get github.com/bancolombia/reactive-commons-go
```

---

## Minimal Application

The entry point is `rabbit.NewApplication`. It returns an `*rabbit.Application` that implements
the `async.Application` interface.

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
    cfg.AppName  = "my-service"   // required — used for queue naming
    cfg.Password = "guest"

    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        log.Fatal("create app:", err)
    }

    // Register handlers here (see patterns below)

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    // Start blocks until the context is cancelled, then gracefully shuts down.
    if err := app.Start(ctx); err != nil {
        log.Fatal(err)
    }
}
```

> **Important**: register all handlers *before* calling `Start`. Handlers registered after
> `Start` are supported but require the broker connection to be live and will update queue
> bindings at runtime.

### Use Kafka

The Kafka backend uses the same `pkg/async` interfaces with its own constructor and config:

```go
import "github.com/bancolombia/reactive-commons-go/kafka"

cfg := kafka.NewConfigWithDefaults()
cfg.AppName = "my-service"
cfg.BootstrapBrokers = []string{"localhost:9092"}

app, err := kafka.NewApplication(cfg)
```

Register handlers before `Start`, as with RabbitMQ. Kafka verifies topics needed by registered
handlers and the reply listener during startup; topics must already exist unless
`AllowAutoCreateTopics` is enabled. See [Kafka support](kafka.md) for local setup and limitations.

---

## Waiting Until Ready

`Start` connects, declares topology, and launches consumer goroutines before it begins
blocking on the context. If you need to know when the application is fully operational
(e.g., before emitting events in tests), use `Ready()`:

```go
go func() {
    if err := app.Start(ctx); err != nil {
        log.Fatal(err)
    }
}()

<-app.Ready() // blocks until topology is declared and consumers are running
```

---

## Your First Event (30-second example)

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
)

type OrderCreated struct {
    OrderID string  `json:"orderId"`
    Amount  float64 `json:"amount"`
}

func main() {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName  = "order-service"
    cfg.Password = "guest"

    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        log.Fatal(err)
    }

    // Subscribe
    _ = app.Registry().ListenEvent("order.created",
        func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
            log.Printf("received order %s for %.2f", e.Data.OrderID, e.Data.Amount)
            return nil
        },
    )

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    // Start in background so we can emit after Ready
    go func() { _ = app.Start(ctx) }()
    <-app.Ready()

    // Publish
    _ = app.EventBus().Emit(ctx, async.DomainEvent[OrderCreated]{
        Name:    "order.created",
        EventID: "550e8400-e29b-41d4-a716-446655440000",
        Data:    OrderCreated{OrderID: "42", Amount: 99.99},
    })

    <-ctx.Done()
    time.Sleep(500 * time.Millisecond) // let handlers drain
}
```

---

## What Happens on Shutdown

For the RabbitMQ example above, when the context is cancelled, `Start` performs a graceful shutdown:

1. Stops accepting new messages on all consumer channels.
2. Waits up to **30 seconds** for in-flight handlers to complete.
3. Closes the AMQP connection.

Kafka also drains in-flight handlers before shutdown; its drain timeout is configurable through
`KafkaConfig.HandlerTimeout` (30 seconds by default).

---

## Next Steps

| Topic | Guide |
|-------|-------|
| Kafka backend and topic setup | [kafka.md](kafka.md) |
| Kafka and RabbitMQ semantics | [kafka-vs-rabbit.md](kafka-vs-rabbit.md) |
| Domain Events (fan-out) | [domain-events.md](domain-events.md) |
| Commands (point-to-point) | [commands.md](commands.md) |
| Async Queries (request/reply) | [async-queries.md](async-queries.md) |
| Notifications (non-durable fan-out) | [notifications.md](notifications.md) |
| Java Interoperability | [java-interop.md](java-interop.md) |
| Full Configuration Reference | [configuration.md](configuration.md) |
| Resilience & Error Handling | [resilience.md](resilience.md) |
| Testing Guide | [testing.md](testing.md) |
| Architecture | [architecture.md](architecture.md) |
