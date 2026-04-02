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

type SendInvoice struct {
	InvoiceID  string  `json:"invoiceId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "billing-service"
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

	// Wait until the broker topology is ready before sending commands
	<-app.Ready()
	logger.Info("application ready, starting to send commands")

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down command sender")
			return
		case <-ticker.C:
			invoiceData := SendInvoice{
				InvoiceID:  uuid.New().String(),
				CustomerID: "customer-42",
				Amount:     250.00,
			}
			cmd := async.Command[any]{
				Name:      "send-invoice",
				CommandID: uuid.New().String(),
				Data:      invoiceData,
			}

			// "invoice-service" is the AppName of the target application
			if err := app.Gateway().SendCommand(ctx, cmd, "invoice-service"); err != nil {
				logger.Error("failed to send command", "error", err)
				continue
			}
			logger.Info("command sent", "commandId", cmd.CommandID, "invoiceId", invoiceData.InvoiceID)
		}
	}
}
