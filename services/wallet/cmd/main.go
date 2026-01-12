// Package main is the entry point for Wallet Service
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// Config holds service configuration
type Config struct {
	// Server
	GRPCPort    string
	HTTPPort    string
	
	// Database
	DatabaseURL string
	
	// Redis
	RedisURL    string
	
	// RabbitMQ
	RabbitMQURL string
	
	// Risk Service
	RiskServiceURL string
	
	// Thresholds
	RiskBlockThreshold  int
	RiskReviewThreshold int
	
	// Logging
	LogLevel string
}

// LoadConfig loads configuration from environment
func LoadConfig() Config {
	return Config{
		GRPCPort:            getEnv("GRPC_PORT", "9080"),
		HTTPPort:            getEnv("HTTP_PORT", "8080"),
		DatabaseURL:         getEnv("DATABASE_URL", "postgres://igaming:igaming_secret@localhost:5432/igaming?sslmode=disable"),
		RedisURL:            getEnv("REDIS_URL", "redis://localhost:6379"),
		RabbitMQURL:         getEnv("RABBITMQ_URL", "amqp://igaming:igaming_secret@localhost:5672/"),
		RiskServiceURL:      getEnv("RISK_SERVICE_URL", "localhost:9082"),
		RiskBlockThreshold:  getEnvInt("RISK_BLOCK_THRESHOLD", 80),
		RiskReviewThreshold: getEnvInt("RISK_REVIEW_THRESHOLD", 50),
		LogLevel:            getEnv("LOG_LEVEL", "info"),
	}
}

func main() {
	// Load configuration
	cfg := LoadConfig()
	
	// Setup logger
	logger := setupLogger(cfg.LogLevel)
	logger.Info("starting wallet service",
		"grpc_port", cfg.GRPCPort,
		"http_port", cfg.HTTPPort,
	)
	
	// Connect to PostgreSQL
	db, err := sqlx.Connect("postgres", cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	
	// Configure connection pool
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	
	// Ping database
	if err := db.Ping(); err != nil {
		logger.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	logger.Info("connected to PostgreSQL")
	
	// Connect to Redis
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		logger.Error("failed to parse Redis URL", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(redisOpts)
	defer redisClient.Close()
	
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		logger.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	logger.Info("connected to Redis")
	
	// Create repositories
	// accountRepo := repository.NewPostgresAccountRepository(db)
	// txRepo := repository.NewPostgresTransactionRepository(db)
	// ledgerRepo := repository.NewPostgresLedgerRepository(db)
	
	// Create event publisher
	// publisher, err := events.NewRabbitMQPublisher(events.DefaultPublisherConfig(cfg.RabbitMQURL), logger)
	// if err != nil {
	// 	logger.Warn("failed to connect to RabbitMQ, events disabled", "error", err)
	// }
	
	// Create risk service client
	// riskClient := NewRiskClient(cfg.RiskServiceURL, logger)
	
	// Create wallet service
	// walletService := service.NewWalletService(
	// 	accountRepo, txRepo, ledgerRepo,
	// 	publisher, riskClient, logger,
	// 	service.Config{
	// 		RiskThresholdBlock:  cfg.RiskBlockThreshold,
	// 		RiskThresholdReview: cfg.RiskReviewThreshold,
	// 	},
	// )
	
	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			loggingInterceptor(logger),
			recoveryInterceptor(logger),
			metricsInterceptor(),
		),
	)
	
	// Register wallet service
	// walletv1.RegisterWalletServiceServer(grpcServer, NewWalletGRPCHandler(walletService))
	
	// Register health check
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("wallet.v1.WalletService", healthpb.HealthCheckResponse_SERVING)
	
	// Enable reflection for debugging
	reflection.Register(grpcServer)
	
	// Start gRPC server
	grpcListener, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		logger.Error("failed to listen", "error", err)
		os.Exit(1)
	}
	
	go func() {
		logger.Info("gRPC server listening", "port", cfg.GRPCPort)
		if err := grpcServer.Serve(grpcListener); err != nil {
			logger.Error("gRPC server error", "error", err)
		}
	}()
	
	// Create HTTP server for metrics and health
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		// Check database and redis
		if err := db.Ping(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not ready","error":"database"}`))
			return
		}
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not ready","error":"redis"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	})
	
	httpServer := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	
	go func() {
		logger.Info("HTTP server listening", "port", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			logger.Error("HTTP server error", "error", err)
		}
	}()
	
	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	
	logger.Info("shutting down...")
	
	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	// Stop accepting new requests
	healthServer.SetServingStatus("wallet.v1.WalletService", healthpb.HealthCheckResponse_NOT_SERVING)
	
	// Shutdown HTTP server
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("HTTP shutdown error", "error", err)
	}
	
	// Shutdown gRPC server
	grpcServer.GracefulStop()
	
	logger.Info("wallet service stopped")
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var v int
		fmt.Sscanf(value, "%d", &v)
		return v
	}
	return defaultValue
}

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
	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler)
}

// gRPC interceptors

func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		
		resp, err := handler(ctx, req)
		
		duration := time.Since(start)
		logger.Info("grpc request",
			"method", info.FullMethod,
			"duration_ms", duration.Milliseconds(),
			"error", err,
		)
		
		return resp, err
	}
}

func recoveryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("panic recovered",
					"method", info.FullMethod,
					"panic", r,
				)
				err = fmt.Errorf("internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

func metricsInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// TODO: Add Prometheus metrics
		return handler(ctx, req)
	}
}
