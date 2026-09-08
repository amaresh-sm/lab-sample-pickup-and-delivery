package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"labgolden/internal/httpapi"
	mongostore "labgolden/internal/mongo"
	"labgolden/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	store, err := mongostore.Open(ctx, mongostore.URI())
	cancel()
	if err != nil {
		logger.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.EnsureInitialAdmin(context.Background(), os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD")); err != nil {
		logger.Error("initial administrator bootstrap failed", "error", err)
		os.Exit(1)
	}
	if err := (&service.Service{Store: store}).EnsureNamedSampleTypesSeeded(context.Background()); err != nil {
		logger.Error("named sample type bootstrap failed", "error", err)
		os.Exit(1)
	}
	frontend := getenv("FRONTEND_DIR", "/workspace/frontend/dist")
	api := httpapi.New(store, frontend, logger)
	applicationCtx, stopApplication := context.WithCancel(context.Background())
	defer stopApplication()
	go func() {
		ticker := time.NewTicker(maintenanceInterval())
		defer ticker.Stop()
		for {
			select {
			case <-applicationCtx.Done():
				return
			case <-ticker.C:
				maintenanceCtx, cancel := context.WithTimeout(applicationCtx, 1500*time.Millisecond)
				if err := api.Maintain(maintenanceCtx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Warn("timer maintenance failed", "error", err)
				}
				cancel()
			}
		}
	}()
	server := &http.Server{
		Addr:              getenv("APP_ADDR", "127.0.0.1:8080"),
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       75 * time.Second,
	}
	stopping := make(chan os.Signal, 1)
	signal.Notify(stopping, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stopping
		stopApplication()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	logger.Info("server listening", "address", server.Addr)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func maintenanceInterval() time.Duration {
	milliseconds := 2000
	if value, err := strconv.Atoi(os.Getenv("MAINTENANCE_INTERVAL_MILLISECONDS")); err == nil &&
		value >= 100 && value <= 60000 {
		milliseconds = value
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func getenv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
