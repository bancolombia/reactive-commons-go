# async-receiver-responder

Go port of [reactive-commons-java/samples/async/async-receiver-responder](https://github.com/reactive-commons/reactive-commons-java/tree/master/samples/async/async-receiver-responder).

Single service registered as `appName=receiver` that exercises every messaging
pattern reactive-commons-go exposes against an in-memory `Teams` model:

| Pattern        | Routing key            | Payload                                       | Handler              |
| -------------- | ---------------------- | --------------------------------------------- | -------------------- |
| Query          | `get-teams`            | _ignored_                                     | `GetTeamsHandler`    |
| Query          | `get-team-members`     | JSON string (team name)                       | `GetTeamHandler`     |
| Command        | `add-member`           | `{ "teamName": "...", "member": { ... } }`    | `AddMemberHandler`   |
| Event          | `member-removed`       | `{ "teamName": "...", "username": "..." }`    | `RemoveMemberHandler`|
| Event          | `animals.#`            | `{ "name": "...", "type": "..." }`            | `AnimalEventHandler` |
| Notification   | `data-reset`           | _ignored_                                     | `ResetHandler`       |

Wire-compatible with the Java sample: same `appName`, same routing keys, same
JSON payload shapes (Jackson-style camelCase). A Java sender pointed at the
same broker can drive this Go receiver and vice-versa.

## Run

Requires a running RabbitMQ at `localhost:5672` (guest/guest). From the repo
root:

```bash
go run ./examples/async-receiver-responder
```

The example is split across multiple files (`main.go`, `handlers.go`,
`usecase.go`, `model.go`), so `go run main.go` will not work — pass the
package directory or use `go run .` from inside it.

## Configuration parity with the Java sample

The Java [`application.yaml`](https://github.com/reactive-commons/reactive-commons-java/blob/master/samples/async/async-receiver-responder/src/main/resources/application.yaml)
sets:

```yaml
app:
  async:
    app:
      withDLQRetry: true
      queueType: quorum
```

The Go example mirrors both:

```go
cfg.WithDLQRetry = true
cfg.QueueType    = "quorum"
```

These declarations are immutable once the queue exists. If you've previously
run with different values, RabbitMQ will reject redeclaration with
`PRECONDITION_FAILED (406)` — delete the queues first
(`receiver.subsEvents`, `receiver`, `receiver.query`, and their `.DLQ`
siblings) or align both sides on the same values.

## Drive it

The repo ships a Go sender for each pattern that pairs naturally with this
receiver — start any of them in a second terminal:

| Sender                               | Pattern this receiver handles |
| ------------------------------------ | ----------------------------- |
| [`examples/emit-event`](../emit-event)         | Domain events (`member-removed`, `animals.*`) |
| [`examples/send-command`](../send-command)     | Commands (`add-member`)       |
| [`examples/request-reply`](../request-reply)   | Queries (`get-teams`, `get-team-members`) |

You'll need to tweak the sender's routing keys, payloads, and target
`AppName=receiver` to match this example's contract — they ship configured for
their own demo names. Or point the Java
[`async-sender-client`](https://github.com/reactive-commons/reactive-commons-java/tree/master/samples/async/async-sender-client)
at the same broker; it already targets `appName=receiver`.

## Differences from the Java sample

- **No `DynamicRegistry`.** The Java sample registers `animals.#` statically
  AND adds `animals.dogs`, `animals.cats`, `animals.cats.angry` after startup
  via `DynamicRegistry`. reactive-commons-go binds queues during `Start()`
  only. We use `animals.#` exclusively — the Go registry resolves wildcards at
  dispatch time, so every concrete animal key the Java sample subscribes to is
  still delivered here.
- **`AnimalEventHandler` returns an error**, mirroring the Java
  `Mono.error("Not implemented")`. With `WithDLQRetry: true`, failed
  deliveries flow into the events DLQ retry loop instead of being requeued
  immediately.
- **Notifications are not durable.** `data-reset` arrives only at instances
  running when it's published — same semantics as the Java
  `listenNotificationEvent`.
