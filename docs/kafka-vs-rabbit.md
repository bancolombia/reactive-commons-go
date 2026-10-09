# Kafka vs RabbitMQ

Both backends implement the shared `pkg/async` API, but their broker topology and delivery
semantics are not interchangeable. Use this table when migrating; follow the linked pattern guides
for details.

| Pattern | RabbitMQ | Kafka |
|---------|----------|-------|
| Events | Durable topic exchange and a queue per subscriber app; each app gets a copy. Optional delayed DLQ retry. | `{AppName}.{eventName}` topic by default. Replicas in one app group compete; distinct groups can each consume. Handler retries then topic DLQ. No Rabbit-style wildcard topic subscription. |
| Notifications | Transient exclusive queues; current instances receive a copy. Publish does not wait for a publisher confirm. | App-scoped topic and per-instance groups; current instances fan out. Missed notifications are not replayed with a new default instance ID. Publish waits for producer acknowledgement. Handler errors are dropped. |
| Commands | Durable target queue, routed by target app. Optional Rabbit delayed-message/TTL topology. | `{targetApp}.commands` topic, target replicas compete. Handler retries then DLQ; unknown command names are committed and discarded. Delayed commands are unsupported. |
| Queries | Durable request queue and temporary exclusive reply queue; configurable `ReplyTimeout`; optional Rabbit DLQ topology. | `{targetApp}.queries` and caller `{app}.replies` topics. Caller context controls timeout; replies are best-effort. Missing handlers lead to timeout; query handlers have no retry/DLQ path. |
| Handler names | Topic-style wildcard matching for events, commands, and notifications; queries are exact. | Command wildcards work at dispatch but require compatible registrations on every replica. Events use per-event topics; queries are exact. |
| Configuration | `rabbit.RabbitConfig`: exchanges, queues, persistence, `ReplyTimeout`, `QueueType`, and optional DLQ retry. | `kafka.KafkaConfig`: bootstrap brokers, producer acknowledgements, retry limits, topic functions, and topic provisioning. No Rabbit-specific exchange, persistence, or delayed-delivery fields. |
| Java interop | AMQP wire/topology compatibility with `reactive-commons-java` when exchange names match. | A raw Java consumer test verifies event envelope consumption; this does not establish `reactive-commons-java` Kafka compatibility. |

## Migration Notes

- Replacing the constructor and config preserves shared API calls, not broker guarantees. Review
  timeout, fan-out, topic provisioning, and retry behavior before switching.
- Kafka events and notifications default to app-scoped topics. Different application names need
  compatible topic functions to communicate.
- Create or verify Kafka topics before sending to remote targets. `AllowAutoCreateTopics` is
  disabled by default.
- Kafka request/reply is not a durable RPC guarantee. The caller must have its reply listener
  available, and its context deadline determines how long it waits.

## Detailed Guides

- [Kafka setup and limits](kafka.md)
- [Commands](commands.md)
- [Async Queries](async-queries.md)
- [Domain Events](domain-events.md)
- [Notifications](notifications.md)
- [Configuration](configuration.md)
