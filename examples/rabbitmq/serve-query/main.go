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

type GetProductRequest struct {
	ProductID string `json:"productId"`
}

type Product struct {
	ProductID string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Stock     int     `json:"stock"`
}

var catalog = map[string]Product{
	"SKU-001": {ProductID: "SKU-001", Name: "Wireless Headphones", Price: 79.99, Stock: 42},
	"SKU-002": {ProductID: "SKU-002", Name: "Mechanical Keyboard", Price: 129.99, Stock: 15},
	"SKU-003": {ProductID: "SKU-003", Name: "USB-C Hub", Price: 39.99, Stock: 200},
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := rabbit.NewConfigWithDefaults()
	cfg.AppName = "catalog-service" // must match the target name used by the requester
	cfg.Logger = logger

	app, err := rabbit.NewApplication(cfg)
	if err != nil {
		logger.Error("failed to create application", "error", err)
		os.Exit(1)
	}

	err = app.Registry().ServeQuery("get-product",
		func(ctx context.Context, query async.AsyncQuery[any], from async.From) (any, error) {
			raw, _ := query.QueryData.(json.RawMessage)
			var req GetProductRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}

			logger.Info("serving get-product query", "productId", req.ProductID)

			product, ok := catalog[req.ProductID]
			if !ok {
				logger.Warn("product not found", "productId", req.ProductID)
				return Product{ProductID: req.ProductID, Name: "Unknown", Price: 0, Stock: 0}, nil
			}

			return product, nil
		},
	)
	if err != nil {
		logger.Error("failed to register query handler", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("serving queries, press Ctrl+C to stop")

	if err := app.Start(ctx); err != nil {
		logger.Error("application stopped with error", "error", err)
		os.Exit(1)
	}
}
