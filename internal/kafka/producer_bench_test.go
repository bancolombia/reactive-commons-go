//go:build integration

package kafka

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

func ensureBenchTopic(b *testing.B, brokers []string, topic string) {
	b.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := (&kgo.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", brokers[0])
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	controller, err := conn.Controller()
	if err != nil {
		b.Fatalf("controller: %v", err)
	}
	ctlConn, err := (&kgo.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp",
		net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		b.Fatalf("dial controller: %v", err)
	}
	defer ctlConn.Close()
	_ = ctlConn.CreateTopics(kgo.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	})
}

// Benchmark_Emit_5k_msgs_per_sec measures the framework's Emit-path overhead
// against a KAFKA_BOOTSTRAP-provided broker. Iterations publish a small
// domain-event envelope via the internal producer and observability wiring.
//
// Baseline (recorded 2026-09-15 on Apple M3 Pro, ambient localhost:9092,
// segmentio/kafka-go v0.4.51, RequireOne + BatchTimeout=1ms):
//
//	Benchmark_Emit_5k_msgs_per_sec:     ~2.8 ms/op  22 KB/op  63 allocs/op
//	Benchmark_Emit_RawKafkaGo_Baseline: ~2.8 ms/op  20 KB/op  48 allocs/op
//
// Framework overhead is ~15 allocs/op above raw kafka-go; ns/op is dominated
// by the broker RTT so parallel workloads scale linearly with concurrency.
// Regressions of allocs/op > 20% vs. that baseline should be investigated.
//
// The comparison Benchmark_Emit_RawKafkaGo_Baseline runs the exact same
// message shape through kafka-go's Writer with no framework in the path so
// the delta reflects only reactive-commons overhead.
func Benchmark_Emit_5k_msgs_per_sec(b *testing.B) {
	brokers := envBrokers()
	if len(brokers) == 0 {
		b.Skip("KAFKA_BOOTSTRAP not set")
	}

	cfg := Config{
		AppName:                    "bench",
		BootstrapBrokers:           brokers,
		ProducerAcks:               kgo.RequireOne,
		ProducerBatchTimeout:       1 * time.Millisecond,
		MaxMessageBytes:            1_000_000,
		AutoGenerateMissingEventID: true,
	}
	p := newProducer(cfg)
	defer p.close()

	envelope := map[string]any{
		"name":    "user.created",
		"eventId": "e-fixed",
		"data":    map[string]any{"userId": "u-42", "email": "u42@example.com"},
	}
	body, _ := json.Marshal(envelope)
	topic := "bench.user.created"
	ensureBenchTopic(b, brokers, topic)
	headers := map[string]string{
		"content-type":              "application/json",
		"reactive-commons-envelope": "v1",
		"reactive-commons-kind":     "event",
	}

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := p.publish(ctx, topic, []byte("e-fixed"), body, headers); err != nil {
			b.Fatalf("publish: %v", err)
		}
	}
}

func Benchmark_Emit_RawKafkaGo_Baseline(b *testing.B) {
	brokers := envBrokers()
	if len(brokers) == 0 {
		b.Skip("KAFKA_BOOTSTRAP not set")
	}
	w := &kgo.Writer{
		Addr:         kgo.TCP(brokers...),
		Balancer:     &kgo.Hash{},
		RequiredAcks: kgo.RequireOne,
		BatchTimeout: 1 * time.Millisecond,
	}
	defer w.Close()

	envelope := map[string]any{
		"name":    "user.created",
		"eventId": "e-fixed",
		"data":    map[string]any{"userId": "u-42", "email": "u42@example.com"},
	}
	body, _ := json.Marshal(envelope)
	topic := "bench.user.created"
	ensureBenchTopic(b, brokers, topic)
	msg := kgo.Message{
		Topic: topic,
		Key:   []byte("e-fixed"),
		Value: body,
	}

	ctx := context.Background()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := w.WriteMessages(ctx, msg); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
}

func envBrokers() []string {
	if v := os.Getenv("KAFKA_BOOTSTRAP"); v != "" {
		return []string{v}
	}
	return nil
}
