//go:build integration

package kafka_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListenNotification_FanoutAllInstances covers T039: three instances of the
// same application each receive every notification (per-instance consumer group).
func TestListenNotification_FanoutAllInstances(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	notifName := "cache.invalidated"
	topic := appName + "." + notifName
	createTopic(t, brokers, topic, 1, 1)

	const instances = 3
	counts := make([]int32, instances)
	seen := make([][]string, instances)
	var mu sync.Mutex
	cancels := make([]context.CancelFunc, instances)

	for i := 0; i < instances; i++ {
		i := i
		cfg := rckafka.NewConfigWithDefaults()
		cfg.AppName = appName
		cfg.BootstrapBrokers = brokers
		cfg.AllowAutoCreateTopics = true
		cfg.DefaultPartitions = 1
		cfg.DefaultReplicationFactor = 1

		app, err := rckafka.NewApplication(cfg)
		require.NoError(t, err)

		require.NoError(t, app.Registry().ListenNotification(notifName, func(_ context.Context, n async.Notification[any]) error {
			atomic.AddInt32(&counts[i], 1)
			mu.Lock()
			seen[i] = append(seen[i], n.EventID)
			mu.Unlock()
			return nil
		}))

		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		go func() { _ = app.Start(ctx) }()
		<-app.Ready()
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	// Give all three consumer groups time to fully join at LastOffset before
	// publishing; too short a wait and the produce lands past the join-time
	// high water mark.
	time.Sleep(6 * time.Second)

	// Publish one notification via a fresh app.
	pubCfg := rckafka.NewConfigWithDefaults()
	pubCfg.AppName = appName + "-pub"
	pubCfg.BootstrapBrokers = brokers
	pubCfg.NotificationTopicNameFunc = func(_ string) string { return topic }
	pubCfg.AllowAutoCreateTopics = true
	pubApp, err := rckafka.NewApplication(pubCfg)
	require.NoError(t, err)
	pubCtx, pubCancel := context.WithCancel(context.Background())
	defer pubCancel()
	go func() { _ = pubApp.Start(pubCtx) }()
	<-pubApp.Ready()

	eid := uuid.NewString()
	emitCtx, emitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer emitCancel()
	require.NoError(t, pubApp.EventBus().EmitNotification(emitCtx, async.Notification[any]{
		Name:    notifName,
		EventID: eid,
		Data:    map[string]any{"key": "user:42"},
	}))

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		total := int32(0)
		for i := 0; i < instances; i++ {
			total += atomic.LoadInt32(&counts[i])
		}
		if total >= instances {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	for i := 0; i < instances; i++ {
		assert.EqualValues(t, 1, atomic.LoadInt32(&counts[i]),
			"instance %d should have received exactly 1 notification", i)
		mu.Lock()
		got := seen[i]
		mu.Unlock()
		if assert.Len(t, got, 1) {
			assert.Equal(t, eid, got[0])
		}
	}
}
