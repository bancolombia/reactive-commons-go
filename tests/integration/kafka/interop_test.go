//go:build integration

package kafka_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interopJars is the pinned dependency set that the Java sidecar links
// against. The build container has no DNS under Apple's container tool, so
// these are fetched on the host before `container build`.
var interopJars = []string{
	"https://repo1.maven.org/maven2/org/apache/kafka/kafka-clients/3.9.0/kafka-clients-3.9.0.jar",
	"https://repo1.maven.org/maven2/com/fasterxml/jackson/core/jackson-databind/2.16.1/jackson-databind-2.16.1.jar",
	"https://repo1.maven.org/maven2/com/fasterxml/jackson/core/jackson-core/2.16.1/jackson-core-2.16.1.jar",
	"https://repo1.maven.org/maven2/com/fasterxml/jackson/core/jackson-annotations/2.16.1/jackson-annotations-2.16.1.jar",
	"https://repo1.maven.org/maven2/org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.jar",
	"https://repo1.maven.org/maven2/org/slf4j/slf4j-simple/2.0.9/slf4j-simple-2.0.9.jar",
	"https://repo1.maven.org/maven2/org/lz4/lz4-java/1.8.0/lz4-java-1.8.0.jar",
	"https://repo1.maven.org/maven2/org/xerial/snappy/snappy-java/1.1.10.5/snappy-java-1.1.10.5.jar",
	"https://repo1.maven.org/maven2/com/github/luben/zstd-jni/1.5.5-11/zstd-jni-1.5.5-11.jar",
}

func fetchInteropJars(t *testing.T, interopDir string) {
	t.Helper()
	libsDir := filepath.Join(interopDir, "libs")
	if err := os.MkdirAll(libsDir, 0o755); err != nil {
		t.Fatalf("interop harness unavailable: mkdir libs: %v", err)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	for _, u := range interopJars {
		dst := filepath.Join(libsDir, filepath.Base(u))
		if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
			continue // already downloaded
		}
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("interop harness unavailable: fetch %s: %v", u, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("interop harness unavailable: fetch %s: HTTP %d", u, resp.StatusCode)
		}
		f, err := os.Create(dst)
		if err != nil {
			resp.Body.Close()
			t.Fatalf("interop harness unavailable: create %s: %v", dst, err)
		}
		_, err = io.Copy(f, resp.Body)
		resp.Body.Close()
		f.Close()
		if err != nil {
			t.Fatalf("interop harness unavailable: write %s: %v", dst, err)
		}
	}
}

const (
	interopKafkaImage    = "rc-interop-kafka:latest"
	interopConsumerImage = "rc-interop-consumer:latest"
	interopKafkaHostPort = 19094
)

