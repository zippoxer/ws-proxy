package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Parse configuration from environment
	cfg := loadConfig()

	// Setup logger
	logger := setupLogger(cfg.LogLevel)
	logger.Info("starting ws-proxy",
		"listen_addr", cfg.ListenAddr,
		"backend_addr", cfg.BackendAddr,
	)

	// Create proxy handler
	handler := &ProxyHandler{
		BackendAddr: cfg.BackendAddr,
		Logger:      logger,
	}

	// Create HTTP server
	server := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: handler,
	}

	// Channel to signal server shutdown
	done := make(chan struct{})

	// Handle graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("received shutdown signal", "signal", sig)

		// Give connections 10 seconds to finish
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(ctx); err != nil {
			logger.Error("server shutdown error", "error", err)
		}
		close(done)
	}()

	// Start server
	logger.Info("server listening", "addr", cfg.ListenAddr)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}

	<-done
	logger.Info("server stopped")
}

// Config holds the application configuration.
type Config struct {
	ListenAddr  string
	BackendAddr string
	LogLevel    string
}

// loadConfig reads configuration from environment variables.
func loadConfig() Config {
	cfg := Config{
		ListenAddr:  getEnv("LISTEN_ADDR", ":8080"),
		BackendAddr: getEnv("BACKEND_ADDR", ""),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
	}

	if cfg.BackendAddr == "" {
		slog.Error("BACKEND_ADDR environment variable is required")
		os.Exit(1)
	}

	return cfg
}

// getEnv returns the value of an environment variable or a default value.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// setupLogger creates a structured logger with the specified level.
func setupLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	return slog.New(handler)
}
