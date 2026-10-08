package kafka

import "errors"

// Sentinel errors shared between the internal implementation and the public
// kafka package. The public package re-exports these so callers get stable
// symbols (kafka.ErrTopicMissing, etc.) while errors.Is works across package
// boundaries.
var (
	ErrTopicMissing      = errors.New("kafka: required topic does not exist")
	ErrBrokerUnreachable = errors.New("kafka: bootstrap brokers unreachable")
	ErrPayloadTooLarge   = errors.New("kafka: payload exceeds MaxMessageBytes")
)
