// Command benchmark-listen-event measures RabbitMQ domain-event consumption
// throughput. It registers a minimal handler, waits for the first event, runs
// for BENCH_DURATION (default 5m), and prints the achieved deliveries per
// second. Feed it with benchmark-emit-event using the same BENCH_EVENT_NAME.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bancolombia/reactive-commons-go/examples/rabbitmq/internal/bench"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
)

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
	prefetch, err := bench.EnvInt("BENCH_PREFETCH", 250)
	if err != nil || prefetch <= 0 {
		logger.Error("invalid BENCH_PREFETCH", "error", err)
		return 1
	}
	startTimeout, err := bench.EnvDuration("BENCH_START_TIMEOUT", 2*time.Minute)
	if err != nil || startTimeout <= 0 {
		logger.Error("invalid BENCH_START_TIMEOUT", "error", err)
		return 1
	}
	eventName := bench.EnvString("BENCH_EVENT_NAME", "benchmark.event")
	if err := bench.ValidateEventName(eventName); err != nil {
		logger.Error("invalid BENCH_EVENT_NAME", "error", err)
		return 1
	}
	baseAppName := bench.EnvString("BENCH_APP_NAME", "bench-listener")
	// A unique app name gives each run a fresh, empty {app}.subsEvents queue,
	// so a leftover backlog from a previous run cannot skew the measurement.
	appName := bench.AppNameWithRunSuffix(baseAppName)

	cfg, err := bench.RabbitConfig(appName)
	if err != nil {
		logger.Error("failed to build RabbitMQ config", "error", err)
		return 1
	}
	cfg.PrefetchCount = prefetch
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		return 1
	}

	var received atomic.Uint64
	firstEvent := make(chan time.Time, 1)

	// The handler only counts. Keeping it free of logging and payload work
	// makes the result reflect framework dispatch and ack overhead.
	err = app.Registry().ListenEvent(eventName,
		func(_ context.Context, _ async.DomainEvent[any]) error {
			received.Add(1)
			select {
			case firstEvent <- time.Now():
			default:
			}
			return nil
		})
	if err != nil {
		logger.Error("failed to register event handler", "error", err)
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

	queueName := appName + ".subsEvents"

	bench.Printf("=== RabbitMQ domain-event listen benchmark ===\n")
	bench.Printf("broker:       %s:%d vhost=%q\n", cfg.Host, cfg.Port, cfg.VirtualHost)
	bench.Printf("event:        %s\n", eventName)
	bench.Printf("app:          %s\n", appName)
	bench.Printf("queue:        %s\n", queueName)
	bench.Printf("prefetch:     %d\n", prefetch)
	bench.Printf("duration:     %s\n", duration)
	bench.Printf("waiting for the first event - start benchmark-emit-event with the same BENCH_EVENT_NAME\n\n")

	startTimer := time.NewTimer(startTimeout)
	defer startTimer.Stop()

	var t0 time.Time
	var startErr error
	status := "complete"

	select {
	case t0 = <-firstEvent:
		// Start the measurement clock on the first delivery so the emitter's
		// compile/startup time is excluded from this run.
		bench.Printf("first event received - measuring for %s\n\n", duration)
	case <-startTimer.C:
		logger.Error("no events received before the start timeout; is the emitter running with the same BENCH_EVENT_NAME?",
			"event", eventName, "timeout", startTimeout)
		stopSignals()
		<-appErrCh
		return 1
	case <-appCtx.Done():
		status = "interrupted"
	case startErr = <-appErrCh:
		status = "app stopped"
	}

	if !t0.IsZero() {
		measure := time.NewTimer(duration)
		defer measure.Stop()
		select {
		case <-measure.C:
		case <-appCtx.Done():
			status = "interrupted"
		case startErr = <-appErrCh:
			status = "app stopped"
		}
	}

	stopSignals()
	if startErr == nil {
		startErr = <-appErrCh
	}
	count := received.Load()

	bench.Printf("=== result ===\n")
	bench.Printf("status:       %s\n", status)
	bench.Printf("queue:        %s\n", queueName)
	if t0.IsZero() {
		bench.Printf("received:     %d\n", count)
		bench.Printf("TPS:          n/a (no events received)\n")
	} else {
		elapsed := time.Since(t0)
		bench.Printf("elapsed:      %s\n", elapsed)
		bench.Printf("received:     %d\n", count)
		bench.Printf("TPS:          %.2f events/s\n", float64(count)/elapsed.Seconds())
	}

	if startErr != nil && !errors.Is(startErr, context.Canceled) {
		logger.Error("application stopped with error", "error", startErr)
		return 1
	}
	if t0.IsZero() || count == 0 {
		return 1
	}
	return 0
}
