// Package main is a Go port of reactive-commons-java's
// `samples/async/async-sender-client` sample. It exposes a small REST API on
// port 4001 (matching the Java sample's `server.port`) that translates HTTP
// requests into reactive-commons calls — domain events, notifications,
// commands, and async queries — all targeting `appName="receiver"`.
//
// Pair this sender with `examples/async-receiver-responder` (or the Java
// receiver) running against the same broker to see the end-to-end round-trip.
//
// Differences from the Java sample:
//
//   - `DELETE /api/teams` calls `EmitNotification` (not `Emit`) so the demo
//     round-trips against the Go receiver, which subscribes via
//     `ListenNotification` (matching the Java `listenNotificationEvent`).
//   - `GET /api/animals/{event}` prepends `animals.` to the path variable so
//     the resulting routing key matches the receiver's `animals.#` binding.
//   - We mirror the receiver's `WithDLQRetry: true` and `QueueType: "quorum"`
//     so the sender's reply queue is declared with the same arguments and
//     does not collide with whatever the receiver declared first.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bancolombia/reactive-commons-go/rabbit"
)

const (
	httpAddr        = ":4001"
	shutdownTimeout = 10 * time.Second
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "sender"
	cfg.Logger = logger
	cfg.WithDLQRetry = true
	cfg.QueueType = "quorum"

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	appErrCh := make(chan error, 1)
	go func() {
		appErrCh <- app.Start(ctx)
	}()

	select {
	case <-app.Ready():
		logger.Info("reactive-commons application ready", "appName", cfg.AppName, "target", TargetAppName)
	case err := <-appErrCh:
		logger.Error("reactive-commons application failed before ready", "error", err)
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("shutdown requested before application became ready")
		<-appErrCh
		return
	}

	api := newRestAPI(app, logger)
	mux := http.NewServeMux()
	api.register(mux)

	server := &http.Server{
		Addr:              httpAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	httpErrCh := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", httpAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
			return
		}
		httpErrCh <- nil
	}()

	appExited := false
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, stopping HTTP server")
	case err := <-httpErrCh:
		if err != nil {
			logger.Error("HTTP server failed", "error", err)
		}
	case err := <-appErrCh:
		appExited = true
		if err != nil {
			logger.Error("reactive-commons application stopped with error", "error", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}

	stop()
	if !appExited {
		if err := <-appErrCh; err != nil {
			logger.Error("reactive-commons application stopped with error", "error", err)
			os.Exit(1)
		}
	}
	logger.Info("sender stopped cleanly")
}
