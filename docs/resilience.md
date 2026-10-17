# Resilience & Error Handling

`reactive-commons-go` provides resilience mechanisms for both brokers, but their behavior is
backend-specific. The reconnect and AMQP dead-letter topology below describe RabbitMQ; Kafka
behavior is summarized in [Kafka Resilience](#kafka-resilience).

---

## RabbitMQ Auto-Reconnect

The library automatically reconnects to RabbitMQ when the broker connection drops.

### How It Works

1. The AMQP client detects a connection error (`amqp.ErrClosed` or similar network failure).
2. The library waits using **exponential backoff**: 1 s → 2 s → 4 s → 8 s → … → 30 s cap.
3. After re-establishing the connection, it:
   - Rebuilds the publisher channel (before restarting consumers, so replies work
     the instant a listener comes back)
   - Re-declares all exchanges and queues (topology is idempotent)
   - Restarts all consumer goroutines
4. If re-declaring topology or restarting consumers fails, the library keeps
   retrying with exponential backoff (1 s → 30 s cap) and logs every failed
   attempt at ERROR level — a half-restored app must never look healthy.
   Publishing stays available throughout, so messages queue broker-side and
   are consumed once the consumers return.

### What Callers Experience

| Component | Behaviour during outage |
|-----------|------------------------|
| `Emit` / `SendCommand` | Returns `async.ErrBrokerUnavailable` promptly when the publish or its broker confirm fails (connection lost, channel closed by the broker, or broker nack) |
| `RequestReply` | Returns `async.ErrBrokerUnavailable` if the send fails, `ErrQueryTimeout` if no reply arrives |
| Consumer goroutines | Paused; automatically resume after reconnect or after a broker-initiated consumer cancel/channel close (retrying if the topology cannot be restored immediately) |
| In-flight messages at handler level | Message re-delivered by broker after reconnect (at-least-once) |

### Sender Channel Self-Healing

The publisher channel has its own failure path, independent of the connection:

- When the broker closes the publisher channel (e.g. a 404 from publishing to
  a missing exchange, or an operator action), every in-flight `SendWithConfirm`
  waiter is released immediately with `async.ErrBrokerUnavailable` — callers
  never block until their context expires.
- If the connection is still healthy, the channel is rebuilt with exponential
  backoff (1 s → 30 s cap), **retrying until it succeeds** — a transient
  rebuild failure cannot leave the sender permanently dead. Publishing
  recovers without a connection drop; sends made during the rebuild window
  fail fast with an error the caller can retry.
- If the rebuild fails right after a connection-level reconnect, the sender
  keeps retrying in the background the same way — publishing can never stay
  broken until the *next* connection drop.
- If the connection is down, the regular reconnect flow rebuilds the channel.
- Sends are serialized through a context-aware publish slot: a send queued
  behind a stalled publish (e.g. a wedged TCP write) returns at its own
  context deadline instead of hanging on the broker's write.
- Every socket write is bounded by `WriteTimeout` (default 30 s). amqp091
  cannot interrupt an in-flight write when the caller's context is cancelled,
  so without this bound a wedged TCP write could hold the publisher slot
  indefinitely. When the write deadline fires, the connection is torn down and
  the reconnect flow recovers. The send that was writing may wait up to
  `WriteTimeout` even if its own context is shorter; sends queued behind it
  honor their own contexts.

`SendWithConfirm` is therefore *send-or-error*: every call either has the
message confirmed by the broker or returns an error. The one documented
exception is `EmitNotification`, which is fire-and-forget by design (a
notification published while no listener is bound is a valid outcome). A
notification that races a broker-initiated channel close can be lost, but the
next send fails fast and the channel self-heals.

### Consumer Channel Self-Healing

Each consumer (events, commands, queries, replies, notifications) supervises
its own channel, mirroring the sender:

- When the broker closes a consumer's channel or cancels its consumer while
  the connection is still healthy (e.g. an operator deletes the queue), the
  listener logs the loss at ERROR level, re-declares topology, and rebuilds
  the channel with exponential backoff (1 s → 30 s cap) — without dropping
  the connection and without touching the other listeners.
- Re-declaring topology on every rebuild attempt means a queue deleted while
  the connection stayed up is recreated, so consumption resumes even though
  the connection never dropped.
- If the connection is down, the supervisor stays out of the way and the
  regular reconnect flow restores the consumer.
- Starting a listener again (e.g. a reconnect restore racing a self-heal)
  supersedes the previous supervisor and waits for it to fully exit — closing
  its channel and finishing any in-flight dispatch — before opening the new
  consumer, so two consumers can never be active on the same queue at once.
- The wait for a superseded supervisor is bounded (30 s). If a handler is still
  running in dispatch, the replacement starts anyway and a WARN is logged. This
  trades a possible duplicate delivery of one unacked message (already permitted
  by at-least-once) for guaranteed recovery: a stuck handler can no longer keep
  a consumer — and every listener started after it — down forever.
- Every listener is attempted during a restore, even if another listener fails
  to start; the errors are joined and the restore retry loop logs them.

A consumer can therefore no longer die silently: either it recovers on its
own, or every failed rebuild attempt is logged at ERROR level.

### Live Health and Readiness

`Ready()` is a **one-shot** signal: it closes when startup completes and never
reopens during a later outage. Use `Health()` for live state:

```go
h := app.Health()
if !h.Healthy {
    log.Printf("degraded: connection=%s lastError=%q event=%s",
        h.Connection.State, h.Connection.LastError, h.Consumers["event"].State)
}
```

| Connection state | Meaning |
|------------------|---------|
| `idle` | Not started yet |
| `connected` | AMQP connection is established |
| `reconnecting` | Connection lost; connect attempts are running |
| `stopped` | Application shut down (terminal) |

| Consumer state | Meaning |
|----------------|---------|
| `running` | Consuming deliveries |
| `rebuilding` | Channel is being opened/reopened |
| `stopped` | Not consuming |

`Healthy` is true only when the connection is `connected` and every configured
consumer is `running`. The `Consumers` map is keyed by listener kind (`event`,
`command`, `query`, `reply`, `notification`); `reply` is always present after
startup, the others only when configured. `Health()` is a local snapshot, not a
broker probe: states can change immediately after it returns, and a handler
hung in dispatch still reports `running`.

Shutting the application down (`ctx` cancellation → `Close`) also aborts any
in-flight reconnect attempt — no connection is re-established after shutdown.

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

## RabbitMQ Graceful Shutdown

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

## RabbitMQ Panic Recovery

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

## Kafka Resilience

Kafka does not use RabbitMQ's AMQP connection supervisor or exchange/queue DLQ topology. Kafka
readers retry fetch errors after a fixed delay. Handler retry behavior depends on the pattern:

| Pattern | Kafka behavior |
|---------|----------------|
| Events and commands | Handler failures retry up to `MaxRetryAttempts` (5 by default), with backoff from `RetryInitialDelay` up to `RetryMaxDelay`; exhausted messages go to `{topic}.dlq` |
| Queries | Handler errors are returned to the requester as error replies; requests are committed without retry or DLQ |
| Notifications | Handler errors are logged and dropped; no retry or DLQ |
| Handler duration | Command and query handlers are bounded by `HandlerTimeout` (30 seconds by default) |
| Shutdown | Waits for in-flight handlers up to `HandlerTimeout` |

Kafka query replies are best-effort. If a reply is missed, the caller eventually receives a
context timeout; there is no RabbitMQ-style `ReplyTimeout` setting. See
[async-queries.md](async-queries.md#kafka-behavior-and-limitations) for details.

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

When `WithDLQRetry: true`, the topology mirrors reactive-commons-java:

| Declared resource | Type | Used for |
|-------------------|------|----------|
| `{appName}.{domainEvents}` | topic exchange | Per-app retry exchange for events |
| `{appName}.{domainEvents}.DLQ` | topic exchange | Per-app DLQ exchange for events |
| `{appName}.subsEvents.DLQ` | durable queue | Failed events; `x-message-ttl=RetryDelay`, dead-letters back to the retry exchange |
| `{directMessages}.DLQ` | direct exchange | Shared DLQ exchange for commands and queries |
| `{appName}.DLQ` | durable queue | Failed commands; TTL → re-publishes to `directMessages` with the original routing key |
| `{appName}.query.DLQ` | durable queue | Failed queries; same TTL pattern as commands |

Origin queues (events, commands, queries) are declared with
`x-dead-letter-exchange` pointing at their DLQ exchange.

### DLQ Flow (Java-equivalent retry loop)

1. Handler returns an error → listener nacks with `requeue=false`.
2. Broker dead-letters the message to the configured DLQ exchange.
3. The DLQ queue holds it for `RetryDelay` ms (per-message TTL).
4. TTL expires → broker dead-letters it to the retry/origin exchange, which
   routes it back to the original queue for another attempt.

This delays redelivery instead of looping immediately. Without `WithDLQRetry`,
the listeners requeue failed messages immediately (suitable only for
development).

---

## Error Types

| Type | Description |
|------|-------------|
| `async.ErrDuplicateHandler` | `ListenEvent`, `ListenCommand`, `ServeQuery`, or `ListenNotification` called twice for the same name |
| `async.ErrQueryTimeout` | `RequestReply` context deadline exceeded before a response was received |
| `async.ErrBrokerUnavailable` | Publish failed or its broker confirm never arrived (connection or publisher channel lost, broker nack) — returned by `Emit`, `SendCommand`, `RequestReply`, and `Reply` |
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
| Consumer channel lost / cancelled | ERROR | `listener`, `queue`, `error` / `consumerTag` |
| Consumer channel rebuilt | INFO | `listener`, `queue` |
| Failed consumer rebuild attempt | ERROR | `listener`, `queue`, `error`, `delay` |
| Previous consumer still dispatching after supersede wait | WARN | `listener`, `queue`, `wait` |
| Handler panic | ERROR | `pattern`, `name`, `panic`, `stack` |
| Handler error (nack) | WARN | `pattern`, `name`, `error` |
| Deserialize error | ERROR | `pattern`, `name`, `body`, `error` |

---

## See Also

- [configuration.md](configuration.md) — `WithDLQRetry`, `RetryDelay`, `Logger`
- [testing.md](testing.md) — integration test for panic recovery
- [architecture.md](architecture.md) — connection pool and reconnect internals
