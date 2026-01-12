// Package ml implements ML model inference using ONNX Runtime
package ml

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ONNXModel wraps ONNX Runtime for fraud detection model
type ONNXModel struct {
	session     *ort.AdvancedSession
	inputName   string
	outputName  string
	inputShape  ort.Shape
	logger      *slog.Logger
	
	mu sync.RWMutex
}

// ModelConfig for loading models
type ModelConfig struct {
	ModelPath   string
	InputName   string
	OutputName  string
	InputShape  []int64
}

// FraudModelConfig default config for fraud detection model
func FraudModelConfig(modelPath string) ModelConfig {
	return ModelConfig{
		ModelPath:  modelPath,
		InputName:  "input",
		OutputName: "output",
		InputShape: []int64{1, 30}, // batch_size=1, features=30
	}
}

// NewONNXModel loads and initializes ONNX model
func NewONNXModel(cfg ModelConfig, logger *slog.Logger) (*ONNXModel, error) {
	// Initialize ONNX Runtime
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("failed to initialize ONNX runtime: %w", err)
	}
	
	// Check if model file exists
	if _, err := os.Stat(cfg.ModelPath); os.IsNotExist(err) {
		logger.Warn("model file not found, using mock predictions", "path", cfg.ModelPath)
		return &ONNXModel{
			session:    nil, // Will use mock predictions
			inputName:  cfg.InputName,
			outputName: cfg.OutputName,
			inputShape: ort.NewShape(cfg.InputShape...),
			logger:     logger,
		}, nil
	}
	
	// Load model
	session, err := ort.NewAdvancedSession(
		cfg.ModelPath,
		[]string{cfg.InputName},
		[]string{cfg.OutputName},
		nil, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ONNX session: %w", err)
	}
	
	logger.Info("ONNX model loaded", "path", cfg.ModelPath)
	
	return &ONNXModel{
		session:    session,
		inputName:  cfg.InputName,
		outputName: cfg.OutputName,
		inputShape: ort.NewShape(cfg.InputShape...),
		logger:     logger,
	}, nil
}

// FeatureVector input for fraud model
// Must match training feature order
type FeatureVector struct {
	// Velocity (0-4)
	TxCount1Min   float32
	TxCount5Min   float32
	TxCount1Hour  float32
	TxSum1Hour    float32
	TxAvg1Hour    float32
	
	// Device (5-8)
	UniqueDevices24h float32
	UniqueIPs24h     float32
	IPCountryChanges float32
	DeviceAgeDays    float32
	
	// Account (9-14)
	AccountAgeDays    float32
	TotalDeposits     float32
	TotalWithdrawals  float32
	NetDeposit        float32
	DepositCount      float32
	WithdrawCount     float32
	
	// Behavioral (15-18)
	TimeSinceLastTx   float32
	SessionDuration   float32
	AvgBetSize        float32
	WinRate           float32
	
	// Risk indicators (19-22)
	IsVPN           float32 // 0 or 1
	IsProxy         float32
	IsTor           float32
	DisposableEmail float32
	
	// Bonus (23-25)
	BonusClaimCount   float32
	BonusWagerRate    float32
	BonusOnlyPlayer   float32 // 0 or 1
	
	// Transaction context (26-29)
	TxAmount         float32
	TxTypeDeposit    float32 // one-hot
	TxTypeWithdraw   float32
	TxTypeBet        float32
}

// ToSlice converts feature vector to float32 slice
func (f *FeatureVector) ToSlice() []float32 {
	return []float32{
		f.TxCount1Min,
		f.TxCount5Min,
		f.TxCount1Hour,
		f.TxSum1Hour,
		f.TxAvg1Hour,
		f.UniqueDevices24h,
		f.UniqueIPs24h,
		f.IPCountryChanges,
		f.DeviceAgeDays,
		f.AccountAgeDays,
		f.TotalDeposits,
		f.TotalWithdrawals,
		f.NetDeposit,
		f.DepositCount,
		f.WithdrawCount,
		f.TimeSinceLastTx,
		f.SessionDuration,
		f.AvgBetSize,
		f.WinRate,
		f.IsVPN,
		f.IsProxy,
		f.IsTor,
		f.DisposableEmail,
		f.BonusClaimCount,
		f.BonusWagerRate,
		f.BonusOnlyPlayer,
		f.TxAmount,
		f.TxTypeDeposit,
		f.TxTypeWithdraw,
		f.TxTypeBet,
	}
}

