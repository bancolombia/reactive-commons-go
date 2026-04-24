# Java Interoperability

`reactive-commons-go` is **wire-compatible** with
[reactive-commons-java](https://github.com/reactive-commons/reactive-commons-java).
Go and Java services communicate on the same broker with zero extra configuration as long
as they share the same exchange names (the defaults match).

---

## How It Works

Both implementations use:

- The same AMQP exchange topology with the same types and durability:
  - `domainEvents` — topic, durable
  - `directMessages` — direct, durable
  - `globalReply` — topic, durable
- The same JSON message envelope field names (verified against Jackson serialization)
- The same AMQP header names for query correlation
- The same DLQ/retry topology when `WithDLQRetry=true` (per-app retry exchange
  for events; shared `{directMessages}.DLQ` for commands and queries; TTL-based
  redelivery)

No adapter, bridge, or SDK shim is required. Mixing Go and Java services is transparent.

---

## Pattern Interoperability Matrix

| Java side | Go side |
|-----------|---------|
| `domainEventBus.emit(event)` | `app.Registry().ListenEvent(...)` handler fires |
| `@HandleEvent` | `app.EventBus().Emit(...)` |
| `asyncGateway.sendCommand(cmd, target)` | `app.Registry().ListenCommand(...)` handler fires |
| `@HandleCommand` | `app.Gateway().SendCommand(...)` |
| `asyncGateway.requestReply(query, target)` | `app.Registry().ServeQuery(...)` handler fires |
| `@HandleQuery` | `app.Gateway().RequestReply(...)` |
| Notification broadcast | `app.Registry().ListenNotification(...)` handler fires |
| `@HandleNotification` | `app.EventBus().EmitNotification(...)` |
| Wildcard names (`order.*`, `inventory.#`) on events/commands/notifications | Same — see `internal/matcher` (port of Java `KeyMatcher`); queries reject wildcards on both sides |

---

## JSON Field Name Mapping

The most common source of interoperability issues is mismatched JSON field names. Go struct
tags must match the Jackson-serialized field names exactly:

### Domain Event / Notification

| Java field | Go JSON tag | Wire key |
|------------|-------------|----------|
| `name` | `json:"name"` | `name` |
| `eventId` | `json:"eventId"` | `eventId` |
| `data` | `json:"data"` | `data` |

### Command

| Java field | Go JSON tag | Wire key |
|------------|-------------|----------|
| `name` | `json:"name"` | `name` |
| `commandId` | `json:"commandId"` | `commandId` |
| `data` | `json:"data"` | `data` |

### Query Request

| Java field | Go JSON tag | Wire key |
|------------|-------------|----------|
| `resource` | `json:"resource"` | `resource` |
| `queryData` | `json:"queryData"` | `queryData` |

> **Common mistakes**: using `event_id` instead of `eventId`, `command_id` instead of
> `commandId`, `query_data` instead of `queryData`. These will silently produce empty fields.

---

## Payload Type Mapping

Your application-defined payload types in Go must use JSON tags that match the Java DTO
field names. Java's Jackson defaults to camelCase, so:

```go
// Java DTO: class OrderCreated { String orderId; double amount; }
// Go equivalent:
type OrderCreated struct {
    OrderID string  `json:"orderId"` // matches Java camelCase
    Amount  float64 `json:"amount"`
}
```

---

## Java Fixture Byte Verification

The following JSON payloads are produced by `reactive-commons-java`. The Go library's unit
tests verify that these deserialize correctly:

### Domain Event

```json
{"name":"order.created","eventId":"d290f1ee-6c54-4b01-90e6-d701748f0851","data":{"orderId":"42","amount":100}}
```

```go
// Verified in tests/unit/java_interop_test.go
var e async.DomainEvent[OrderCreated]
json.Unmarshal([]byte(`{"name":"order.created","eventId":"d290f1ee-...","data":{"orderId":"42","amount":100}}`), &e)
// e.Name    = "order.created"
// e.EventID = "d290f1ee-6c54-4b01-90e6-d701748f0851"
// e.Data    = OrderCreated{OrderID: "42", Amount: 100}
```

### Command

```json
{"name":"send-invoice","commandId":"a3bb189e-8bf9-3888-9912-ace4e6543002","data":{"invoiceId":"INV-001"}}
```

### Query Request

```json
{"resource":"get-product","queryData":{"productId":"SKU-999"}}
```

---

## AMQP Header Compatibility

Query correlation headers are defined in `pkg/headers/headers.go` and match Java's
`Headers.java` exactly:

| Go constant | Header key | Java constant |
|-------------|------------|---------------|
| `headers.ReplyID` | `x-reply_id` | `Headers.REPLY_ID` |
| `headers.CorrelationID` | `x-correlation-id` | `Headers.CORRELATION_ID` |
| `headers.ServedQueryID` | `x-serveQuery-id` | `Headers.SERVED_QUERY_ID` |
| `headers.ReplyTimeoutMillis` | `x-reply-timeout-millis` | `Headers.REPLY_TIMEOUT_MILLIS` |
| `headers.CompletionOnlySignal` | `x-empty-completion` | `Headers.COMPLETION_ONLY_SIGNAL` |
| `headers.SourceApplication` | `sourceApplication` | `Headers.SOURCE_APPLICATION` |

---

## Exchange and Queue Name Alignment

Go default exchange names (from `rabbit.NewConfigWithDefaults()`) match the Java Spring Boot
starter defaults:

| Purpose | Default name | Java property |
|---------|-------------|---------------|
| Domain events | `domainEvents` | `app.async.rabbit.domain-events-exchange` |
| Commands + Queries | `directMessages` | `app.async.rabbit.direct-messages-exchange` |
| Query replies | `globalReply` | `app.async.rabbit.global-reply-exchange` |

If your Java services use custom exchange names, set them explicitly in Go:

```go
cfg := rabbit.NewConfigWithDefaults()
cfg.AppName                = "my-go-service"
cfg.DomainEventsExchange   = "my-domain-events"
cfg.DirectMessagesExchange = "my-direct-messages"
cfg.GlobalReplyExchange    = "my-global-reply"
```

---

## Mixed Deployment Example

### Java publisher → Go subscriber

```java
// Java (reactive-commons-java)
@Autowired DomainEventBus bus;

bus.emit(new DomainEvent<>("order.created", UUID.randomUUID().toString(),
    new OrderCreated("42", 99.99)));
```

```go
// Go subscriber
app.Registry().ListenEvent("order.created",
    func(ctx context.Context, e async.DomainEvent[OrderCreated]) error {
        log.Println("received from Java:", e.Data.OrderID)
        return nil
    },
)
```

### Go publisher → Java subscriber

```go
// Go publisher
app.EventBus().Emit(ctx, async.DomainEvent[OrderCreated]{
    Name:    "order.created",
    EventID: uuid.New().String(),
    Data:    OrderCreated{OrderID: "42", Amount: 99.99},
})
```

```java
// Java subscriber (reactive-commons-java)
@HandleEvent("order.created")
public Mono<Void> handleOrderCreated(DomainEvent<OrderCreated> event) {
    log.info("received from Go: {}", event.getData().getOrderId());
    return Mono.empty();
}
```

### Go caller → Java query handler

```go
// Go caller
raw, err := app.Gateway().RequestReply(ctx,
    async.AsyncQuery[GetProductRequest]{Resource: "get-product", QueryData: GetProductRequest{ProductID: "SKU-999"}},
    "catalog-service",
)
```

```java
// Java query handler (reactive-commons-java)
@HandleQuery("get-product")
public Mono<Product> handleGetProduct(AsyncQuery<GetProductRequest> query) {
    return productService.findById(query.getQueryData().getProductId());
}
```

---

## Troubleshooting

| Symptom | Likely cause |
|---------|-------------|
| Go handler never fires for Java events | Exchange name mismatch; check `DomainEventsExchange` |
| `eventId` is empty after deserialization | Go struct uses `event_id` tag instead of `eventId` |
| Query response is always nil | JSON field `queryData` spelled as `query_data` in Go request |
| Java handler never fires for Go events | `AppName` queue not bound; ensure Java service uses same exchange |
| Random bytes in payload | Content-type mismatch; both sides must use `application/json` |

---

## See Also

- [domain-events.md](domain-events.md) — event publishing/subscribing details
- [commands.md](commands.md) — command routing
- [async-queries.md](async-queries.md) — query/reply mechanics
- [configuration.md](configuration.md) — exchange name overrides
