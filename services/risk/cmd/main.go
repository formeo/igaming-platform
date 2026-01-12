// Package main is the entry point for Risk Service
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
	GRPCPort string
	HTTPPort string
	
	// Redis (feature store)
	RedisURL string
	
	// ClickHouse (batch features)
	ClickHouseURL string
	
	// RabbitMQ (events)
	RabbitMQURL string
	
	// ML Models
	FraudModelPath string
	LTVModelPath   string
	
	// Scoring thresholds
	BlockThreshold  int
	ReviewThreshold int
	
	// Rate limiting
	MaxTxPerMinute int
	MaxTxPerHour   int
	
	// Logging
	LogLevel string
}

// LoadConfig loads configuration from environment
func LoadConfig() Config {
	return Config{
		GRPCPort:        getEnv("GRPC_PORT", "9082"),
		HTTPPort:        getEnv("HTTP_PORT", "8082"),
		RedisURL:        getEnv("REDIS_URL", "redis://localhost:6379"),
		ClickHouseURL:   getEnv("CLICKHOUSE_URL", "http://igaming:igaming_secret@localhost:8123"),
		RabbitMQURL:     getEnv("RABBITMQ_URL", "amqp://igaming:igaming_secret@localhost:5672/"),
		FraudModelPath:  getEnv("FRAUD_MODEL_PATH", "/app/models/fraud_model.onnx"),
		LTVModelPath:    getEnv("LTV_MODEL_PATH", "/app/models/ltv_model.onnx"),
		BlockThreshold:  getEnvInt("BLOCK_THRESHOLD", 80),
		ReviewThreshold: getEnvInt("REVIEW_THRESHOLD", 50),
		MaxTxPerMinute:  getEnvInt("MAX_TX_PER_MINUTE", 10),
		MaxTxPerHour:    getEnvInt("MAX_TX_PER_HOUR", 100),
		LogLevel:        getEnv("LOG_LEVEL", "info"),
	}
}

func main() {
	// Load configuration
	cfg := LoadConfig()
	
	// Setup logger
	logger := setupLogger(cfg.LogLevel)
	logger.Info("starting risk service",
		"grpc_port", cfg.GRPCPort,
		"http_port", cfg.HTTPPort,
	)
	
	// Connect to Redis (feature store)
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
	
	// Initialize feature store
	// featureStore := features.NewRedisFeatureStore(redisClient)
	
	// Load ML models
	// fraudModel, err := ml.NewONNXModel(ml.FraudModelConfig(cfg.FraudModelPath), logger)
	// if err != nil {
	// 	logger.Warn("failed to load fraud model, using rules only", "error", err)
	// }
	// defer fraudModel.Close()
	
	// ltvModel, err := ml.NewLTVModel(cfg.LTVModelPath, logger)
	// if err != nil {
	// 	logger.Warn("failed to load LTV model", "error", err)
	// }
	// defer ltvModel.Close()
	
	// Create scoring engine
	// scoringEngine := scoring.NewScoringEngine(
	// 	featureStore,
	// 	fraudModel,
	// 	nil, // IP intelligence (optional)
	// 	featureStore, // blacklist
	// 	logger,
	// 	scoring.ScoringConfig{
	// 		BlockThreshold:  cfg.BlockThreshold,
	// 		ReviewThreshold: cfg.ReviewThreshold,
	// 		MaxTxPerMinute:  cfg.MaxTxPerMinute,
	// 		MaxTxPerHour:    cfg.MaxTxPerHour,
	// 	},
	// )
	
	// Create LTV predictor
	// ltvPredictor := prediction.NewLTVPredictor(nil, logger) // needs data source
	
	// Create gRPC server
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			loggingInterceptor(logger),
			recoveryInterceptor(logger),
			metricsInterceptor(),
		),
	)
	
	// Register risk service
	// riskv1.RegisterRiskServiceServer(grpcServer, NewRiskGRPCHandler(scoringEngine, ltvPredictor))
	
	// Register health check
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("risk.v1.RiskService", healthpb.HealthCheckResponse_SERVING)
	
	// Enable reflection
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
	
	// Create HTTP server
	mux := http.NewServeMux()
	
	// Metrics endpoint
	mux.Handle("/metrics", promhttp.Handler())
	
	// Health endpoints
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy","service":"risk"}`))
	})
	
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		// Check Redis
		if err := redisClient.Ping(context.Background()).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not ready","error":"redis"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ready"}`))
	})
	
	// Debug endpoints
	mux.HandleFunc("/debug/thresholds", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"block_threshold":%d,"review_threshold":%d}`,
			cfg.BlockThreshold, cfg.ReviewThreshold)
	})
	
	// Score explanation endpoint (for debugging)
	mux.HandleFunc("/debug/score", func(w http.ResponseWriter, r *http.Request) {
		// TODO: Accept account_id and return detailed score breakdown
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"POST with account_id to get score explanation"}`))
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
	
	// Start event consumer for feature updates
	go func() {
		logger.Info("starting event consumer for feature updates")
		// consumer, err := events.NewRabbitMQConsumer(...)
		// consumer.Subscribe("wallet.transactions", handleTransactionEvent)
		// consumer.Start(ctx)
	}()
	
	// Start batch feature updater (periodic job)
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		
		for range ticker.C {
			logger.Info("running batch feature update")
			// Update batch features from ClickHouse
			// featureStore.UpdateBatchFeatures(ctx)
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
	
	healthServer.SetServingStatus("risk.v1.RiskService", healthpb.HealthCheckResponse_NOT_SERVING)
	
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("HTTP shutdown error", "error", err)
	}
	
	grpcServer.GracefulStop()
	
	logger.Info("risk service stopped")
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
		Level:     logLevel,
		AddSource: level == "debug",
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler)
}

// gRPC Interceptors

func loggingInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		
		resp, err := handler(ctx, req)
		
		duration := time.Since(start)
		
		// Log at debug level for successful requests, info for errors
		if err != nil {
			logger.Info("grpc request",
				"method", info.FullMethod,
				"duration_ms", duration.Milliseconds(),
				"error", err.Error(),
			)
		} else {
			logger.Debug("grpc request",
				"method", info.FullMethod,
				"duration_ms", duration.Milliseconds(),
			)
		}
		
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
		// Record:
		// - Request count by method
		// - Latency histogram
		// - Error count
		// - Score distribution
		return handler(ctx, req)
	}
}
