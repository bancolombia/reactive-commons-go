package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/google/uuid"
)

type CacheInvalidated struct {
	Key    string `json:"key"`
	Reason string `json:"reason"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "cache-service"
	cfg.BootstrapBrokers = []string{envOr("KAFKA_BOOTSTRAP", "localhost:9092")}
	cfg.AllowAutoCreateTopics = true
	cfg.Logger = logger

	app, err := rckafka.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "err", err)
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
	logger.Info("application ready", "topic", cfg.AppName+".cache.invalidated")

	n := async.Notification[any]{
		Name:    "cache.invalidated",
		EventID: uuid.NewString(),
		Data:    CacheInvalidated{Key: "user:42", Reason: "ttl-expired"},
	}

	emitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := app.EventBus().EmitNotification(emitCtx, n); err != nil {
		logger.Error("emit notification failed", "err", err)
		stop()
		return
	}
	logger.Info("emitted notification", "eventId", n.EventID, "topic", cfg.AppName+".cache.invalidated")
	stop()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
