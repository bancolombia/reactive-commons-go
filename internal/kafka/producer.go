package kafka

import (
	"context"
	"errors"
	"fmt"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// libraryVersion is the value emitted in the reactive-commons-version header.
// Bumped in lockstep with the module version when the wire schema changes.
const libraryVersion = "0.1.0"

// messageWriter is the subset of *kgo.Writer the producer needs. The seam
// exists so publish retry behavior can be unit-tested without a broker.
type messageWriter interface {
	WriteMessages(ctx context.Context, msgs ...kgo.Message) error
	Close() error
}

// producer wraps a *kafka.Writer configured from the internal Config. It is
// broker-address-agnostic: the topic is passed per message, so a single writer
// serves both event and notification emissions.
type producer struct {
	w   messageWriter
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
// level or ctx is cancelled. Transient topic-metadata errors are retried with
// the configured backoff: kafka-go's Writer surfaces them immediately from its
// per-write metadata lookup (before its internal batch-retry machinery), which
// otherwise fails writes racing a just-created topic on a busy broker.
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

	attempts := p.cfg.MaxRetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
retryLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			backoff := retryBackoff(attempt-1, p.cfg.RetryInitialDelay, p.cfg.RetryMaxDelay)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				break retryLoop
			}
		}
		lastErr = p.w.WriteMessages(ctx, msg)
		if lastErr == nil || !isTransientTopicError(lastErr) {
			break
		}
	}
	if lastErr == nil {
		return nil
	}
	return fmt.Errorf("kafka: publish %s: %w", topic, lastErr)
}

// isTransientTopicError reports whether err is a transient topic-metadata
// error that a bounded retry can recover from: a topic that was just created
// but is not yet visible on the broker (UnknownTopicOrPartition) or a
// partition whose election is still in flight (LeaderNotAvailable). Standard
// Kafka producer behavior is to refresh metadata and retry these.
func isTransientTopicError(err error) bool {
	var kerr kgo.Error
	return errors.As(err, &kerr) &&
		(kerr == kgo.UnknownTopicOrPartition || kerr == kgo.LeaderNotAvailable)
}

func (p *producer) close() error {
	if p == nil || p.w == nil {
		return nil
	}
	return p.w.Close()
}
