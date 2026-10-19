//go:build integration

package kafka_test

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// kafkaBrokersAddr is set by TestMain to the testcontainers broker, the
// KAFKA_BOOTSTRAP override, or a local-broker fallback.
var kafkaBrokersAddr []string

// kafkaBrokers returns the bootstrap brokers for integration tests. The broker
// is started once in TestMain; tests skip if none is available.
func kafkaBrokers(t *testing.T) []string {
	t.Helper()
	if len(kafkaBrokersAddr) == 0 {
		t.Skip("no Kafka broker available")
	}
	return kafkaBrokersAddr
}

// startKafkaContainer keeps the old signature so existing tests compile
// unchanged. The shared broker is started once in TestMain.
func startKafkaContainer(t *testing.T) (brokers []string, cleanup func()) {
	t.Helper()
	return kafkaBrokers(t), func() {}
}

// createTopic ensures topic exists on the broker with the given partitions and
// replication factor. Idempotent.
func createTopic(t *testing.T, brokers []string, topic string, partitions, replication int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := (&kgo.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial for topic create: %v", err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		t.Fatalf("read controller: %v", err)
	}
	ctlAddr := net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port))
	ctlConn, err := (&kgo.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", ctlAddr)
	if err != nil {
		t.Fatalf("dial controller %s: %v", ctlAddr, err)
	}
	defer ctlConn.Close()

	if err := ctlConn.CreateTopics(kgo.TopicConfig{
		Topic:             topic,
		NumPartitions:     partitions,
		ReplicationFactor: replication,
	}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
}

// tryStartKafkaContainer starts the suite-wide broker via testcontainers-go.
// Returns ok=false when no Docker-compatible provider is available.
func tryStartKafkaContainer(ctx context.Context) (addr string, cleanup func(), ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	port, err := reserveFreePort()
	if err != nil {
		log.Printf("Failed to reserve a Kafka host port: %v", err)
		return "", nil, false
	}
	broker, err := startKafkaBroker(ctx, port)
	if err != nil {
		log.Printf("Failed to start Kafka container: %v", err)
		return "", nil, false
	}
	if err := waitBroker(broker.Addr, 60*time.Second); err != nil {
		log.Printf("Kafka container not ready: %v", err)
		_ = broker.Container.Terminate(context.Background())
		return "", nil, false
	}
	return broker.Addr, func() { _ = broker.Container.Terminate(context.Background()) }, true
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Priority 1: use KAFKA_BOOTSTRAP (e.g. in CI with a pre-existing broker).
	if url := os.Getenv("KAFKA_BOOTSTRAP"); url != "" {
		kafkaBrokersAddr = []string{url}
		os.Exit(m.Run())
	}

	// Priority 2: start a testcontainer via testcontainers-go.
	if addr, cleanup, ok := tryStartKafkaContainer(ctx); ok {
		kafkaBrokersAddr = []string{addr}
		code := m.Run()
		cleanup()
		os.Exit(code)
	}

	// Last resort: an already-running local broker.
	conn, err := net.DialTimeout("tcp", "localhost:9092", 2*time.Second)
	if err != nil {
		log.Println("SKIP: integration tests require Docker or a running Kafka (set KAFKA_BOOTSTRAP). Skipping.")
		os.Exit(0)
	}
	_ = conn.Close()
	kafkaBrokersAddr = []string{"localhost:9092"}
	os.Exit(m.Run())
}
