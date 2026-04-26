# Configuration Reference

All configuration is provided via `rabbit.RabbitConfig` — a plain Go struct with no hidden
global state. Use `rabbit.NewConfigWithDefaults()` to start from sensible defaults and
override only what you need.

---

## Quick Reference

```go
cfg := rabbit.NewConfigWithDefaults()

// Required
cfg.AppName = "my-service"

// Optional overrides from defaults
cfg.Host        = "rabbitmq.prod.internal"
cfg.Port        = 5672
cfg.Username    = "app-user"
cfg.Password    = os.Getenv("RABBITMQ_PASSWORD")
cfg.VirtualHost = "/production"

app, err := rabbit.NewApplication(cfg)
```

---

## Full Field Reference

### Connection

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Host` | `string` | `"localhost"` | RabbitMQ hostname or IP address |
| `Port` | `int` | `5672` | AMQP port (use 5671 for TLS) |
| `Username` | `string` | `"guest"` | AMQP username |
| `Password` | `string` | `"guest"` | AMQP password |
| `VirtualHost` | `string` | `"/"` | AMQP virtual host |

### Identity

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `AppName` | `string` | **required** | Service name. Used for queue naming (`{AppName}.subsEvents`, `{AppName}` commands queue, `{AppName}.query`, etc.) |
| `ConnectionName` | `string` | `AppName` | Advertised to RabbitMQ as the AMQP `connection_name` client property. Shows up in the Management UI Connections tab and broker logs. Override per-instance (e.g. `{AppName}-{podName}`) to distinguish replicas. |

`AppName` must be non-empty — `NewApplication` returns an error if it is missing.

#### Setting `ConnectionName` per-pod on Kubernetes

When you run multiple replicas, every pod will report the same `connection_name`
(its `AppName`) on the broker — making it hard to tell replicas apart in the
RabbitMQ Management UI. Use the [Kubernetes downward API](https://kubernetes.io/docs/concepts/workloads/pods/downward-api/)
to expose the pod name as an env var, then read it from Go and stitch it into
`ConnectionName`.

**1. Expose pod metadata in your Deployment/StatefulSet:**

```yaml
spec:
  template:
    spec:
      containers:
        - name: my-service
          env:
            - name: APP_NAME
              value: "my-service"
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_NAMESPACE
              valueFrom:
                fieldRef:
                  fieldPath: metadata.namespace
```

`metadata.name` resolves to e.g. `my-service-7d9c8f6b4-xk2lp` (Deployment) or
the stable `my-service-0` (StatefulSet).

**2. Build `ConnectionName` from those env vars:**

```go
import (
    "fmt"
    "os"

    "github.com/bancolombia/reactive-commons-go/rabbit"
)

func buildConnectionName(appName string) string {
    if pod := os.Getenv("POD_NAME"); pod != "" {
        return fmt.Sprintf("%s@%s", appName, pod)
    }
    if host, err := os.Hostname(); err == nil && host != "" {
        return fmt.Sprintf("%s@%s", appName, host)
    }
    return appName
}

cfg := rabbit.NewConfigWithDefaults()
cfg.AppName = os.Getenv("APP_NAME")
cfg.ConnectionName = buildConnectionName(cfg.AppName)
```

The Management UI's Connections tab will then list each replica separately,
e.g. `my-service@my-service-7d9c8f6b4-xk2lp`. Combine with `POD_NAMESPACE`
when several namespaces share one RabbitMQ cluster:

```go
cfg.ConnectionName = fmt.Sprintf("%s/%s/%s",
    cfg.AppName,
    os.Getenv("POD_NAMESPACE"),
    os.Getenv("POD_NAME"),
)
```

**Note:** in Kubernetes the container's hostname already equals the pod name by
default, so `os.Hostname()` works as a no-manifest fallback. The library
automatically records `host` from `os.Hostname()` in the AMQP client-properties
table regardless, but the explicit `POD_NAME` env var is the recommended
contract for production manifests.


### Exchange Names

| Field | Type | Default | Java Equivalent |
|-------|------|---------|-----------------|
| `DomainEventsExchange` | `string` | `"domainEvents"` | `app.async.rabbit.domain-events-exchange` |
| `DirectMessagesExchange` | `string` | `"directMessages"` | `app.async.rabbit.direct-messages-exchange` |
| `GlobalReplyExchange` | `string` | `"globalReply"` | `app.async.rabbit.global-reply-exchange` |

Change these only when your Java services use custom exchange names. Default values ensure
zero-configuration interoperability with `reactive-commons-java`.

### Behaviour

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `PrefetchCount` | `int` | `250` | AMQP consumer prefetch (QoS). Higher values increase throughput but increase in-flight message count. |
| `ReplyTimeout` | `time.Duration` | `15s` | Default timeout for `RequestReply` calls. Can be overridden per-call via `context.WithTimeout`. |
| `PersistentEvents` | `bool` | `true` | Publish domain events with delivery-mode 2 (persistent). Survives broker restart. |
| `PersistentCommands` | `bool` | `true` | Publish commands with delivery-mode 2 (persistent). |
| `PersistentQueries` | `bool` | `false` | Publish query requests with delivery-mode 2. Usually transient is fine. |
| `QueueType` | `string` | `"classic"` | RabbitMQ queue type for durable consumer queues and their DLQs. Allowed values: `"classic"` or `"quorum"`. Mirrors reactive-commons-java's `app.async.app.queueType`. |

#### Queue Type Notes

`QueueType` only affects durable consumer and DLQ queues
(`{appName}.subsEvents`, `{appName}`, `{appName}.query`, and their `.DLQ`
siblings when `WithDLQRetry` is enabled). Temporary queues — the per-instance
reply queue and the notification fan-out queue — are always classic, because
they are declared exclusive auto-delete and quorum queues do not support those
flags.

`x-queue-type` is recorded on the queue at declaration time and is immutable
for the queue's lifetime. If a queue with the same name already exists with a
different type, the broker rejects redeclaration with `PRECONDITION_FAILED`
(code 406). To switch types, delete the existing queue first.

### Dead-Letter Queue (DLQ) Retry

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `WithDLQRetry` | `bool` | `false` | Declare delayed-retry DLQ topology for events, commands and queries. Matches reactive-commons-java's behaviour. |
| `RetryDelay` | `time.Duration` | `1s` | DLQ message TTL before redelivery (requires `WithDLQRetry: true`). |

When `WithDLQRetry` is enabled the topology mirrors reactive-commons-java:

- Events: `{appName}.{domainEventsExchange}` (topic, retry exchange),
  `{appName}.{domainEventsExchange}.DLQ` (topic, DLQ exchange) and
  `{appName}.subsEvents.DLQ` (DLQ queue with `x-message-ttl`).
- Commands and queries: `{directMessagesExchange}.DLQ` (direct, shared DLQ
  exchange), plus per-queue DLQ queues `{appName}.DLQ` and `{appName}.query.DLQ`
  with `x-message-ttl` that dead-letter back to `directMessages`.
- Listeners nack failed deliveries with `requeue=false`, so the broker dead-
  letters them through the DLQ. Without `WithDLQRetry` the listeners requeue
  immediately (infinite retry — only suitable for development).

### Observability

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Logger` | `*slog.Logger` | `slog.Default()` | Structured logger for connection events, reconnect attempts, handler panics, and errors. Set to a custom `slog.Logger` to integrate with your log pipeline. |

