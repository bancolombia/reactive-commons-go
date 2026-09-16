package kafka

import (
	"context"
	"strconv"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// dlqProducer publishes exhausted messages to <origTopic><cfg.DLQSuffix> with
// the required x-dlq-* headers plus x-dlq-source-service. It reuses the shared
// producer so writers are not duplicated per listener.
type dlqProducer struct {
	cfg      Config
	producer *producer
}

func newDLQProducer(cfg Config, p *producer) *dlqProducer {
	return &dlqProducer{cfg: cfg, producer: p}
}

// publish routes the original message bytes to the DLQ. attempts is the number
// of times the handler was invoked; reason is a short, non-empty diagnostic.
func (d *dlqProducer) publish(ctx context.Context, origTopic string, orig kgo.Message, attempts int, reason string) error {
	dlqTopic := origTopic + d.cfg.DLQSuffix

	origHeaders := make(map[string]string, len(orig.Headers))
	for _, h := range orig.Headers {
		origHeaders[h.Key] = string(h.Value)
	}
	origHeaders["x-dlq-reason"] = reason
	origHeaders["x-dlq-attempts"] = strconv.Itoa(attempts)
	origHeaders["x-dlq-origin-topic"] = origTopic
	origHeaders["x-dlq-first-seen"] = time.Now().UTC().Format(time.RFC3339)
	origHeaders["x-dlq-source-service"] = d.cfg.AppName

	return d.producer.publish(ctx, dlqTopic, orig.Key, orig.Value, origHeaders)
}
