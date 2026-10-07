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

type SendInvoice struct {
	InvoiceID  string  `json:"invoiceId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "invoice-service" // must match the target name used by the sender
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	err = app.Registry().ListenCommand("send-invoice",
		func(ctx context.Context, cmd async.Command[any]) error {
			raw, _ := cmd.Data.(json.RawMessage)
			var invoice SendInvoice
			if err := json.Unmarshal(raw, &invoice); err != nil {
				return err
			}
			logger.Info("received send-invoice command",
				"commandId", cmd.CommandID,
				"invoiceId", invoice.InvoiceID,
				"customerId", invoice.CustomerID,
				"amount", invoice.Amount,
			)
			// Business logic: generate PDF, send email, persist record, etc.
			return nil // return non-nil to nack and redeliver the message
		},
	)
	if err != nil {
		logger.Error("failed to register command handler", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("listening for commands, press Ctrl+C to stop")

	if err := app.Start(ctx); err != nil {
		logger.Error("application stopped with error", "error", err)
		os.Exit(1)
	}
}
