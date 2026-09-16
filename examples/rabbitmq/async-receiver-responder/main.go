// Package main is a Go port of reactive-commons-java's
// `samples/async/async-receiver-responder` sample. It registers handlers for
// every reactive-commons messaging pattern (queries, commands, events,
// notifications) under appName="receiver" so it can be driven by either a Go
// or a Java sender pointed at the same broker.
//
// Differences from the Java sample:
//
//   - The Java sample uses DynamicRegistry to subscribe to `animals.dogs`,
//     `animals.cats` and `animals.cats.angry` after startup. reactive-commons-go
//     only binds queues during Start(), so this example uses the wildcard
//     `animals.#` instead. The Go registry resolves the wildcard at dispatch
//     time, so every animal event the Java sample would have received is
//     delivered here as well.
//   - The Java sample sets `withDLQRetry: true` and `queueType: quorum`. We
//     mirror both on the Go side via `cfg.WithDLQRetry` and `cfg.QueueType`
//     so the queue arguments match the ones the Java sample declares
//     (otherwise the broker rejects redeclaration with PRECONDITION_FAILED).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bancolombia/reactive-commons-go/rabbit"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "receiver"
	cfg.Logger = logger
	cfg.WithDLQRetry = true
	cfg.QueueType = "quorum"

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	uc := NewUseCase(logger)
	if err := RegisterHandlers(app, uc); err != nil {
		logger.Error("failed to register handlers", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("receiver-responder started, press Ctrl+C to stop", "appName", cfg.AppName)

	if err := app.Start(ctx); err != nil {
		logger.Error("application stopped with error", "error", err)
		os.Exit(1)
	}
}
