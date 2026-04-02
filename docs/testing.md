# Testing Guide

`reactive-commons-go` has two test suites:

- **Unit tests** (`tests/unit/`) — no broker required; fast; run continuously during development
- **Integration tests** (`tests/integration/`) — require a RabbitMQ instance; validate real
  broker behaviour using [testcontainers-go](https://golang.testcontainers.org/)

---

## Running the Tests

### Unit Tests (no broker required)

```bash
make test-unit
# or directly:
go test ./tests/unit/... -v -count=1
```

Unit tests cover:
- JSON serialization round-trips and Java fixture compatibility (`converter_test.go`)
- Queue/exchange name generation (`namegen_test.go`)
- Reply router correlation logic (`replyrouter_test.go`)
- Handler registry duplicate detection (`registry_test.go`)
- Java wire-format deserialization (`java_interop_test.go`)

### Integration Tests (requires RabbitMQ)

```bash
make test-integration
# or directly:
go test -tags integration ./tests/integration/... -v -count=1 -timeout 120s
```

Integration tests spin up a real RabbitMQ container via testcontainers-go automatically
(requires Docker, Podman, or nerdctl). They cover:
- Domain event fan-out (`events_test.go`)
- Command point-to-point delivery (`commands_test.go`)
- Async query request/reply round-trip (`queries_test.go`)
- Notification non-durable broadcast (`notifications_test.go`)

### Run Everything

```bash
make test
```

### Point at an Existing Broker

If you already have RabbitMQ running (e.g., in CI), skip the testcontainer startup:

```bash
RABBITMQ_HOST=localhost RABBITMQ_PORT=5672 \
  go test -tags integration ./tests/integration/... -v
```

---

## Test Tags

Integration tests use the build tag `//go:build integration` so they are excluded from
`go test ./...` by default. The `-tags integration` flag opts in.

```bash
go test ./...                          # unit tests only
go test -tags integration ./...        # unit + integration
```

---

## Writing Unit Tests

### Testing Your Event Handlers

```go
package myservice_test

import (
    "context"
    "testing"

    "github.com/bancolombia/reactive-commons-go/pkg/async"
    "github.com/stretchr/testify/assert"
)

type OrderCreated struct {
    OrderID string  `json:"orderId"`
    Amount  float64 `json:"amount"`
}

func TestHandleOrderCreated(t *testing.T) {
    var received *OrderCreated

    handler := func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        received = &e.Data
        return nil
    }

    event := async.DomainEvent[OrderCreated]{
        Name:    "order.created",
        EventID: "test-id",
        Data:    OrderCreated{OrderID: "42", Amount: 99.99},
    }

    err := handler(context.Background(), event)
    assert.NoError(t, err)
    assert.Equal(t, "42", received.OrderID)
    assert.InDelta(t, 99.99, received.Amount, 0.001)
}
```

### Testing Your Query Handlers

```go
func TestHandleGetProduct(t *testing.T) {
    handler := func(
        ctx  context.Context,
        q    async.AsyncQuery[GetProductRequest],
        from async.From,
    ) (Product, error) {
        return Product{SKU: q.QueryData.ProductID, Name: "Widget", Price: 9.99}, nil
    }

    result, err := handler(
        context.Background(),
        async.AsyncQuery[GetProductRequest]{
            Resource:  "get-product",
            QueryData: GetProductRequest{ProductID: "SKU-1"},
        },
        async.From{},
    )
    assert.NoError(t, err)
    assert.Equal(t, "SKU-1", result.SKU)
}
```

---

## Writing Integration Tests

Integration tests use a shared RabbitMQ fixture set up in `TestMain`. The standard pattern is:

```go
//go:build integration

package integration_test

import (
    "context"
    "testing"
    "time"

    "github.com/bancolombia/reactive-commons-go/pkg/async"
    "github.com/bancolombia/reactive-commons-go/rabbit"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestMyEvent(t *testing.T) {
    ctx := context.Background()

    // Create application using the shared broker from TestMain
    cfg := rabbit.NewConfigWithDefaults()
    cfg.AppName  = "test-my-event-" + t.Name() // unique per test
    cfg.Host     = rabbitHost
    cfg.Port     = rabbitPort

    app, err := rabbit.NewApplication(cfg)
    require.NoError(t, err)

    received := make(chan string, 1)
    require.NoError(t, app.Registry().ListenEvent("my-event",
        func(ctx context.Context, e async.DomainEvent[map[string]any]) error {
            if id, ok := e.Data["id"].(string); ok {
                received <- id
            }
            return nil
        },
    ))

    appCtx, cancel := context.WithCancel(ctx)
    defer cancel()
    go func() { _ = app.Start(appCtx) }()
    <-app.Ready()

    require.NoError(t, app.EventBus().Emit(ctx, async.DomainEvent[map[string]any]{
        Name:    "my-event",
        EventID: "ev-001",
        Data:    map[string]any{"id": "payload-123"},
    }))

    select {
    case id := <-received:
        assert.Equal(t, "payload-123", id)
    case <-time.After(5 * time.Second):
        t.Fatal("handler was not invoked within 5s")
    }
}
```

### Isolation: Use Unique AppNames

Each test should use a unique `AppName` to avoid queue/binding collisions between parallel
test runs:

```go
cfg.AppName = "test-" + t.Name() // produces e.g. "test-TestMyEvent"
```

### Waiting for App Readiness

Always `<-app.Ready()` before sending messages in tests. Without this, messages may arrive
before consumers are bound and get dropped.

---

## Java Interop Fixture Tests

`tests/unit/java_interop_test.go` validates that Go-deserialized messages from Java byte
fixtures match expected field values. These tests are the ground-truth compatibility check.

```bash
go test ./tests/unit/ -run TestJavaInterop -v
```

---

## Coverage

```bash
# Unit test coverage
go test ./tests/unit/... -coverprofile=coverage.out -covermode=atomic
go tool cover -html=coverage.out -o coverage.html

# Integration coverage (includes internal packages)
go test -tags integration ./tests/integration/... \
  -coverprofile=coverage.out -covermode=atomic \
  -coverpkg=github.com/bancolombia/reactive-commons-go/internal/rabbit/...
go tool cover -func coverage.out
```

The target coverage for `internal/rabbit/` is ≥ 70%.

---

## CI Example (GitHub Actions)

```yaml
name: test

on: [push, pull_request]

jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - run: go test ./tests/unit/... -v -count=1

  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Integration tests (testcontainers pulls RabbitMQ automatically)
        run: |
          go test -tags integration ./tests/integration/... -v -count=1 -timeout 120s
```

No separate RabbitMQ service definition is needed — testcontainers-go pulls the image and
starts the broker automatically when Docker is available on the runner.

---

## See Also

- [getting-started.md](getting-started.md) — quick setup
- [resilience.md](resilience.md) — panic recovery and DLQ behaviour (covered in events tests)
- [java-interop.md](java-interop.md) — fixture byte tests
