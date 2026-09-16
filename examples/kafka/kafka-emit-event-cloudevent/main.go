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

type UserCreated struct {
	UserID string `json:"userId"`
	Email  string `json:"email"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "billing"
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
	logger.Info("application ready", "topic", cfg.AppName+".user.created")

	payload := UserCreated{UserID: "u-42", Email: "u42@example.com"}
	ev := async.DomainEvent[any]{
		Name:    "user.created",
		EventID: uuid.NewString(),
		Data:    rckafka.WrapAsCloudEvent("billing", "user.created", payload),
	}

	emitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := app.EventBus().Emit(emitCtx, ev); err != nil {
		logger.Error("emit failed", "err", err)
		stop()
		return
	}
	logger.Info("emitted event with CloudEvent-in-data", "eventId", ev.EventID)
	stop()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
