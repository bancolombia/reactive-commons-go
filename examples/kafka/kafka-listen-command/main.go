package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

type SendInvoice struct {
	InvoiceID  string  `json:"invoiceId"`
	CustomerID string  `json:"customerId"`
	Amount     float64 `json:"amount"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "invoice-service" // must match the target name used by the sender
	cfg.BootstrapBrokers = []string{envOr("KAFKA_BOOTSTRAP", "localhost:9092")}
	cfg.AllowAutoCreateTopics = true
	cfg.Logger = logger

	app, err := rckafka.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "err", err)
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
			// Return a non-nil error to retry and eventually route to the DLQ.
			return nil
		},
	)
	if err != nil {
		logger.Error("failed to register command handler", "err", err)
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
	logger.Info("listening for commands, press Ctrl+C to stop", "topic", cfg.AppName+".commands")

	<-ctx.Done()
	logger.Info("shutting down")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
