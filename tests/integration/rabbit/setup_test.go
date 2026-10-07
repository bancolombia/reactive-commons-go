//go:build integration

package rabbit_test

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

func portMap(hostPort, containerPort int) string {
	return itoa(hostPort) + ":" + itoa(containerPort)
}

func itoa(n int) string {
	// Small inline itoa to avoid depending on strconv just for this file.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return strings.Clone(string(buf[i:]))
}

func containerIPv4(name string, timeout time.Duration) string {
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

// tryStartContainerMacOS attempts to start a RabbitMQ container using macOS-specific
// container runtimes (Podman, Colima, OrbStack, etc.) as alternatives to Docker.
// Returns (host, amqpPort, mgmtPort, cleanup, ok).
// Returns ok=false without panicking when a macOS container runtime is unavailable.
func tryStartContainerMacOS(ctx context.Context) (host string, amqpPort, mgmtPort int, cleanup func(), ok bool) {
	if runtime.GOOS != "darwin" {
		log.Printf("No MacOS, so no container runtime available.")
		return "", 0, 0, nil, false
	}

	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	dockerImage := os.Getenv("TEST_RABBITMQ_IMAGE")
	if dockerImage == "" {
		dockerImage = "rabbitmq:3.12-management-alpine"
	}

	// This function will attempt to use whichever runtime is available.
	log.Println("Attempting to start RabbitMQ container on macOS using available container runtime...")

	containerName := "rabbitmq-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	args := []string{
		"run", "-d", "--name", containerName,
		"-p", portMap(5672, 5672),
		"-p", portMap(15672, 15672),
		dockerImage,
	}
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		log.Printf("Failed to start RabbitMQ container on macOS run: %v\n%s", err, out)
		return "", 0, 0, nil, false
	}

	h := containerIPv4(containerName, 10*time.Second)
	if h == "" {
		return "", 0, 0, nil, false
	}

	if !waitForPort(h, 5672, 60*time.Second) {
		log.Printf("RabbitMQ at %s:5672 never became ready", h)
		_ = exec.Command("container", "stop", containerName).Run()
		_ = exec.Command("container", "delete", containerName).Run()
		return "", 0, 0, nil, false
	}

	log.Printf("RabbitMQ container started on macOS at %s:%d (mgmt: %d)", h, 5672, 15672)
	return h, 5672, 15672, func() {
		_ = exec.Command("container", "stop", containerName).Run()
		_ = exec.Command("container", "delete", containerName).Run()
	}, true
}

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

		// Priority 3: spin up a container when running on macos and container system is avail.
		host2, port2, mgmt2, cleanup2, ok2 := tryStartContainerMacOS(ctx)

		if !ok2 {

			// Last resort. Check if a local broker is already running.
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

		// use container spined by macos container system
		rabbitHost = host2
		rabbitPort = port2
		rabbitMgmtPort = mgmt2

		code := m.Run()
		cleanup2()
		os.Exit(code)
	}

	rabbitHost = host
	rabbitPort = port
	rabbitMgmtPort = mgmt

	code := m.Run()
	cleanup()
	os.Exit(code)
}