// Normalize applies feature normalization
func (f *FeatureVector) Normalize() {
	// Log transform for skewed features
	f.TxSum1Hour = logTransform(f.TxSum1Hour)
	f.TotalDeposits = logTransform(f.TotalDeposits)
	f.TotalWithdrawals = logTransform(f.TotalWithdrawals)
	f.TxAmount = logTransform(f.TxAmount)
	
	// Scale to [0, 1] for count features
	f.TxCount1Min = minMaxScale(f.TxCount1Min, 0, 20)
	f.TxCount5Min = minMaxScale(f.TxCount5Min, 0, 50)
	f.TxCount1Hour = minMaxScale(f.TxCount1Hour, 0, 200)
	f.UniqueDevices24h = minMaxScale(f.UniqueDevices24h, 0, 10)
	f.UniqueIPs24h = minMaxScale(f.UniqueIPs24h, 0, 20)
	f.AccountAgeDays = minMaxScale(f.AccountAgeDays, 0, 365)
	f.TimeSinceLastTx = minMaxScale(f.TimeSinceLastTx, 0, 86400) // 1 day
}

func logTransform(x float32) float32 {
	if x <= 0 {
		return 0
	}
	return float32(log1p(float64(x)))
}

func log1p(x float64) float64 {
	return x // Simplified, use math.Log1p in production
}

func minMaxScale(x, min, max float32) float32 {
	if x < min {
		return 0
	}
	if x > max {
		return 1
	}
	return (x - min) / (max - min)
}

// Predict runs inference on feature vector
func (m *ONNXModel) Predict(ctx context.Context, features *FeatureVector) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Normalize features
	features.Normalize()
	
	// If no model loaded, use mock prediction
	if m.session == nil {
		return m.mockPredict(features), nil
	}
	
	// Convert to tensor
	inputData := features.ToSlice()
	inputTensor, err := ort.NewTensor(m.inputShape, inputData)
	if err != nil {
		return 0, fmt.Errorf("failed to create input tensor: %w", err)
	}
	defer inputTensor.Destroy()
	
	// Create output tensor
	outputShape := ort.NewShape(1, 1)
	outputData := make([]float32, 1)
	outputTensor, err := ort.NewTensor(outputShape, outputData)
	if err != nil {
		return 0, fmt.Errorf("failed to create output tensor: %w", err)
	}
	defer outputTensor.Destroy()
	
	// Run inference
	err = m.session.Run()
	if err != nil {
		return 0, fmt.Errorf("failed to run inference: %w", err)
	}
	
	// Get output (probability of fraud)
	result := outputData[0]
	
	// Clamp to [0, 1]
	if result < 0 {
		result = 0
	}
	if result > 1 {
		result = 1
	}
	
	return float64(result), nil
}

// mockPredict provides rule-based prediction when model is not available
func (m *ONNXModel) mockPredict(f *FeatureVector) float64 {
	var score float64
	
	// High velocity = suspicious
	if f.TxCount1Min > 0.5 { // normalized, so > 10 tx/min
		score += 0.2
	}
	if f.TxCount1Hour > 0.5 { // > 100 tx/hour
		score += 0.15
	}
	
	// Multiple devices/IPs
	if f.UniqueDevices24h > 0.3 { // > 3 devices
		score += 0.15
	}
	if f.UniqueIPs24h > 0.25 { // > 5 IPs
		score += 0.1
	}
	
	// VPN/Proxy
	if f.IsVPN > 0 || f.IsProxy > 0 {
		score += 0.15
	}
	if f.IsTor > 0 {
		score += 0.25
	}
	
	// New account + large transaction
	if f.AccountAgeDays < 0.02 && f.TxAmount > 0.5 { // < 7 days, large tx
		score += 0.2
	}
	
	// Bonus abuse signals
	if f.BonusOnlyPlayer > 0 {
		score += 0.15
	}
	
	// Rapid deposit-withdraw
	if f.TimeSinceLastTx < 0.01 && f.TxTypeWithdraw > 0 { // < 15 min
		if f.TotalWithdrawals > f.TotalDeposits*0.8 {
			score += 0.2
		}
	}
	
	// Clamp to [0, 1]
	if score > 1 {
		score = 1
	}
	
	return score
}

// PredictBatch runs inference on multiple feature vectors
func (m *ONNXModel) PredictBatch(ctx context.Context, batch []*FeatureVector) ([]float64, error) {
	results := make([]float64, len(batch))
	
	// For now, sequential prediction
	// TODO: Implement batch inference for better performance
	for i, features := range batch {
		score, err := m.Predict(ctx, features)
		if err != nil {
			m.logger.Warn("prediction failed", "index", i, "error", err)
			score = 0.5 // neutral on error
		}
		results[i] = score
	}
	
	return results, nil
}

