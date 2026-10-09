package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	rckafka "github.com/bancolombia/reactive-commons-go/kafka"
	"github.com/bancolombia/reactive-commons-go/pkg/async"
)

type GetProductRequest struct {
	ProductID string `json:"productId"`
}

type Product struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Stock     int     `json:"stock"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rckafka.NewConfigWithDefaults()
	cfg.AppName = "storefront-service"
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

	// Wait until the reply consumer is primed before sending queries
	<-app.Ready()
	logger.Info("application ready, starting to send queries", "repliesTopic", cfg.AppName+".replies")

	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	productIDs := []string{"SKU-001", "SKU-002", "SKU-003"}
	i := 0

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down query requester")
			return
		case <-ticker.C:
			productID := productIDs[i%len(productIDs)]
			i++

			queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)

			raw, err := app.Gateway().RequestReply(queryCtx, async.AsyncQuery[any]{
				Resource:  "get-product",
				QueryData: GetProductRequest{ProductID: productID},
			}, "catalog-service") // "catalog-service" must match the server's AppName
			cancel()

			if err != nil {
				logger.Error("query failed", "productId", productID, "err", err)
				continue
			}

			var product Product
			if err := json.Unmarshal(raw, &product); err != nil {
				logger.Error("failed to decode reply", "err", err)
				continue
			}

			logger.Info("query reply received",
				"productId", product.ProductID,
				"name", product.Name,
				"price", product.Price,
				"stock", product.Stock,
			)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
