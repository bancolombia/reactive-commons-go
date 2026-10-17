//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// interopJars is the pinned dependency set that the Java sidecar links
// against. They are fetched on the host before the image build so the build
// does not need Maven access.
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
	interopKafkaImageRepo    = "rc-interop-kafka"
	interopConsumerImageRepo = "rc-interop-consumer"
	// interopHostListenerPort is Kafka's HOST listener inside the container;
	// it is published to a free host port for the Go process.
	interopHostListenerPort = 19094
)

// logCapture accumulates container output delivered by the testcontainers log
// producer so assertions can wait for individual log lines.
type logCapture struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *logCapture) Accept(log testcontainers.Log) {
	l.mu.Lock()
	l.buf.Write(log.Content)
	l.mu.Unlock()
}

func (l *logCapture) contains(needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.buf.String(), needle)
}

func (l *logCapture) tail(maxLines int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := strings.Split(strings.TrimRight(l.buf.String(), "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func (l *logCapture) waitFor(needle string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if l.contains(needle) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("did not observe log line matching %q within %s; last logs:\n%s",
		needle, timeout, l.tail(40))
}

// TestInterop_JavaConsumerReadsGoEmits covers T059 / SC-005: a Java consumer
// built against kafka-clients + jackson (proxying for io.cloudevents parsing)
// can decode the wire envelope produced by the Go framework, both for a plain
// payload and for a CloudEvent-shaped payload nested in the envelope's data
// field.
//
// Skips on `-short` (nightly-only) and when no Docker-compatible provider is
// available. Everything runs through testcontainers-go.
func TestInterop_JavaConsumerReadsGoEmits(t *testing.T) {
	if testing.Short() {
		t.Skip("nightly interop")
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()
	interopDir, err := filepath.Abs("interop")
	require.NoError(t, err)

	// Fetch the Java dependencies on the host before the Docker build.
	fetchInteropJars(t, interopDir)

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	hostPort := freePort(t)

	dockerNet, err := network.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerNet.Remove(context.Background()) })

	// Kafka: reachable from the host on localhost:<hostPort> and from the Java
	// sidecar on the network alias kafka:9092.
	kafkaCtr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    interopDir,
				Dockerfile: "kafka.Containerfile",
				Repo:       interopKafkaImageRepo,
				Tag:        "latest",
				KeepImage:  true,
			},
			ExposedPorts: []string{strconv.Itoa(interopHostListenerPort) + "/tcp"},
			HostConfigModifier: fixedHostPortBinding(hostPort,
				strconv.Itoa(interopHostListenerPort)+"/tcp"),
			Env:            map[string]string{"KAFKA_HOST_PORT": strconv.Itoa(hostPort)},
			Networks:       []string{dockerNet.Name},
			NetworkAliases: map[string][]string{dockerNet.Name: {"kafka"}},
			WaitingFor: wait.ForListeningPort(strconv.Itoa(interopHostListenerPort) + "/tcp").
				WithStartupTimeout(90 * time.Second),
			Name: "rc-interop-kafka-" + suffix,
		},
		Started: true,
	})
	require.NoError(t, err, "start interop Kafka container")
	t.Cleanup(func() { _ = kafkaCtr.Terminate(context.Background()) })

	hostBroker := "localhost:" + strconv.Itoa(hostPort)
	require.NoError(t, waitBroker(hostBroker, 60*time.Second),
		"host-facing kafka MUST be reachable within 60s")
	t.Logf("interop kafka: name=kafka host=%s internal=kafka:9092", hostBroker)

	// Create the two topics on the interop broker.
	appName := "billing" + suffix
	plainTopic := appName + ".user.created"
	ceTopic := appName + ".user.cloudevent"
	createTopic(t, []string{hostBroker}, plainTopic, 1, 1)
	createTopic(t, []string{hostBroker}, ceTopic, 1, 1)

	// Run the Java consumer sidecar, subscribing to both topics.
	logs := &logCapture{}
	consumerCtr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context:    interopDir,
				Dockerfile: "consumer.Containerfile",
				Repo:       interopConsumerImageRepo,
				Tag:        "latest",
				KeepImage:  true,
			},
			Env: map[string]string{
				"KAFKA_BOOTSTRAP":  "kafka:9092",
				"KAFKA_TOPICS":     plainTopic + "," + ceTopic,
				"INTEROP_GROUP_ID": "interop-" + suffix,
			},
			Networks:   []string{dockerNet.Name},
			WaitingFor: wait.ForLog("READY").WithStartupTimeout(90 * time.Second),
			LogConsumerCfg: &testcontainers.LogConsumerConfig{
				Consumers: []testcontainers.LogConsumer{logs},
			},
			Name: "rc-interop-consumer-" + suffix,
		},
		Started: true,
	})
	require.NoError(t, err, "start Java interop consumer")
	t.Cleanup(func() { _ = consumerCtr.Terminate(context.Background()) })

	// Emit two events via the Go framework against the host port.
	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = []string{hostBroker}
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(appCtx) }()
	<-app.Ready()

	plainID := uuid.NewString()
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    "user.created",
		EventID: plainID,
		Data:    map[string]any{"userId": "u-plain"},
	}))

	// Second event: CloudEvent-in-data. Build the helper's map so we can
	// capture its id for the assertion below.
	ceHelper := rckafka.WrapAsCloudEvent("billing", "user.created", map[string]any{"userId": "u-ce"})
	ceMap := ceHelper.(map[string]any)
	ceID := ceMap["id"].(string)

	ceEventID := uuid.NewString()
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    "user.cloudevent",
		EventID: ceEventID,
		Data:    ceHelper,
	}))

	plainNeedle := "MSG topic=" + plainTopic +
		" name=user.created eventId=" + plainID + " ce.id=- ce.type=-"
	ceNeedle := "MSG topic=" + ceTopic +
		" name=user.cloudevent eventId=" + ceEventID + " ce.id=" + ceID + " ce.type=user.created"

	require.NoError(t, logs.waitFor(plainNeedle, 60*time.Second),
		"Java consumer did not print the plain-payload MSG line")
	require.NoError(t, logs.waitFor(ceNeedle, 60*time.Second),
		"Java consumer did not print the CloudEvent MSG line")
}
