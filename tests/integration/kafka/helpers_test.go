//go:build integration

package kafka_test

import (
	"context"
	"strings"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"
)

// newAppName returns a unique app name so tests never share topics or groups.
func newAppName() string {
	return "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

// startAppWith starts an app with the standard test config (topics
// auto-create, single partition, single replica), applies mutate to the
// config, runs register before Start, and waits for Ready.
func startAppWith(t *testing.T, brokers []string, appName string, mutate func(cfg *rckafka.KafkaConfig), register func(app *rckafka.Application) error) *runningApp {
	t.Helper()

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	if mutate != nil {
		mutate(&cfg)
	}

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)
	if register != nil {
		require.NoError(t, register(app))
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Start(ctx) }()

	select {
	case <-app.Ready():
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatalf("app %s did not become ready within 30s", appName)
	}
	return &runningApp{app: app, appName: appName, cancel: cancel}
}

// publishRaw writes msgs with a bare kafka-go Writer, retrying on transient
// errors. kafka-go fails fast on UnknownTopicOrPartition from its per-write
// metadata lookup, which races topics that were just created while the
// broker's metadata cache catches up; retrying (with auto-create enabled on
// the writer) lets the broker settle.
func publishRaw(t *testing.T, brokers []string, msgs ...kgo.Message) {
	t.Helper()
	w := &kgo.Writer{
		Addr:                   kgo.TCP(brokers...),
		Balancer:               &kgo.Hash{},
		AllowAutoTopicCreation: true,
	}
	defer w.Close()

	var lastErr error
	for i := 0; i < 10; i++ {
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = w.WriteMessages(pubCtx, msgs...)
		pubCancel()
		if lastErr == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	require.NoError(t, lastErr, "raw publish should succeed within retries")
}
