# Resilience & Error Handling

`reactive-commons-go` is designed for production messaging workloads. It includes built-in
mechanisms for reconnection, graceful shutdown, panic isolation, and dead-letter routing.

---

## Auto-Reconnect

The library automatically reconnects to RabbitMQ when the broker connection drops.

### How It Works

1. The AMQP client detects a connection error (`amqp.ErrClosed` or similar network failure).
2. The library waits using **exponential backoff**: 1 s → 2 s → 4 s → 8 s → … → 30 s cap.
3. After re-establishing the connection, it:
   - Re-declares all exchanges and queues (topology is idempotent)
   - Restarts all consumer goroutines

### What Callers Experience

| Component | Behaviour during outage |
|-----------|------------------------|
| `Emit` / `SendCommand` | Returns an error if the broker is unavailable during the publish |
| `RequestReply` | Returns `ErrBrokerUnavailable` or context timeout |
| Consumer goroutines | Paused; automatically resume after reconnect |
| In-flight messages at handler level | Message re-delivered by broker after reconnect (at-least-once) |

There is no configuration required — auto-reconnect is always active.

### Backoff Profile

| Attempt | Wait |
|---------|------|
| 1 | 1 s |
| 2 | 2 s |
| 3 | 4 s |
| 4 | 8 s |
| 5 | 16 s |
| 6+ | 30 s (capped) |

---

## Graceful Shutdown

When the `context.Context` passed to `Start` is cancelled, the library performs an orderly shutdown:

1. **Stop accepting new messages** — consumer delivery channels are closed; no new messages
   are dispatched to handlers.
2. **Wait for in-flight handlers** — the library waits for all currently-running handler
   goroutines to complete, up to a **30-second hard timeout**.
3. **Close connection** — the AMQP connection is closed cleanly.

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// SIGINT / SIGTERM triggers graceful shutdown
if err := app.Start(ctx); err != nil {
    log.Fatal(err)
}
// Execution reaches here only after full shutdown
```

### Zero-Message-Loss Guarantee

For up to ~100 in-flight messages the 30-second window is sufficient for handlers to
complete. For higher in-flight counts, ensure your handlers have timeouts:

```go
app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        // Use a child context with a handler-level timeout
        procCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        defer cancel()
        return processOrder(procCtx, e.Data)
    },
)
```

---

## Panic Recovery

Every consumer goroutine wraps handler invocations in a `recover()`. If a handler panics:

1. The panic is caught.
2. The stack trace is logged via the configured `slog.Logger`.
3. The message is **nacked** (requeued or moved to DLQ).
4. The consumer goroutine **continues** processing subsequent messages.

No panic propagates out of the library. Your service stays alive.

### Example

```go
app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        panic("something went very wrong") // caught automatically
    },
)
```

The log output will include:

```json
{
  "level": "ERROR",
  "msg": "handler panic recovered",
  "pattern": "event",
  "name": "order.created",
  "panic": "something went very wrong",
  "stack": "goroutine 17 [running]:\n..."
}
```

---

## Dead-Letter Queues (DLQ)

Enable DLQ routing for events and commands to capture messages that fail repeatedly:

```go
cfg := rabbit.NewConfigWithDefaults()
cfg.AppName      = "my-service"
cfg.WithDLQRetry = true
cfg.RetryDelay   = 5 * time.Second
```

### What Gets Declared

When `WithDLQRetry: true`:

| Declared resource | Used for |
|-------------------|---------|
| `domainEvents.DLQ` (direct exchange) | Dead-lettered events |
| `{appName}.subsEvents.DLQ` (queue) | Failed event messages |
| `directMessages.DLQ` (direct exchange) | Dead-lettered commands |
| `{appName}.DLQ` (queue) | Failed command messages |

Events and commands queues are declared with:
- `x-dead-letter-exchange`: pointing to the DLQ exchange
- `x-message-ttl`: set to `RetryDelay` in milliseconds

### DLQ Flow

```
Handler returns error → nack → broker requeues → (after RetryDelay) → moves to DLQ queue
```

From the DLQ queue, messages can be:
- Inspected via the RabbitMQ Management UI
- Replayed by moving them back to the primary queue
- Consumed by a dedicated DLQ processor service

---

## Error Types

| Type | Description |
|------|-------------|
| `async.ErrDuplicateHandler` | `ListenEvent`, `ListenCommand`, `ServeQuery`, or `ListenNotification` called twice for the same name |
| `async.ErrQueryTimeout` | `RequestReply` context deadline exceeded before a response was received |
| `async.ErrBrokerUnavailable` | Broker connection unavailable during a publish or subscribe operation |
| `async.ErrDeserialize` | Incoming message body could not be unmarshaled into the handler's target type |

```go
import (
    "errors"
    "github.com/bancolombia/reactive-commons-go/pkg/async"
)

raw, err := app.Gateway().RequestReply(ctx, query, "catalog-service")
if errors.Is(err, async.ErrQueryTimeout) {
    log.Println("catalog service did not respond in time")
} else if errors.Is(err, async.ErrBrokerUnavailable) {
    log.Println("broker connection unavailable")
}
```

---

## Handler Returning Errors vs. Panicking

| Scenario | Recommended approach |
|----------|---------------------|
| Business validation failure | Return `error` — nack, message requeued or DLQ |
| Transient dependency failure (DB down) | Return `error` — nack, message requeued |
| Unrecoverable programming error | Let it panic — caught, logged, message nacked, consumer continues |

---

## Monitoring Recommendations

Log entries from the library use structured key-value pairs. Key fields to monitor:

| Event | Log level | Key fields |
|-------|-----------|------------|
| Connection lost | WARN | `host`, `error` |
| Reconnect attempt | INFO | `host`, `attempt`, `backoff` |
| Reconnect success | INFO | `host` |
| Handler panic | ERROR | `pattern`, `name`, `panic`, `stack` |
| Handler error (nack) | WARN | `pattern`, `name`, `error` |
| Deserialize error | ERROR | `pattern`, `name`, `body`, `error` |

---

## See Also

- [configuration.md](configuration.md) — `WithDLQRetry`, `RetryDelay`, `Logger`
- [testing.md](testing.md) — integration test for panic recovery
- [architecture.md](architecture.md) — connection pool and reconnect internals
