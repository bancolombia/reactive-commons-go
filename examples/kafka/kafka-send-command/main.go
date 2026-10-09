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

type SendInvoice struct {
	InvoiceID  string  `json:"invoiceId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "billing-service"
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

			// "invoice-service" is the AppName of the target application; its
			// commands topic defaults to invoice-service.commands.
			if err := app.Gateway().SendCommand(ctx, cmd, "invoice-service"); err != nil {
				logger.Error("failed to send command", "err", err)
				continue
			}
			logger.Info("command sent", "commandId", cmd.CommandID, "invoiceId", invoiceData.InvoiceID)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
