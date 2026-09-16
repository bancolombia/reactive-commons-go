//go:build integration

package kafka_test

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecovery_ProducerAndConsumerResume covers T058 / SC-006: while the app
// is running, killing and restarting the Kafka broker must (a) surface a
// wrapped error from Emit while down, (b) allow both the producer and
// consumer to resume within 60 s of broker restart without a process
// restart, and (c) leave no goroutine leaks.
//
// Isolates itself in a dedicated Kafka container so the ambient broker is
// untouched. Skips cleanly if Apple's `container` CLI is unavailable.
func TestRecovery_ProducerAndConsumerResume(t *testing.T) {
	if !containerCLIAvailable(t) {
		t.Skip("Apple `container` CLI not available; skipping recovery test")
	}

	containerName := "rc-recovery-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	brokerAddr := runKafkaContainer(t, containerName, randomBrokerPort())
	brokers := []string{brokerAddr}

	baselineGoroutines := runtime.NumGoroutine()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	eventName := "user.created"
	topic := appName + "." + eventName
	createTopic(t, brokers, topic, 1, 1)

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1
	// Shorter than the default so retries in this test finish fast.
	cfg.MaxRetryAttempts = 2
	cfg.RetryInitialDelay = 200 * time.Millisecond
	cfg.RetryMaxDelay = 500 * time.Millisecond
	cfg.HandlerTimeout = 2 * time.Second

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	var received int32
	require.NoError(t, app.Registry().ListenEvent(eventName, func(_ context.Context, _ async.DomainEvent[any]) error {
		atomic.AddInt32(&received, 1)
		return nil
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	// Step 1: emit-and-consume successfully against the healthy broker.
	preOutageID := uuid.NewString()
	require.NoError(t, app.EventBus().Emit(context.Background(), async.DomainEvent[any]{
		Name:    eventName,
		EventID: preOutageID,
		Data:    map[string]any{"phase": "pre-outage"},
	}))
	require.Eventually(t,
		func() bool { return atomic.LoadInt32(&received) >= 1 },
		15*time.Second, 200*time.Millisecond,
		"handler should receive the pre-outage event")

	// Step 2: kill the broker.
	stopContainer(t, containerName)
	// Give the broker's TCP listener time to fully go away.
	time.Sleep(5 * time.Second)

	// Emit MUST return a wrapped error within a bounded time. Cap the request
	// context so a hung write cannot block the test forever.
	outageCtx, outageCancel := context.WithTimeout(context.Background(), 8*time.Second)
	outageErr := app.EventBus().Emit(outageCtx, async.DomainEvent[any]{
		Name:    eventName,
		EventID: uuid.NewString(),
		Data:    map[string]any{"phase": "during-outage"},
	})
	outageCancel()
	require.Error(t, outageErr, "emit MUST fail while the broker is down")
	assert.Contains(t, outageErr.Error(), "kafka:", "outage error must be wrapped with kafka: prefix")

	// Step 3: bring the broker back.
	startContainer(t, containerName)
	require.NoError(t, waitBroker(brokerAddr, 45*time.Second),
		"broker MUST become reachable within 45s of restart")

	// Step 4: emit again. The producer must resume without a process restart.
	// Allow up to 60 s for reader + writer to reconnect and metadata to refresh.
	postOutageID := uuid.NewString()
	resumed := false
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		emitCtx, emitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := app.EventBus().Emit(emitCtx, async.DomainEvent[any]{
			Name:    eventName,
			EventID: postOutageID,
			Data:    map[string]any{"phase": "post-outage"},
		})
		emitCancel()
		if err == nil {
			resumed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	require.True(t, resumed, "producer MUST resume within 60s of broker restart")

	// Consumer resumes too — expect at least one more delivery.
	require.Eventually(t,
		func() bool { return atomic.LoadInt32(&received) >= 2 },
		60*time.Second, 500*time.Millisecond,
		"consumer MUST resume and process the post-outage event within 60s")

	// Step 5: cancel the app and verify goroutine count returns to baseline.
	cancel()
	// Give listener/producer close routines time to unwind.
	drainDeadline := time.Now().Add(60 * time.Second)
	var finalCount int
	for time.Now().Before(drainDeadline) {
		finalCount = runtime.NumGoroutine()
		if finalCount <= baselineGoroutines+2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	assert.LessOrEqual(t, finalCount, baselineGoroutines+2,
		"goroutine count %d should return to baseline %d (+2 slack) after shutdown",
		finalCount, baselineGoroutines)
}
