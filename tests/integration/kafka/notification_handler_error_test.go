//go:build integration

package kafka_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
	kgo "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListenNotification_HandlerErrorNoRetryNoDLQ covers T041: a failing
// notification handler is invoked exactly once per message (no retries), and
// no DLQ message is produced.
func TestListenNotification_HandlerErrorNoRetryNoDLQ(t *testing.T) {
	brokers, cleanup := startKafkaContainer(t)
	defer cleanup()

	appName := "svc" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	notifName := "cache.invalidated"
	topic := appName + "." + notifName
	dlqTopic := topic + ".dlq"
	createTopic(t, brokers, topic, 1, 1)

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = appName
	cfg.BootstrapBrokers = brokers
	cfg.AllowAutoCreateTopics = true
	cfg.DefaultPartitions = 1
	cfg.DefaultReplicationFactor = 1

	app, err := rckafka.NewApplication(cfg)
	require.NoError(t, err)

	var calls int32
	require.NoError(t, app.Registry().ListenNotification(notifName, func(_ context.Context, _ async.Notification[any]) error {
		atomic.AddInt32(&calls, 1)
		return errors.New("intentional failure")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = app.Start(ctx) }()
	<-app.Ready()

	// Let the consumer fully join the group at LastOffset before publishing;
	// too short a wait and the produce would land past the join-time high
	// water mark.
	time.Sleep(5 * time.Second)

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
	require.NoError(t, pubApp.EventBus().EmitNotification(context.Background(), async.Notification[any]{
		Name:    notifName,
		EventID: eid,
		Data:    map[string]any{"key": "user:99"},
	}))

	// Give the framework time to invoke the handler and (not) retry.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) >= 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Wait another window in which a buggy retry loop would fire additional
	// invocations, then assert exactly one.
	time.Sleep(3 * time.Second)
	assert.EqualValues(t, 1, atomic.LoadInt32(&calls),
		"notification handler must not be retried")

	// Ensure no DLQ message was produced. The DLQ topic may not exist at all;
	// treat any read error as absence.
	dlqReader := kgo.NewReader(kgo.ReaderConfig{
		Brokers:     brokers,
		Topic:       dlqTopic,
		StartOffset: kgo.FirstOffset,
		MaxWait:     500 * time.Millisecond,
	})
	defer dlqReader.Close()
	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	if _, err := dlqReader.ReadMessage(readCtx); err == nil {
		t.Fatalf("no DLQ message should have been produced for notifications")
	}
}
