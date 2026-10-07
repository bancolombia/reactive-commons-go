//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"log"
	"os/exec"
	"strings"
	"testing"
	"time"

	kgo "github.com/segmentio/kafka-go"
)

// containerCLIAvailable reports whether Apple's `container` CLI is present on
// PATH and its system services are running. Tests that manage container
// lifecycles use this to skip cleanly on machines that don't have it.
func containerCLIAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("container"); err != nil {
		return false
	}
	// `container ls` should succeed when the system service is up.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "container", "ls").Run() == nil
}

// runKafkaContainer starts a fresh KRaft-mode Kafka broker in a new container
// listening on host port hostPort. Returns the broker address and a stop
// function. The stop function is idempotent — you may call it during the
// test to exercise the down-then-up path; it is also invoked from t.Cleanup
// to guarantee cleanup on failure. Do NOT pass --rm; the recovery test needs
// to stop and start the SAME container.
func runKafkaContainer(t *testing.T, name string, hostPort int) string {
	t.Helper()

	args := []string{
		"run", "-d", "--name", name,
		"-p", portMap(hostPort, 9092),
		"-e", "KAFKA_NODE_ID=1",
		"-e", "KAFKA_PROCESS_ROLES=broker,controller",
		"-e", "KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
		"-e", "KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
		"-e", "KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://localhost:" + itoa(hostPort),
		"-e", "KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
		"-e", "KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
		"-e", "KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT",
		"-e", "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
		"-e", "CLUSTER_ID=" + name,
		"apache/kafka:latest",
	}
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("container run: %v\n%s", err, out)
	}

	t.Cleanup(func() {
		_ = exec.Command("container", "stop", name).Run()
		_ = exec.Command("container", "delete", name).Run()
	})

	addr := "localhost:" + itoa(hostPort)
	if err := waitBroker(addr, 45*time.Second); err != nil {
		t.Fatalf("Kafka container %s not ready: %v", name, err)
	}
	log.Printf("Kafka broker started at %s", addr)
	return addr
}

func stopContainer(t *testing.T, name string) {
	t.Helper()
	log.Printf("stoping container %s", name)
	out, err := exec.Command("container", "stop", name).CombinedOutput()
	if err != nil {
		t.Fatalf("container stop %s: %v\n%s", name, err, out)
	}
}

func startContainer(t *testing.T, name string) {
	t.Helper()
	log.Printf("starting container %s", name)
	out, err := exec.Command("container", "start", name).CombinedOutput()
	if err != nil {
		t.Fatalf("container start %s: %v\n%s", name, err, out)
	}
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
			conn.Close()
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

// containerIPv4 returns the container's first IPv4 address in the default
// network, stripped of any CIDR suffix. Retries until the network is
// populated or the deadline elapses.
func containerIPv4(t *testing.T, name string, timeout time.Duration) string {
	t.Helper()
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
	t.Fatalf("container %s never reported an ipv4 address within %s", name, timeout)
	return ""
}

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
