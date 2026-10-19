//go:build integration

package kafka_test

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	mobycontainer "github.com/moby/moby/api/types/container"
	mobynetwork "github.com/moby/moby/api/types/network"
	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const defaultKafkaTestImage = "apache/kafka:latest"

// kafkaTestImage returns the Kafka image used by the testcontainers helpers.
func kafkaTestImage() string {
	if image := os.Getenv("TEST_KAFKA_IMAGE"); image != "" {
		return image
	}
	return defaultKafkaTestImage
}

// kafkaKraftEnv configures a single-node KRaft broker reachable from the host
// at localhost:<hostPort>.
func kafkaKraftEnv(hostPort int) map[string]string {
	return map[string]string{
		"KAFKA_NODE_ID":                          "1",
		"KAFKA_PROCESS_ROLES":                    "broker,controller",
		"KAFKA_CONTROLLER_QUORUM_VOTERS":         "1@localhost:9093",
		"KAFKA_LISTENERS":                        "PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
		"KAFKA_ADVERTISED_LISTENERS":             "PLAINTEXT://localhost:" + strconv.Itoa(hostPort),
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":   "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
		"KAFKA_CONTROLLER_LISTENER_NAMES":        "CONTROLLER",
		"KAFKA_INTER_BROKER_LISTENER_NAME":       "PLAINTEXT",
		"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR": "1",
	}
}

// kafkaBrokerContainer owns a Kafka testcontainer whose published host port is
// stable across Stop/Start, so a client can reconnect to the same address.
type kafkaBrokerContainer struct {
	Container testcontainers.Container
	Addr      string
}

// fixedHostPortBinding publishes containerPort on a fixed host port. The fixed
// mapping is required when the container's advertised address must contain the
// host port before the container starts (Kafka's advertised listeners).
func fixedHostPortBinding(hostPort int, containerPort string) func(*mobycontainer.HostConfig) {
	port := mobynetwork.MustParsePort(containerPort)
	return func(hc *mobycontainer.HostConfig) {
		if hc.PortBindings == nil {
			hc.PortBindings = mobynetwork.PortMap{}
		}
		hc.PortBindings[port] = []mobynetwork.PortBinding{{
			HostIP:   netip.MustParseAddr("0.0.0.0"),
			HostPort: strconv.Itoa(hostPort),
		}}
	}
}

// startKafkaBroker starts a single-node KRaft broker published on hostPort.
func startKafkaBroker(ctx context.Context, hostPort int) (*kafkaBrokerContainer, error) {
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:              kafkaTestImage(),
			ExposedPorts:       []string{"9092/tcp"},
			HostConfigModifier: fixedHostPortBinding(hostPort, "9092/tcp"),
			Env:                kafkaKraftEnv(hostPort),
			WaitingFor:         wait.ForListeningPort("9092/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return nil, err
	}
	return &kafkaBrokerContainer{
		Container: ctr,
		Addr:      "localhost:" + strconv.Itoa(hostPort),
	}, nil
}

// runKafkaContainer starts a dedicated Kafka broker for a test. The container
// is terminated in Cleanup; tests may Stop and Start it in between.
func runKafkaContainer(t *testing.T, hostPort int) *kafkaBrokerContainer {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)

	broker, err := startKafkaBroker(context.Background(), hostPort)
	require.NoError(t, err, "start Kafka testcontainer")
	t.Cleanup(func() { _ = broker.Container.Terminate(context.Background()) })

	require.NoError(t, waitBroker(broker.Addr, 45*time.Second), "Kafka container not ready")
	return broker
}

// Stop stops the broker container, exercising the down path.
func (b *kafkaBrokerContainer) Stop(t *testing.T) {
	t.Helper()
	timeout := 30 * time.Second
	require.NoError(t, b.Container.Stop(context.Background(), &timeout))
}

// Start restarts the same container and waits for the broker to serve metadata.
func (b *kafkaBrokerContainer) Start(t *testing.T) {
	t.Helper()
	require.NoError(t, b.Container.Start(context.Background()))
	require.NoError(t, waitBroker(b.Addr, 45*time.Second), "Kafka broker did not recover after restart")
}

// waitBroker polls addr until it accepts a TCP dial and returns Kafka
// metadata, or until deadline expires.
func waitBroker(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := (&kgo.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		cancel()
		if err == nil {
			// Confirm the broker responds to metadata, not just TCP accept.
			_, err = conn.ReadPartitions()
			_ = conn.Close()
			if err == nil {
				return nil
			}
			lastErr = err
		} else {
			lastErr = err
		}
		time.Sleep(500 * time.Millisecond)
	}
	return lastErr
}

// freePort returns an unused local TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	port, err := reserveFreePort()
	require.NoError(t, err)
	return port
}

// reserveFreePort is freePort without a *testing.T, for TestMain.
func reserveFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}