// GetFeatureImportance returns feature importance from model
func (m *ONNXModel) GetFeatureImportance() map[string]float64 {
	// In production, this would come from the trained model
	// For now, return static importance based on domain knowledge
	return map[string]float64{
		"is_vpn":            0.15,
		"is_tor":            0.12,
		"tx_count_1min":     0.10,
		"unique_devices":    0.10,
		"account_age":       0.09,
		"tx_amount":         0.08,
		"bonus_only_player": 0.08,
		"unique_ips":        0.07,
		"time_since_last":   0.06,
		"net_deposit":       0.05,
		"other":             0.10,
	}
}

// Close releases model resources
func (m *ONNXModel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.session != nil {
		m.session.Destroy()
	}
	return nil
}

// ModelMetrics for monitoring
type ModelMetrics struct {
	TotalPredictions int64
	AvgLatencyMs     float64
	ErrorCount       int64
	HighRiskCount    int64  // score > 0.7
	BlockedCount     int64  // score > 0.8
}

// LTVModel for lifetime value prediction
type LTVModel struct {
	session *ort.AdvancedSession
	logger  *slog.Logger
	mu      sync.RWMutex
}

// LTVFeatures for LTV prediction
type LTVFeatures struct {
	DaysSinceRegistration float32
	DaysSinceLastDeposit  float32
	DaysSinceLastBet      float32
	TotalActiveDays       float32
	SessionsPerWeek       float32
	TotalDeposits         float32
	TotalWithdrawals      float32
	NetRevenue            float32
	AvgDepositAmount      float32
	DepositFrequency      float32
	TotalBets             float32
	BetCount              float32
	WinRate               float32
	AvgBetSize            float32
	GamesPlayed           float32
	BonusesClaimed        float32
	BonusConversionRate   float32
}

// NewLTVModel creates LTV prediction model
func NewLTVModel(modelPath string, logger *slog.Logger) (*LTVModel, error) {
	// Similar to fraud model, but with different architecture
	return &LTVModel{
		session: nil, // Use mock for now
		logger:  logger,
	}, nil
}

// Predict estimates lifetime value
func (m *LTVModel) Predict(ctx context.Context, features *LTVFeatures) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Mock prediction based on simple formula
	// LTV = (monthly_value * remaining_months) + current_value
	
	currentValue := float64(features.NetRevenue)
	
	// Estimate monthly value
	daysActive := float64(features.DaysSinceRegistration)
	if daysActive < 1 {
		daysActive = 1
	}
	monthlyValue := currentValue / daysActive * 30
	
	// Estimate remaining months based on engagement
	engagementScore := m.calculateEngagement(features)
	remainingMonths := 12.0 * engagementScore
	
	// Calculate churn adjustment
	churnRisk := m.calculateChurnRisk(features)
	churnAdjustment := 1.0 - (churnRisk * 0.5)
	
	ltv := (currentValue + monthlyValue*remainingMonths) * churnAdjustment
	
	return ltv, nil
}

func (m *LTVModel) calculateEngagement(f *LTVFeatures) float64 {
	var score float64
	
	if f.DaysSinceLastBet < 3 {
		score += 0.3
	} else if f.DaysSinceLastBet < 7 {
		score += 0.2
	}
	
	if f.SessionsPerWeek >= 5 {
		score += 0.2
	} else if f.SessionsPerWeek >= 3 {
		score += 0.15
	}
	
	if f.DepositFrequency >= 4 {
		score += 0.2
	} else if f.DepositFrequency >= 2 {
		score += 0.15
	}
	
	if f.GamesPlayed >= 10 {
		score += 0.15
	}
	
	if f.BonusConversionRate > 0.5 {
		score += 0.15
	}
	
	if score > 1 {
		score = 1
	}
	return score
}

func (m *LTVModel) calculateChurnRisk(f *LTVFeatures) float64 {
	var risk float64
	
	if f.DaysSinceLastBet > 30 {
		risk += 0.5
	} else if f.DaysSinceLastBet > 14 {
		risk += 0.3
	}
	
	if f.DaysSinceLastDeposit > 30 {
		risk += 0.2
	}
	
	if f.SessionsPerWeek < 1 && f.DaysSinceRegistration > 30 {
		risk += 0.2
	}
	
	if risk > 1 {
		risk = 1
	}
	return risk
}

// Close releases resources
func (m *LTVModel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.session != nil {
		m.session.Destroy()
	}
	return nil
}
