# kafka-emit-event-cloudevent

Variant of `kafka-emit-event` that embeds a CloudEvents 1.0 object inside the
reactive-commons envelope's `data` field.

## What lands on the wire

The outer wire object is still the reactive-commons envelope:

```json
{
  "name": "user.created",
  "eventId": "<uuid>",
  "data": {
    "specversion": "1.0",
    "id": "<uuid>",
    "source": "billing",
    "type": "user.created",
    "time": "2026-09-15T12:00:00Z",
    "datacontenttype": "application/json",
    "data": { "userId": "u-42", "email": "u42@example.com" }
  }
}
```

The `data` field carries the CloudEvent; the framework treats it as opaque
JSON. No consumer changes are required — a plain reactive-commons consumer
receives the same envelope and can dig into `data.data` for the domain
payload.

## Run

```sh
export KAFKA_BOOTSTRAP=localhost:9092
go run ./examples/kafka-emit-event-cloudevent
```

Verify from another shell:

```sh
kcat -b localhost:9092 -t billing.user.created -C -o beginning -e -q -J | jq .
```

See [quickstart Scenario 7](../../specs/001-kafka-broker-support/quickstart.md)
for the full recipe.
