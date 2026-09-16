//go:build integration

package kafka_test

import (
	"context"
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

// TestListenNotification_NonDurableAcrossRestart covers T040: notifications
// emitted while an instance is down are NOT delivered on restart. This works
// because InstanceID is regenerated on each NewApplication call, so the new
// consumer group starts at LastOffset with no committed history.
func TestListenNotification_NonDurableAcrossRestart(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	notifName := "cache.invalidated"
	topic := appName + "." + notifName
	createTopic(t, brokers, topic, 1, 1)

	makeListener := func() (*rckafka.Application, *int32, context.CancelFunc) {
		cfg := rckafka.NewConfigWithDefaults()
		cfg.AppName = appName
		cfg.BootstrapBrokers = brokers
		cfg.AllowAutoCreateTopics = true
		cfg.DefaultPartitions = 1
		cfg.DefaultReplicationFactor = 1

		app, err := rckafka.NewApplication(cfg)
		require.NoError(t, err)

		var count int32
		require.NoError(t, app.Registry().ListenNotification(notifName, func(_ context.Context, _ async.Notification[any]) error {
			atomic.AddInt32(&count, 1)
			return nil
		}))

		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = app.Start(ctx) }()
		<-app.Ready()
		return app, &count, cancel
	}

	// Start first instance, then stop it.
	_, count1, cancel1 := makeListener()
	time.Sleep(2 * time.Second) // let it join the group at LastOffset
	cancel1()
	time.Sleep(500 * time.Millisecond)
	assert.EqualValues(t, 0, atomic.LoadInt32(count1), "no notifications should have arrived yet")

	// Publish two notifications while no instance is listening.
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

	for i := 0; i < 2; i++ {
		require.NoError(t, pubApp.EventBus().EmitNotification(context.Background(), async.Notification[any]{
			Name:    notifName,
			EventID: uuid.NewString(),
			Data:    map[string]any{"seq": i},
		}))
	}

	time.Sleep(500 * time.Millisecond)

	// Restart the listener. New InstanceID → new consumer group at LastOffset →
	// the two missed notifications MUST NOT arrive.
	_, count2, cancel2 := makeListener()
	defer cancel2()
	time.Sleep(4 * time.Second)

	assert.EqualValues(t, 0, atomic.LoadInt32(count2),
		"missed notifications must not be replayed on restart")
}