---

## Environment Variable Pattern

There is no built-in env-var loading; you wire it yourself:

```go
func configFromEnv() rabbit.RabbitConfig {
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName = requireEnv("APP_NAME")

    if h := os.Getenv("RABBITMQ_HOST"); h != "" {
        cfg.Host = h
    }
    if p := os.Getenv("RABBITMQ_PORT"); p != "" {
        port, _ := strconv.Atoi(p)
        cfg.Port = port
    }
    cfg.Username = getEnvOrDefault("RABBITMQ_USERNAME", "guest")
    cfg.Password = requireEnv("RABBITMQ_PASSWORD")
    cfg.VirtualHost = getEnvOrDefault("RABBITMQ_VHOST", "/")
    return cfg
}

func requireEnv(key string) string {
    v := os.Getenv(key)
    if v == "" {
        log.Fatalf("required env var %s is not set", key)
    }
    return v
}

func getEnvOrDefault(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}
```

---

## Custom Logger

```go
import "log/slog"

logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

cfg := rabbit.NewConfigWithDefaults()
cfg.AppName = "my-service"
cfg.Logger  = logger
```

All internal log calls use structured key-value pairs compatible with any `slog.Handler`.

---

## Validation

`NewApplication` calls `cfg.Validate()` and returns an error if:

- `AppName` is empty (`"reactive-commons: RabbitConfig.AppName is required"`)

You can call `Validate()` directly for early checks:

```go
if err := cfg.Validate(); err != nil {
    log.Fatal(err)
}
```

---

## Complete Configuration Example

```go
package main

import (
    "log/slog"
    "os"
    "time"

    "github.com/bancolombia/reactive-commons-go/rabbit"
)

func buildConfig() rabbit.RabbitConfig {
    return rabbit.RabbitConfig{
        // Connection
        Host:        getEnv("RABBITMQ_HOST", "localhost"),
        Port:        5672,
        Username:    getEnv("RABBITMQ_USER", "guest"),
        Password:    getEnv("RABBITMQ_PASS", "guest"),
        VirtualHost: getEnv("RABBITMQ_VHOST", "/"),

        // Identity
        AppName: "payment-service",

        // Exchange names (using java-compatible defaults)
        DomainEventsExchange:   "domainEvents",
        DirectMessagesExchange: "directMessages",
        GlobalReplyExchange:    "globalReply",

        // Behaviour
        PrefetchCount:      500,              // higher throughput
        ReplyTimeout:       30 * time.Second, // lenient query timeout
        PersistentEvents:   true,
        PersistentCommands: true,
        PersistentQueries:  false,
        QueueType:          "classic", // or "quorum" for HA in clusters

        // DLQ
        WithDLQRetry: true,
        RetryDelay:   5 * time.Second,

        // Logging
        Logger: slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
            Level: slog.LevelInfo,
        })),
    }
}

func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}
```

---

## See Also

- [getting-started.md](getting-started.md) — minimal setup
- [resilience.md](resilience.md) — DLQ retry and auto-reconnect details
- [java-interop.md](java-interop.md) — matching Java exchange names
