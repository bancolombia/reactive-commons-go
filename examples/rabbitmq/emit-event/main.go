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

	appErrCh := make(chan error, 1)
	go func() {
		appErrCh <- app.Start(ctx)
	}()

	// Wait until the broker topology is ready before publishing. Also watch for
	// a startup failure so an unreachable broker does not block forever.
	select {
	case <-app.Ready():
		logger.Info("application ready, starting to emit events")
	case err := <-appErrCh:
		logger.Error("application stopped before ready", "error", err)
		return
	}

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

			// Bound each send: SendWithConfirm returns when the broker confirms
			// or the context expires.
			emitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := app.EventBus().Emit(emitCtx, event)
			cancel()
			if err != nil {
				logger.Error("failed to emit event", "error", err)
				continue
			}
			logger.Info("event emitted", "eventId", event.EventID, "orderId", orderData.OrderID)
		}
	}
}
