package kafka

import ikafka "github.com/bancolombia/reactive-commons-go/internal/kafka"

// ErrNotSupportedOnKafka is returned by every DirectAsyncGateway method and by
// HandlerRegistry.ListenCommand / ServeQuery on Kafka-backed applications.
// Commands and async queries are out of scope for this feature.
var ErrNotSupportedOnKafka = ikafka.ErrNotSupportedOnKafka

// ErrTopicMissing is returned by Start when a required topic does not exist
// and KafkaConfig.AllowAutoCreateTopics is false.
var ErrTopicMissing = ikafka.ErrTopicMissing

// ErrBrokerUnreachable is returned by Start when bootstrap brokers cannot be
// dialed.
var ErrBrokerUnreachable = ikafka.ErrBrokerUnreachable

// ErrPayloadTooLarge is returned by Emit / EmitNotification when the marshalled
// envelope exceeds KafkaConfig.MaxMessageBytes.
var ErrPayloadTooLarge = ikafka.ErrPayloadTooLarge
