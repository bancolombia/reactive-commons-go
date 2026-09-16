package kafka

import "errors"

// Sentinel errors shared between the internal implementation and the public
// kafka package. The public package re-exports these so callers get stable
// symbols (kafka.ErrNotSupportedOnKafka, etc.) while errors.Is works across
// package boundaries.
var (
	ErrNotSupportedOnKafka = errors.New("kafka: commands and async queries are not supported")
	ErrTopicMissing        = errors.New("kafka: required topic does not exist")
	ErrBrokerUnreachable   = errors.New("kafka: bootstrap brokers unreachable")
	ErrPayloadTooLarge     = errors.New("kafka: payload exceeds MaxMessageBytes")
)
