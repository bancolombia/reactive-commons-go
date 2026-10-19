package rabbit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func restoreTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRestoreWithBackoff_RetriesUntilSuccess(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	var attempts int
	err := errors.New("nope")

	done := make(chan struct{})
	go func() {
		restoreWithBackoff(context.Background(), 1, &gen, log, func() error {
			attempts++
			if attempts < 3 {
				return err
			}
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("restoreWithBackoff did not finish")
	}
	assert.Equal(t, 3, attempts)
}

func TestRestoreWithBackoff_ExitsOnContextCancel(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	err := errors.New("nope")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		restoreWithBackoff(ctx, 1, &gen, log, func() error {
			close(started)
			return err
		})
		close(done)
	}()

	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("restoreWithBackoff did not exit on context cancel")
	}
}

func TestRestoreWithBackoff_ExitsWhenSuperseded(t *testing.T) {
	log := restoreTestLogger()
	var gen uint64 = 1
	var attempts int
	err := errors.New("nope")

	done := make(chan struct{})
	go func() {
		restoreWithBackoff(context.Background(), 1, &gen, log, func() error {
			attempts++
			// A newer reconnect supersedes this loop during the first attempt.
			atomic.StoreUint64(&gen, 2)
			return err
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("restoreWithBackoff did not exit after being superseded")
	}
	assert.Equal(t, 1, attempts)
}

// TestStartListeners_JoinsErrors: one listener failing to start must not
// prevent the others from coming up; the returned error names the failed kind.
func TestStartListeners_JoinsErrors(t *testing.T) {
	log := restoreTestLogger()
	openErr := errors.New("event open failed")
	openFails := func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		return nil, nil, openErr
	}
	openOK := func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		return &fakeConsumerChannel{}, make(chan amqp.Delivery), nil
	}

	eventSup := newSupervisedConsumer(openConn(), log, nil, "event", openFails, func(context.Context, amqp.Delivery) {}, nil)
	replySup := newSupervisedConsumer(openConn(), log, nil, "reply", openOK, func(context.Context, amqp.Delivery) {}, nil)

	app := &RabbitApp{
		cfg:            Config{AppName: "svc"},
		log:            log,
		ready:          make(chan struct{}),
		replyQueueName: "svc.replies",
		eventL:         &eventListener{sup: eventSup, log: log},
		replyL:         &replyListener{sup: replySup, log: log},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := app.startListeners(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "event listener")
	assert.Contains(t, err.Error(), "event open failed")
	assert.Equal(t, ConsumerRunning, replySup.snapshot().State, "the healthy listener must still start")
}

// TestRabbitApp_Health_Aggregates: Healthy is derived from the connection and
// every configured consumer.
func TestRabbitApp_Health_Aggregates(t *testing.T) {
	conn := NewConnection(Config{})
	conn.setHealth(ConnectionConnected, nil)

	sup := newTestConsumer(openConn(), func(string) (consumerChannel, <-chan amqp.Delivery, error) {
		return &fakeConsumerChannel{}, make(chan amqp.Delivery), nil
	}, func(context.Context, amqp.Delivery) {}, nil)
	sup.stateMu.Lock()
	sup.runGen = 1
	sup.setHealthLocked(ConsumerRunning, nil)
	sup.stateMu.Unlock()

	app := &RabbitApp{
		ready:     make(chan struct{}),
		conn:      conn,
		consumers: []namedConsumer{{name: "event", sup: sup}},
	}
	close(app.ready)

	h := app.Health()
	assert.True(t, h.Ready)
	assert.True(t, h.Healthy)
	assert.Equal(t, ConnectionConnected, h.Connection.State)
	require.Contains(t, h.Consumers, "event")
	assert.Equal(t, ConsumerRunning, h.Consumers["event"].State)

	sup.setHealthForGen(1, ConsumerRebuilding, errors.New("channel lost"))
	h = app.Health()
	assert.False(t, h.Healthy)
	assert.Equal(t, ConsumerRebuilding, h.Consumers["event"].State)
}

func TestRabbitApp_Health_BeforeStart(t *testing.T) {
	app := NewRabbitApp(Config{AppName: "svc"})

	h := app.Health()
	assert.False(t, h.Ready)
	assert.False(t, h.Healthy)
	assert.Equal(t, ConnectionIdle, h.Connection.State)
	assert.Empty(t, h.Consumers)
}
