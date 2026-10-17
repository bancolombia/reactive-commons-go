//go:build integration

package rabbit_test

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Package-level RabbitMQ connection details shared by all tests in this file.
// rabbitMgmtPort is the management plugin's HTTP port (15672 by default) and
// is only populated when the broker exposes it; tests that need it should
// skip when it is zero.
var (
	rabbitHost     string
	rabbitPort     int
	rabbitMgmtPort int
)

// tryStartContainer attempts to start a RabbitMQ container. Returns (host, amqpPort, mgmtPort, cleanup, ok).
// Returns ok=false without panicking when Docker is unavailable.
func tryStartContainer(ctx context.Context) (host string, amqpPort, mgmtPort int, cleanup func(), ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	dockerImage := os.Getenv("TEST_RABBITMQ_IMAGE")
	if dockerImage == "" {
		// The -management image keeps the same AMQP port (5672) and adds
		// the management HTTP API on 15672, which the reconnect test uses
		// to force-close a live broker connection.
		dockerImage = "rabbitmq:3.12-management-alpine"
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        dockerImage,
			ExposedPorts: []string{"5672/tcp", "15672/tcp"},
			WaitingFor:   wait.ForListeningPort("5672/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		log.Printf("Failed to start RabbitMQ container: %v", err)
		return "", 0, 0, nil, false
	}

	h, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return "", 0, 0, nil, false
	}
	p, err := container.MappedPort(ctx, "5672")
	if err != nil {
		_ = container.Terminate(ctx)
		return "", 0, 0, nil, false
	}
	mp, mErr := container.MappedPort(ctx, "15672")
	mgmt := 0
	if mErr == nil {
		mgmt = int(mp.Num())
	}
	return h, int(p.Num()), mgmt, func() { _ = container.Terminate(ctx) }, true
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Priority 1: use RABBITMQ_URL env var (e.g. in CI with a pre-existing broker)
	if url := os.Getenv("RABBITMQ_HOST"); url != "" {
		rabbitHost = url
		if portStr := os.Getenv("RABBITMQ_PORT"); portStr != "" {
			p, err := strconv.Atoi(portStr)
			if err != nil {
				log.Fatalf("invalid RABBITMQ_PORT %q: %v", portStr, err)
			}
			rabbitPort = p
		} else {
			rabbitPort = 5672
		}
		if mgmtStr := os.Getenv("RABBITMQ_MGMT_PORT"); mgmtStr != "" {
			p, err := strconv.Atoi(mgmtStr)
			if err != nil {
				log.Fatalf("invalid RABBITMQ_MGMT_PORT %q: %v", mgmtStr, err)
			}
			rabbitMgmtPort = p
		}
		os.Exit(m.Run())
	}

	// Priority 2: spin up a container via testcontainers-go
	host, port, mgmt, cleanup, ok := tryStartContainer(ctx)
	if !ok {
		// Last resort: use a broker that is already running locally.
		conn, err := net.DialTimeout("tcp", "localhost:5672", 2*time.Second)
		if err != nil {
			log.Println("SKIP: integration tests require Docker or a running RabbitMQ (set RABBITMQ_HOST / RABBITMQ_PORT). Skipping.")
			os.Exit(0)
		}
		_ = conn.Close()
		rabbitHost = "localhost"
		rabbitPort = 5672
		// Best-effort: assume default management port. Reconnect test will
		// skip if HTTP probe fails.
		if mgmtConn, mgmtErr := net.DialTimeout("tcp", "localhost:15672", 2*time.Second); mgmtErr == nil {
			_ = mgmtConn.Close()
			rabbitMgmtPort = 15672
		}
		os.Exit(m.Run())
	}

	rabbitHost = host
	rabbitPort = port
	rabbitMgmtPort = mgmt

	code := m.Run()
	cleanup()
	os.Exit(code)
}
