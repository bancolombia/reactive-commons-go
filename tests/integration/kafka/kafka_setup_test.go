//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	kafkaHost []string
)

// randomBrokerPort returns a random port in the range [19093, 19099] for isolated
// broker testing. This range is chosen to avoid conflicts with the ambient Kafka
// broker (typically 9092) and common development ports.
func randomBrokerPort() int {
	return 19093 + rand.Intn(7) // 7 possible values: 0-6
}

// kafkaBrokers returns the bootstrap brokers to use for integration tests.
// A pre-provisioned Kafka is assumed to be running; the address is taken from
// KAFKA_BOOTSTRAP with a localhost:9092 fallback. The connection is probed
// once so a broker outage skips the test rather than hanging.
func kafkaBrokers(t *testing.T) []string {
	t.Helper()
	addr := os.Getenv("KAFKA_BOOTSTRAP")
	if addr == "" {
		addr = "localhost:9092"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := (&kgo.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Skipf("kafka broker at %s unreachable: %v", addr, err)
	}
	_ = conn.Close()
	return []string{addr}
}

// startKafkaContainer keeps the old signature so existing tests compile
// unchanged; it now uses the ambient Kafka broker instead of spawning a
// container.
func startKafkaContainer(t *testing.T) (brokers []string, cleanup func()) {
	t.Helper()
	return kafkaBrokers(t), func() {}
}

// createTopic ensures topic exists on the ambient broker with the given
// partitions and replication factor. Called by tests whose broker does not
// have auto.create.topics.enable=true. Idempotent.
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

// ----------------------------------------------------------------

func containerIPv4v2(name string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("container", "inspect", name).CombinedOutput()
		if err == nil {
			// `container inspect` returns a JSON array; the assigned IP lives
			// under status.networks[].ipv4Address as CIDR.
			var docs []struct {
				Status struct {
					Networks []struct {
						IPv4Address string `json:"ipv4Address"`
					} `json:"networks"`
				} `json:"status"`
			}
			if err := json.Unmarshal(out, &docs); err == nil && len(docs) > 0 {
				for _, net := range docs[0].Status.Networks {
					ip := net.IPv4Address
					if slash := strings.IndexByte(ip, '/'); slash > 0 {
						ip = ip[:slash]
					}
					if ip != "" {
						return ip
					}
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("container %s never reported an ipv4 address within %s", name, timeout)
	return ""
}

// waitForPort probes host:port with TCP until it accepts a connection or the
// deadline is reached. Returns true if the port became reachable.
func waitForPort(host string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func tryStartKafkaContainer(ctx context.Context) (host string, cleanup func(), ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	dockerImage := os.Getenv("TEST_KAFKA_IMAGE")
	if dockerImage == "" {
		dockerImage = "apache/kafka:latest"
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImage,
			ExposedPorts: []string{"9092/tcp", "9092/tcp"},
			WaitingFor:   wait.ForListeningPort("9092/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		log.Printf("Failed to start Kafka container: %v", err)
		return "", nil, false
	}

	h, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, false
	}
	p, err := container.MappedPort(ctx, "9092")
	if err != nil {
		_ = container.Terminate(ctx)
		return "", nil, false
	}
	return h + ":" + strconv.FormatUint(uint64(p.Num()), 10), func() { _ = container.Terminate(ctx) }, true
}

func tryStartKafkaContainerMacOS(ctx context.Context) (host string, cleanup func(), ok bool) {
	if runtime.GOOS != "darwin" {
		log.Printf("No MacOS, so no container runtime available.")
		return "", nil, false
	}

	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	dockerImage := os.Getenv("TEST_KAFKA_IMAGE")
	if dockerImage == "" {
		dockerImage = "apache/kafka:latest"
	}

	// This function will attempt to use whichever runtime is available.
	log.Println("Attempting to start Kafka container on macOS using available container runtime...")

	containerName := "kafka-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	args := []string{
		"run", "-d", "--name", containerName,
		"-p", portMap(9092, 9092),
		dockerImage,
	}
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		log.Printf("Failed to start Kafka container on macOS run: %v\n%s", err, out)
		return "", nil, false
	}

	h := containerIPv4v2(containerName, 10*time.Second)
	if h == "" {
		return "", nil, false
	}

	if !waitForPort(h, 9092, 60*time.Second) {
		log.Printf("Kafka at %s:9092 never became ready", h)
		_ = exec.Command("container", "stop", containerName).Run()
		_ = exec.Command("container", "delete", containerName).Run()
		return "", nil, false
	}

	log.Printf("Kafka container started on macOS at %s:%d", h, 9092)
	return h, func() {
		_ = exec.Command("container", "stop", containerName).Run()
		_ = exec.Command("container", "delete", containerName).Run()
	}, true
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Priority 1: use KAFKA_BOOTSTRAP env var (e.g. in CI with a pre-existing broker)
	if url := os.Getenv("KAFKA_BOOTSTRAP"); url != "" {
		kafkaHost = []string{url}
		os.Exit(m.Run())
	}

	// Priority 2: spin up a container via testcontainers-go
	host, cleanup, ok := tryStartKafkaContainer(ctx)
	if !ok {

		// Priority 3: spin up a container when running on macos and container system is avail.
		host2, cleanup2, ok2 := tryStartKafkaContainerMacOS(ctx)

		if !ok2 {

			// Last resort. Check if a local broker is already running.
			conn, err := net.DialTimeout("tcp", "localhost:9092", 2*time.Second)
			if err != nil {
				log.Println("SKIP: integration tests require Docker or a running Kafka (set KAFKA_BOOTSTRAP). Skipping.")
				os.Exit(0)
			}
			_ = conn.Close()
			kafkaHost = []string{"localhost:9092"}
			os.Exit(m.Run())
		}

		// use container spined by macos container system
		kafkaHost = []string{host2}
		code := m.Run()
		cleanup2()
		os.Exit(code)
	}

	kafkaHost = []string{host}

	code := m.Run()
	cleanup()
	os.Exit(code)
}
