package kafka

import (
	"crypto/tls"
	"log/slog"
	"time"

	kgo "github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Config is the canonical field layout underlying the public kafka.KafkaConfig.
// Keep implementation-only runtime state outside this type to avoid exposing
// it through the public configuration API.
type Config struct {
	AppName          string
	BootstrapBrokers []string
	ClientID         string
	InstanceID       string

	TLS  *tls.Config
	SASL sasl.Mechanism

	TopicNameFunc             func(name string) string
	NotificationTopicNameFunc func(name string) string
	CommandsTopicNameFunc     func(appName string) string
	QueriesTopicNameFunc      func(appName string) string
	RepliesTopicNameFunc      func(appName string) string
	ConsumerGroupPrefix       string

	DisableReplyListener bool

	ProducerAcks         kgo.RequiredAcks
	ProducerBatchTimeout time.Duration
	ProducerCompression  kgo.Compression
	MaxMessageBytes      int

	HandlerTimeout    time.Duration
	MaxRetryAttempts  int
	RetryInitialDelay time.Duration
	RetryMaxDelay     time.Duration
	DLQSuffix         string

	AllowAutoCreateTopics    bool
	DefaultPartitions        int
	DefaultReplicationFactor int

	ConsumerSessionTimeout    time.Duration
	ConsumerHeartbeatInterval time.Duration

	AutoGenerateMissingEventID bool

	Logger        *slog.Logger
	Tracer        trace.Tracer
	MeterProvider metric.MeterProvider
}
