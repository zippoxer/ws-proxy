package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
		"max_message_size", cfg.MaxMessageSize,
		"connect_timeout", cfg.ConnectTimeout,
		"read_timeout", cfg.ReadTimeout,
		"write_timeout", cfg.WriteTimeout,
		"max_connections", cfg.MaxConnections,
		"trust_proxy_headers", cfg.TrustProxyHeaders,
		"allowed_origins", cfg.AllowedOrigins,
	)

	// Create proxy handler
	handler := NewProxyHandler(cfg, logger)

	// Create HTTP server with timeouts to prevent slowloris attacks
	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second, // Time to read request headers
		IdleTimeout:       120 * time.Second, // Keep-alive timeout
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
	ListenAddr        string
	BackendAddr       string
	LogLevel          string
	MaxMessageSize    int64
	ConnectTimeout    time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	MaxConnections    int
	TrustProxyHeaders bool     // Trust X-Forwarded-For/X-Real-IP headers
	AllowedOrigins    []string // Allowed WebSocket origins (empty = allow all)
}

// Default configuration values (HAProxy-inspired)
const (
	DefaultMaxMessageSize = 1048576           // 1MB
	DefaultConnectTimeout = 5 * time.Second   // HAProxy default
	DefaultReadTimeout    = 60 * time.Second  // Idle timeout
	DefaultWriteTimeout   = 10 * time.Second
	DefaultMaxConnections = 0                 // Unlimited
)

// loadConfig reads configuration from environment variables.
func loadConfig() Config {
	cfg := Config{
		ListenAddr:        getEnv("LISTEN_ADDR", ":8080"),
		BackendAddr:       getEnv("BACKEND_ADDR", ""),
		LogLevel:          getEnv("LOG_LEVEL", "info"),
		MaxMessageSize:    getEnvInt64("MAX_MESSAGE_SIZE", DefaultMaxMessageSize),
		ConnectTimeout:    getEnvDuration("CONNECT_TIMEOUT", DefaultConnectTimeout),
		ReadTimeout:       getEnvDuration("READ_TIMEOUT", DefaultReadTimeout),
		WriteTimeout:      getEnvDuration("WRITE_TIMEOUT", DefaultWriteTimeout),
		MaxConnections:    getEnvInt("MAX_CONNECTIONS", DefaultMaxConnections),
		TrustProxyHeaders: getEnvBool("TRUST_PROXY_HEADERS", false),
		AllowedOrigins:    getEnvStringSlice("ALLOWED_ORIGINS"),
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

// getEnvInt returns an integer environment variable or a default value.
func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
	}
	return defaultValue
}

// getEnvInt64 returns an int64 environment variable or a default value.
func getEnvInt64(key string, defaultValue int64) int64 {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	}
	return defaultValue
}

// getEnvDuration returns a duration environment variable or a default value.
// Accepts formats like "5s", "1m", "500ms".
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if d, err := time.ParseDuration(value); err == nil {
			return d
		}
	}
	return defaultValue
}

// getEnvBool returns a boolean environment variable or a default value.
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

// getEnvStringSlice returns a slice from a comma-separated environment variable.
func getEnvStringSlice(key string) []string {
	value := os.Getenv(key)
	if value == "" {
		return nil
	}
	var result []string
	for _, s := range strings.Split(value, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			result = append(result, s)
		}
	}
	return result
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
