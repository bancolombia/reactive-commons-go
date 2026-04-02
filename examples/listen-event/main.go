package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bancolombia/reactive-commons-go/pkg/async"
	"github.com/bancolombia/reactive-commons-go/rabbit"
)

type OrderCreated struct {
	OrderID    string  `json:"orderId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "notification-service"
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	err = app.Registry().ListenEvent("order.created",
		func(ctx context.Context, event async.DomainEvent[any]) error {
			raw, _ := event.Data.(json.RawMessage)
			var order OrderCreated
			if err := json.Unmarshal(raw, &order); err != nil {
				return err
			}
			logger.Info("received order.created event",
				"eventId", event.EventID,
				"orderId", order.OrderID,
				"customerId", order.CustomerID,
				"amount", order.Amount,
			)
			// Business logic: send confirmation email, update inventory, etc.
			return nil // return non-nil to nack and redeliver the message
		},
	)
	if err != nil {
		logger.Error("failed to register event handler", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("listening for events, press Ctrl+C to stop")

	if err := app.Start(ctx); err != nil {
		logger.Error("application stopped with error", "error", err)
		os.Exit(1)
	}
}
