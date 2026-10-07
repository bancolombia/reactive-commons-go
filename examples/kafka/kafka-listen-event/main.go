package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "user-service"
	cfg.BootstrapBrokers = []string{envOr("KAFKA_BOOTSTRAP", "localhost:9092")}
	cfg.AllowAutoCreateTopics = true
	cfg.Logger = logger

	app, err := rckafka.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "err", err)
		os.Exit(1)
	}

	if err := app.Registry().ListenEvent("user.created", func(ctx context.Context, ev async.DomainEvent[any]) error {
		logger.Info("received event", "name", ev.Name, "eventId", ev.EventID, "data", ev.Data)
		return nil
	}); err != nil {
		logger.Error("failed to register handler", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := app.Start(ctx); err != nil {
			logger.Error("application stopped with error", "err", err)
		}
	}()

	<-app.Ready()
	logger.Info("listening", "topic", cfg.AppName+".user.created")

	<-ctx.Done()
	logger.Info("shutting down")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
