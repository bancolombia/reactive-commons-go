package kafka

import (
	"context"
	"fmt"

	kgo "github.com/segmentio/kafka-go"
)

// libraryVersion is the value emitted in the reactive-commons-version header.
// Bumped in lockstep with the module version when the wire schema changes.
const libraryVersion = "0.1.0"

// producer wraps a *kafka.Writer configured from the internal Config. It is
// broker-address-agnostic: the topic is passed per message, so a single writer
// serves both event and notification emissions.
type producer struct {
	w   *kgo.Writer
	cfg Config
}

func newProducer(cfg Config) *producer {
	w := &kgo.Writer{
		Addr:                   kgo.TCP(cfg.BootstrapBrokers...),
		Balancer:               &kgo.Hash{},
		RequiredAcks:           cfg.ProducerAcks,
		BatchTimeout:           cfg.ProducerBatchTimeout,
		Compression:            cfg.ProducerCompression,
		AllowAutoTopicCreation: cfg.AllowAutoCreateTopics,
		Transport: &kgo.Transport{
			TLS:  cfg.TLS,
			SASL: cfg.SASL,
		},
	}
	return &producer{w: w, cfg: cfg}
}

// publish blocks until Kafka acknowledges the write per the configured acks
// level or ctx is cancelled.
func (p *producer) publish(ctx context.Context, topic string, key, value []byte, headers map[string]string) error {
	hh := make([]kgo.Header, 0, len(headers))
	for k, v := range headers {
		hh = append(hh, kgo.Header{Key: k, Value: []byte(v)})
	}
	msg := kgo.Message{
		Topic:   topic,
		Key:     key,
		Value:   value,
		Headers: hh,
	}
	if err := p.w.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("kafka: publish %s: %w", topic, err)
	}
	return nil
}

func (p *producer) close() error {
	if p == nil || p.w == nil {
		return nil
	}
	return p.w.Close()
}
