//go:build integration

package kafka_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListen_CompetingConsumers covers T028: two applications with the same
// AppName (→ same consumer group) share the topic's partitions. Publishing 10
// events must result in exactly 10 total handler invocations (each event
// processed once, not twice).
func TestListen_CompetingConsumers(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	eventName := "user.created"
	topic := appName + "." + eventName
	// ≥2 partitions so the two consumers can share the load.
	createTopic(t, brokers, topic, 2, 1)

	makeApp := func(seen chan<- string, wg *sync.WaitGroup) (*rckafka.Application, context.CancelFunc) {
		cfg := rckafka.NewConfigWithDefaults()
		cfg.AppName = appName
		cfg.BootstrapBrokers = brokers
		cfg.AllowAutoCreateTopics = true
		cfg.DefaultPartitions = 2
		cfg.DefaultReplicationFactor = 1

		app, err := rckafka.NewApplication(cfg)
		require.NoError(t, err)
		require.NoError(t, app.Registry().ListenEvent(eventName, func(_ context.Context, ev async.DomainEvent[any]) error {
			seen <- ev.EventID
			wg.Done()
			return nil
		}))
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = app.Start(ctx) }()
		<-app.Ready()
		return app, cancel
	}

	const total = 10
	var wg sync.WaitGroup
	wg.Add(total)
	seen := make(chan string, total*2)

	_, cancel1 := makeApp(seen, &wg)
	defer cancel1()
	_, cancel2 := makeApp(seen, &wg)
	defer cancel2()

	// Give both consumers a moment to join the group before publishing.
	time.Sleep(2 * time.Second)

	producerCfg := rckafka.NewConfigWithDefaults()
	producerCfg.AppName = appName
	producerCfg.BootstrapBrokers = brokers
	producerCfg.AllowAutoCreateTopics = true
	producerCfg.DefaultPartitions = 2
	producerCfg.DefaultReplicationFactor = 1
	pubApp, err := rckafka.NewApplication(producerCfg)
	require.NoError(t, err)
	pubCtx, pubCancel := context.WithCancel(context.Background())
	defer pubCancel()
	go func() { _ = pubApp.Start(pubCtx) }()
	<-pubApp.Ready()

	emitCtx, emitCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer emitCancel()
	published := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		eid := uuid.NewString()
		published[eid] = true
		require.NoError(t, pubApp.EventBus().Emit(emitCtx, async.DomainEvent[any]{
			Name:    eventName,
			EventID: eid,
			Data:    map[string]any{"n": i},
		}))
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatalf("did not receive all %d messages within 60s", total)
	}

	// Drain the channel; assert union == published set with no duplicates.
	close(seen)
	got := make(map[string]int)
	for id := range seen {
		got[id]++
	}
	assert.Len(t, got, total, "each eventId should appear exactly once across both consumers")
	for id := range published {
		assert.Equal(t, 1, got[id], "eventId %s delivered %d times, expected 1", id, got[id])
	}
}