// TestInterop_JavaConsumerReadsGoEmits covers T059 / SC-005: a Java consumer
// built against kafka-clients + jackson (proxying for io.cloudevents parsing)
// can decode the wire envelope produced by the Go framework, both for a plain
// payload and for a CloudEvent-shaped payload nested in the envelope's data
// field.
//
// Skips on `-short` (task marks this nightly-only) and when Apple's
// `container` CLI is unavailable.
func TestInterop_JavaConsumerReadsGoEmits(t *testing.T) {
	if testing.Short() {
		t.Skip("nightly interop")
	}
	if !containerCLIAvailable(t) {
		t.Skip("Apple `container` CLI not available; skipping interop test")
	}

	interopDir, err := filepath.Abs("interop")
	require.NoError(t, err)

	// Step 1 — pre-fetch the Java dependencies on the host (build containers
	// on Apple's `container` tool have no DNS), then build both images.
	// Idempotent: `container build` caches layers and fetchInteropJars skips
	// already-downloaded files.
	fetchInteropJars(t, interopDir)
	buildImage(t, interopDir, "kafka.Containerfile", interopKafkaImage)
	buildImage(t, interopDir, "consumer.Containerfile", interopConsumerImage)

	// Step 2 — start Kafka. Publishes 19094:19094 for host access; internal
	// listener is advertised on the container's own IP for the Java sidecar.
	kafkaName := "rc-interop-kafka-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	runInteropKafka(t, kafkaName, interopKafkaHostPort)
	kafkaIP := containerIPv4(t, kafkaName, 20*time.Second)
	t.Logf("interop kafka: name=%s host=localhost:%d internal=%s:9092", kafkaName, interopKafkaHostPort, kafkaIP)

	hostBroker := "localhost:" + itoa(interopKafkaHostPort)
	require.NoError(t, waitBroker(hostBroker, 60*time.Second),
		"host-facing kafka MUST be reachable within 60s")

	// Step 3 — create the two topics on the interop broker.
	appName := "billing" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	plainTopic := appName + ".user.created"
	ceTopic := appName + ".user.cloudevent"
	createTopic(t, []string{hostBroker}, plainTopic, 1, 1)
	createTopic(t, []string{hostBroker}, ceTopic, 1, 1)

	// Step 4 — run the Java consumer sidecar, subscribing to both topics.
	consumerName := "rc-interop-consumer-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	runJavaConsumer(t, consumerName, kafkaIP, plainTopic+","+ceTopic)
	require.NoError(t, waitForLogLine(consumerName, "READY", 45*time.Second),
		"Java consumer never printed READY; harness broken")

	// Step 5 — emit two events via the Go framework against the host port.
	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = []string{hostBroker}
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	plainID := uuid.NewString()
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    "user.created",
		EventID: plainID,
		Data:    map[string]any{"userId": "u-plain"},
	}))

	// Second event: CloudEvent-in-data. Note we build the CloudEvent
	// helper's map so we can capture its id for the assertion below.
	ceHelper := rckafka.WrapAsCloudEvent("billing", "user.created", map[string]any{"userId": "u-ce"})
	ceMap := ceHelper.(map[string]any)
	ceID := ceMap["id"].(string)

	ceEventID := uuid.NewString()
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    "user.cloudevent",
		EventID: ceEventID,
		Data:    ceHelper,
	}))

	// Step 6 — poll the Java container logs for two matching MSG lines.
	plainNeedle := "MSG topic=" + plainTopic +
		" name=user.created eventId=" + plainID + " ce.id=- ce.type=-"
	ceNeedle := "MSG topic=" + ceTopic +
		" name=user.cloudevent eventId=" + ceEventID + " ce.id=" + ceID + " ce.type=user.created"

	require.NoError(t, waitForLogLine(consumerName, plainNeedle, 60*time.Second),
		"Java consumer did not print the plain-payload MSG line")
	require.NoError(t, waitForLogLine(consumerName, ceNeedle, 60*time.Second),
		"Java consumer did not print the CloudEvent MSG line")

	assert.True(t, true, "if we got here, both messages parsed round-trip in Java")
}

func buildImage(t *testing.T, contextDir, dockerfile, tag string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "container", "build",
		"-t", tag,
		"-f", filepath.Join(contextDir, dockerfile),
		contextDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("interop harness unavailable: build %s failed: %v\n%s", tag, err, out)
	}
}

func runInteropKafka(t *testing.T, name string, hostPort int) {
	t.Helper()
	args := []string{
		"run", "-d", "--name", name,
		"-p", portMap(hostPort, 19094),
		interopKafkaImage,
	}
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("interop harness unavailable: run kafka failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("container", "stop", name).Run()
		_ = exec.Command("container", "delete", name).Run()
	})
}

func runJavaConsumer(t *testing.T, name, kafkaIP, topics string) {
	t.Helper()
	args := []string{
		"run", "-d", "--name", name,
		"-e", "KAFKA_BOOTSTRAP=" + kafkaIP + ":9092",
		"-e", "KAFKA_TOPICS=" + topics,
		"-e", "INTEROP_GROUP_ID=interop-" + name,
		interopConsumerImage,
	}
	out, err := exec.Command("container", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("interop harness unavailable: run consumer failed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("container", "stop", name).Run()
		_ = exec.Command("container", "delete", name).Run()
	})
}

// waitForLogLine polls `container logs` until the exact substring appears or
// the deadline expires.
func waitForLogLine(container, needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("container", "logs", container).CombinedOutput()
		if strings.Contains(string(out), needle) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Include tail of logs to aid debugging.
	tail, _ := exec.Command("container", "logs", "-n", "40", container).CombinedOutput()
	return &interopTimeoutError{needle: needle, timeout: timeout, tail: string(tail)}
}

type interopTimeoutError struct {
	needle  string
	timeout time.Duration
	tail    string
}

func (e *interopTimeoutError) Error() string {
	return "did not observe log line matching `" + e.needle + "` within " + e.timeout.String() + "; last logs:\n" + e.tail
}
