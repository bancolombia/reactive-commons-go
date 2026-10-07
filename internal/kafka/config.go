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

// Config is the internal mirror of kafka.KafkaConfig. Public callers never see
// this type; kafka.NewApplication converts KafkaConfig -> Config before handing
// it to NewKafkaApp.
type Config struct {
	AppName          string
	BootstrapBrokers []string
	ClientID         string
	InstanceID       string

	TLS  *tls.Config
	SASL sasl.Mechanism

	TopicNameFunc             func(name string) string
	NotificationTopicNameFunc func(name string) string
	ConsumerGroupPrefix       string

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
