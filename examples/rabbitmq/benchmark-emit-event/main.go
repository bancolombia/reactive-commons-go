// Command benchmark-emit-event measures RabbitMQ domain-event publish
// throughput. It runs for BENCH_DURATION (default 5m) and prints the achieved
// publishes per second. Run benchmark-listen-event alongside it to measure a
// realistic fan-out with a durable queue bound to the event.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bancolombia/reactive-commons-go/examples/rabbitmq/internal/bench"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
)

type benchPayload struct {
	Payload string `json:"payload"`
}

func main() {
	os.Exit(run())
}

func run() int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	duration, err := bench.EnvDuration("BENCH_DURATION", 5*time.Minute)
	if err != nil || duration <= 0 {
		logger.Error("invalid BENCH_DURATION", "error", err)
		return 1
	}
	publishers, err := bench.EnvInt("BENCH_PUBLISHERS", 16)
	if err != nil || publishers < 1 {
		logger.Error("invalid BENCH_PUBLISHERS", "error", err)
		return 1
	}
	sendTimeout, err := bench.EnvDuration("BENCH_SEND_TIMEOUT", 10*time.Second)
	if err != nil || sendTimeout <= 0 {
		logger.Error("invalid BENCH_SEND_TIMEOUT", "error", err)
		return 1
	}
	persistent, err := bench.EnvBool("BENCH_PERSISTENT", true)
	if err != nil {
		logger.Error("invalid BENCH_PERSISTENT", "error", err)
		return 1
	}
	eventName := bench.EnvString("BENCH_EVENT_NAME", "benchmark.event")
	if err := bench.ValidateEventName(eventName); err != nil {
		logger.Error("invalid BENCH_EVENT_NAME", "error", err)
		return 1
	}
	appName := bench.EnvString("BENCH_APP_NAME", "bench-emitter")

	cfg, err := bench.RabbitConfig(appName)
	if err != nil {
		logger.Error("failed to build RabbitMQ config", "error", err)
		return 1
	}
	cfg.PersistentEvents = persistent
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		return 1
	}

	appCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	appErrCh := make(chan error, 1)
	go func() { appErrCh <- app.Start(appCtx) }()

	select {
	case <-app.Ready():
	case err := <-appErrCh:
		logger.Error("application stopped before ready", "error", err)
		return 1
	case <-appCtx.Done():
		<-appErrCh
		return 1
	}

	bench.Printf("=== RabbitMQ domain-event emit benchmark ===\n")
	bench.Printf("broker:       %s:%d vhost=%q\n", cfg.Host, cfg.Port, cfg.VirtualHost)
	bench.Printf("event:        %s\n", eventName)
	bench.Printf("app:          %s\n", appName)
	bench.Printf("publishers:   %d\n", publishers)
	bench.Printf("duration:     %s\n", duration)
	bench.Printf("send timeout: %s\n", sendTimeout)
	bench.Printf("persistent:   %t\n\n", persistent)

	// The event is immutable and shared by every worker: Emit only marshals it,
	// while the sender generates a fresh AMQP MessageId per publish.
	event := async.DomainEvent[any]{
		Name:    eventName,
		EventID: "benchmark-event",
		Data:    benchPayload{Payload: "benchmark"},
	}

	bus := app.EventBus()
	runCtx, cancelRun := context.WithCancel(appCtx)
	defer cancelRun()

	start := time.Now()
	timer := time.AfterFunc(duration, cancelRun)
	defer timer.Stop()

	var published, failed atomic.Uint64
	var logOnce sync.Once
	var wg sync.WaitGroup

	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for runCtx.Err() == nil {
				sendCtx, cancel := context.WithTimeout(runCtx, sendTimeout)
				err := bus.Emit(sendCtx, event)
				cancel()

				if err == nil {
					published.Add(1)
					continue
				}
				// A canceled run means the deadline or Ctrl+C cut off an
				// in-flight send; it is not a broker failure.
				if runCtx.Err() != nil {
					return
				}
				failed.Add(1)
				logOnce.Do(func() {
					logger.Error("send failed; suppressing further send errors", "error", err)
				})
				select {
				case <-runCtx.Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		}()
	}

	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	status := "complete"
	var startErr error
	select {
	case <-workersDone:
		if appCtx.Err() != nil {
			status = "interrupted"
		}
	case startErr = <-appErrCh:
		status = "app stopped"
		cancelRun()
		<-workersDone
	}

	elapsed := time.Since(start)
	ok := published.Load()
	failures := failed.Load()
	tps := float64(ok) / elapsed.Seconds()

	bench.Printf("=== result ===\n")
	bench.Printf("status:       %s\n", status)
	bench.Printf("elapsed:      %s\n", elapsed)
	bench.Printf("published:    %d\n", ok)
	bench.Printf("failed:       %d\n", failures)
	bench.Printf("TPS:          %.2f events/s\n", tps)

	stopSignals()
	if startErr == nil {
		startErr = <-appErrCh
	}
	if startErr != nil && !errors.Is(startErr, context.Canceled) {
		logger.Error("application stopped with error", "error", startErr)
		return 1
	}
	if ok == 0 {
		logger.Error("no events were published")
		return 1
	}
	return 0
}
