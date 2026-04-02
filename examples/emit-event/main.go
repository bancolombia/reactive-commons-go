package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
	"github.com/google/uuid"
)

type OrderCreated struct {
	OrderID    string  `json:"orderId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "order-service"
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := app.Start(ctx); err != nil {
			logger.Error("application stopped with error", "error", err)
		}
	}()

	// Wait until the broker topology is ready before publishing
	<-app.Ready()
	logger.Info("application ready, starting to emit events")

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down emitter")
			return
		case <-ticker.C:
			orderData := OrderCreated{
				OrderID:    uuid.New().String(),
				CustomerID: "customer-42",
				Amount:     99.99,
			}
			event := async.DomainEvent[any]{
				Name:    "order.created",
				EventID: uuid.New().String(),
				Data:    orderData,
			}

			if err := app.EventBus().Emit(ctx, event); err != nil {
				logger.Error("failed to emit event", "error", err)
				continue
			}
			logger.Info("event emitted", "eventId", event.EventID, "orderId", orderData.OrderID)
		}
	}
}
