# Async Queries (Request / Reply)

Async queries implement the request/reply messaging pattern: a caller sends a named query to a
**specific target service** and waits — synchronously — for a typed response, with a configurable
timeout. The transport is entirely asynchronous over AMQP; the synchronous appearance is
achieved by a per-request correlation ID and a temporary reply queue.

## Key Characteristics

| Property | Value |
|----------|-------|
| Request exchange | `directMessages` (direct, durable) |
| Request routing key | `{targetService}.query` |
| Request queue | `{targetService}.query` (durable) |
| Reply exchange | `globalReply` (topic, durable — matches reactive-commons-java) |
| Reply routing key | `{caller}.replies.{uuid}` (unique per app instance) |
| Reply queue | `{caller}.replies.{uuid}` (temp, auto-delete, exclusive) |
| Default timeout | 15 seconds (configurable via `RabbitConfig.ReplyTimeout`) |
| Caller blocks | Yes — `RequestReply` is synchronous from the caller's perspective |

---

## Defining Payload Types

```go
// Request payload
type GetProductRequest struct {
    ProductID string `json:"productId"`
}

// Response payload
type Product struct {
    SKU   string  `json:"sku"`
    Name  string  `json:"name"`
    Price float64 `json:"price"`
}
```

---

## Sending a Query (Caller)

```go
import (
    "context"
    "encoding/json"
    "time"

    "github.com/bancolombia/reactive-commons-go/pkg/async"
)

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

raw, err := app.Gateway().RequestReply(ctx,
    async.AsyncQuery[GetProductRequest]{
        Resource:  "get-product",          // query name
        QueryData: GetProductRequest{ProductID: "SKU-999"},
    },
    "catalog-service",                     // target service AppName
)
if err != nil {
    // async.ErrQueryTimeout   — context deadline exceeded before response arrived
    // async.ErrBrokerUnavailable — broker connection failure
    log.Println("query failed:", err)
    return
}

var product Product
if err := json.Unmarshal(raw, &product); err != nil {
    log.Println("deserialize failed:", err)
    return
}
log.Printf("product: %+v", product)
```

`RequestReply` returns `(json.RawMessage, error)`. You unmarshal the raw bytes into your
response type. This keeps the caller in control of deserialization.

---

## Handling a Query (Server)

Register handlers **before** calling `app.Start()`:

```go
err := app.Registry().ServeQuery("get-product",
    func(
        ctx  context.Context,
        q    async.AsyncQuery[GetProductRequest],
        from async.From,
    ) (Product, error) {
        // look up the product
        p, err := db.FindProduct(ctx, q.QueryData.ProductID)
        if err != nil {
            return Product{}, err // sends error reply
        }
        return p, nil // serialized and sent back to caller
    },
)
```

The `from` argument carries the correlation metadata needed to route the reply. You don't need
to use it directly — the library calls `Gateway().Reply(ctx, response, from)` for you after
your handler returns.

---

## Manual Reply

If you need to reply asynchronously (e.g., after a background lookup), you can call `Reply`
directly using the `from` value captured in your handler:

```go
app.Registry().ServeQuery("get-product",
    func(ctx context.Context, q async.AsyncQuery[GetProductRequest], from async.From) (any, error) {
        go func() {
            product := slowLookup(q.QueryData.ProductID)
            _ = app.Gateway().Reply(context.Background(), product, from)
        }()
        return nil, nil // return nil to suppress automatic reply
    },
)
```

> Returning `(nil, nil)` suppresses the automatic reply. Use this only when you intend to
> call `Reply` manually. Failing to reply will cause the caller to time out.

---

## Wildcards Are Not Supported

Unlike events, commands, and notifications, query handler names **must be
exact strings**. Calling `ServeQuery` with a name containing `*` or `#`
returns `async.ErrWildcardNotSupported`:

```go
err := app.Registry().ServeQuery("get-product.*", handler)
// errors.Is(err, async.ErrWildcardNotSupported) == true
```

This matches `reactive-commons-java`'s `HandlerResolver.addQueryHandler`
guard. Queries route through the **direct** exchange (routing key
`{targetService}.query`), so topic-style patterns can't be expressed at the
broker level and would create cache-conflict ambiguities. Use distinct
resource names per query instead.

---

## Concurrent Queries

Multiple concurrent `RequestReply` calls are safe. Each request gets a unique correlation ID
managed by an in-memory router (`sync.Map`). Replies are matched to their original callers by
that ID.

```go
// Safe to run concurrently
var wg sync.WaitGroup
for i := 0; i < 10; i++ {
    wg.Add(1)
    go func(id string) {
        defer wg.Done()
        raw, err := app.Gateway().RequestReply(ctx,
            async.AsyncQuery[GetProductRequest]{
                Resource:  "get-product",
                QueryData: GetProductRequest{ProductID: id},
            },
            "catalog-service",
        )
        _ = raw
        _ = err
    }("SKU-" + strconv.Itoa(i))
}
wg.Wait()
```

---

## Timeout Handling

The caller's `context.Context` controls the timeout:

```go
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()

_, err := app.Gateway().RequestReply(ctx, query, "catalog-service")
if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, async.ErrQueryTimeout) {
    log.Println("no response within 500ms")
}
```

When a timeout fires:
1. The caller's pending channel is removed from the reply router.
2. Any late reply arriving after the timeout is **silently discarded** — no leak, no panic.

---

## How Correlation Works

```
Caller (order-service)                       Server (catalog-service)
────────────────────────────────────────────────────────────────────
1. Generate correlationID = "abc123"
2. Register chan in ReplyRouter[correlationID]
3. Publish to directMessages
   routing-key: "catalog-service.query"
   headers:
     x-correlation-id:       "abc123"
     x-reply_id:             "order-svc.replies.xyz789"
     x-serveQuery-id:        "get-product"
     x-reply-timeout-millis: "10000"
                                              4. Receive from .query queue
                                              5. Deserialize, invoke handler
                                              6. Publish response to globalReply
                                                 routing-key: "order-svc.replies.xyz789"
                                                 headers:
                                                   x-correlation-id: "abc123"
7. Reply queue delivers to caller
8. ReplyRouter.Route("abc123", body)
9. RequestReply returns raw body
```

---

## Wire Format

### Query Request

```
Exchange:    directMessages
Routing key: catalog-service.query

Body:
{
  "resource":  "get-product",
  "queryData": { "productId": "SKU-999" }
}

Headers:
  x-reply_id:              order-svc.replies.xyz789
  x-correlation-id:        abc123
  x-serveQuery-id:         get-product
  x-reply-timeout-millis:  10000
  sourceApplication:       order-svc
```

### Query Reply

```
Exchange:    globalReply
Routing key: order-svc.replies.xyz789

Body: { "sku": "SKU-999", "name": "Widget", "price": 9.99 }

Headers:
  x-correlation-id: abc123
  sourceApplication: catalog-service
```

A nil response sends `null` body with header `x-empty-completion: true`.

---

## See Also

- [commands.md](commands.md) — fire-and-forget point-to-point
- [java-interop.md](java-interop.md) — query interoperability with Java `@HandleQuery`
- [configuration.md](configuration.md) — `ReplyTimeout` and `PersistentQueries` settings
- [architecture.md](architecture.md) — internals of the reply router
