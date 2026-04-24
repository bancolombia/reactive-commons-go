# Commands

Commands are point-to-point instructions directed at a **specific named service**. Unlike events,
only one service receives each command — the service whose `AppName` matches the routing key.

## Key Characteristics

| Property | Value |
|----------|-------|
| Exchange | `directMessages` (direct, durable) |
| Routing key | Target service `AppName` |
| Queue | `{appName}` (durable, one per target service) |
| Delivery | Persistent (delivery-mode 2) by default |
| Guarantee | At-least-once (broker confirms on send; nack triggers redelivery) |
| Fan-out | None — exactly one service receives each command |

---

## Defining a Payload Type

```go
type SendInvoice struct {
    InvoiceID  string `json:"invoiceId"`
    CustomerID string `json:"customerId"`
    Amount     float64 `json:"amount"`
}
```

---

## Sending a Command

```go
import (
    "context"
    "github.com/bancolombia/reactive-commons-go/pkg/async"
    "github.com/google/uuid"
)

err := app.Gateway().SendCommand(ctx,
    async.Command[SendInvoice]{
        Name:      "send-invoice",
        CommandID: uuid.New().String(),
        Data:      SendInvoice{InvoiceID: "INV-001", CustomerID: "CUST-42", Amount: 250.00},
    },
    "invoice-service", // target service AppName
)
if err != nil {
    // broker ack failed or ctx deadline exceeded
}
```

`SendCommand` blocks until the broker confirms the message. It respects the deadline on `ctx`.

---

## Handling a Command

Register handlers **before** calling `app.Start()`:

```go
err := app.Registry().ListenCommand("send-invoice",
    func(ctx context.Context, cmd async.Command[SendInvoice]) error {
        log.Printf("sending invoice %s for customer %s", cmd.Data.InvoiceID, cmd.Data.CustomerID)
        // perform work...
        return nil // return non-nil to nack (redelivery or DLQ)
    },
)
if err != nil {
    // async.ErrDuplicateHandler if "send-invoice" was already registered
}
```

---

## Routing

Commands are routed by the target service name. The `directMessages` exchange is a **direct**
exchange, so the routing key must match the queue name exactly:

```
Sender ──► directMessages exchange ──[routing key: "invoice-service"]──► invoice-service queue
                                    ──[routing key: "billing-service"]──► billing-service queue
```

If the target service is not running (queue does not exist), the command is dropped.
If the service is running but has no handler for the command name, the message is **nacked**
and discarded (or moved to DLQ if `WithDLQRetry: true`).

---

## Delayed Commands

To schedule a command for future delivery (requires the RabbitMQ Delayed Message Plugin):

```go
// Not natively supported in the core library — use a standard delayed queue setup
// or the broker's message TTL + DLQ mechanism.
```

> The routing key is appended with `-delayed` when a non-zero delay header is present
> in the internal implementation. Ensure your broker has the delayed message exchange
> plugin installed for this to function.

---

## Handler Error Semantics

| Return value | Broker action |
|--------------|---------------|
| `nil` | Message acknowledged (`ack`) |
| `error` | Message negatively acknowledged (`nack`); requeued or moved to DLQ |

Panics inside handlers are caught automatically. The consumer goroutine continues processing.
See [resilience.md](resilience.md).

---

## Wire Format

```json
{
  "name":      "send-invoice",
  "commandId": "a3bb189e-8bf9-3888-9912-ace4e6543002",
  "data":      { "invoiceId": "INV-001", "customerId": "CUST-42", "amount": 250.00 }
}
```

This matches the `reactive-commons-java` `Command` serialization format. A Java service calling
`asyncGateway.sendCommand(cmd, "invoice-service")` is received by Go `ListenCommand` handlers.

---

## Dead-Letter Queue and Delayed Retry (Optional)

Enable DLQ retry to delay-redeliver failed commands instead of looping immediately:

```go
cfg.WithDLQRetry = true
cfg.RetryDelay   = 5 * time.Second
```

When enabled, the topology mirrors `reactive-commons-java`'s
`ApplicationCommandListener`:

| Object | Type | Notes |
|--------|------|-------|
| `{directMessagesExchange}.DLQ` | direct, durable | Shared DLQ exchange (also used by queries) |
| `{appName}` | durable | Origin queue, dead-letters to the DLQ exchange |
| `{appName}.DLQ` | durable | DLQ queue, has `x-message-ttl=RetryDelay` and dead-letters back to `directMessages` |

After the TTL expires the broker re-publishes the message to
`directMessages` with the original routing key (`{appName}`), which routes it
back to the origin queue for another attempt.

---

## Persistent vs. Transient Commands

By default, commands are persistent (delivery-mode 2). To disable:

```go
cfg.PersistentCommands = false
```

---

## Wildcard Command Names

Command handler names may contain RabbitMQ topic-style wildcards (`*` for one
segment, `#` for zero or more). Because commands always route to a service by
its `AppName` on the direct exchange, wildcards affect only **dispatch-time
handler lookup** inside the receiving service — broker routing is unchanged.

```go
_ = app.Registry().ListenCommand("invoice.*", func(ctx context.Context, c async.Command[any]) error {
    // fires for "invoice.create", "invoice.cancel", ...
    return nil
})
```

Resolution follows the same rules as events: exact name wins; otherwise the
most specific matching pattern is selected. See
[domain-events.md](domain-events.md#wildcard-subscriptions) for the full
ranking semantics.

---

## Duplicate Handler Registration

Registering the same command name twice returns `async.ErrDuplicateHandler`:

```go
_ = app.Registry().ListenCommand("send-invoice", handlerA) // ok
err := app.Registry().ListenCommand("send-invoice", handlerB) // ErrDuplicateHandler
```

---

## Complete Example: Sender + Receiver

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

type SendInvoice struct {
    InvoiceID string `json:"invoiceId"`
}

func runReceiver(ctx context.Context) (*rabbit.Application, error) {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName = "invoice-service"
    app, err := rabbit.NewApplication(cfg)
    if err != nil {
        return nil, err
    }
    _ = app.Registry().ListenCommand("send-invoice",
        func(ctx context.Context, cmd async.Command[SendInvoice]) error {
            log.Printf("[invoice-service] processing invoice: %s", cmd.Data.InvoiceID)
            return nil
        },
    )
    return app, nil
}

func runSender(ctx context.Context) (*rabbit.Application, error) {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName = "order-service"
    return rabbit.NewApplication(cfg)
}

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    receiver, err := runReceiver(ctx)
    if err != nil {
        log.Fatal(err)
    }
    sender, err := runSender(ctx)
    if err != nil {
        log.Fatal(err)
    }

    go func() { _ = receiver.Start(ctx) }()
    go func() { _ = sender.Start(ctx) }()
    <-receiver.Ready()
    <-sender.Ready()

    _ = sender.Gateway().SendCommand(ctx,
        async.Command[SendInvoice]{
            Name:      "send-invoice",
            CommandID: uuid.New().String(),
            Data:      SendInvoice{InvoiceID: "INV-001"},
        },
        "invoice-service",
    )

    time.Sleep(500 * time.Millisecond)
    stop()
}
```

---

## See Also

- [domain-events.md](domain-events.md) — fan-out events
- [async-queries.md](async-queries.md) — request/reply pattern
- [resilience.md](resilience.md) — DLQ, panic recovery, reconnect
- [configuration.md](configuration.md) — persistence and exchange settings
