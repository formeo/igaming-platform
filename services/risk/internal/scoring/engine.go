// Package scoring implements real-time fraud scoring engine
package scoring

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ReasonCode for explaining scoring decisions
type ReasonCode string

const (
	ReasonHighVelocity       ReasonCode = "HIGH_VELOCITY"
	ReasonNewAccountLargeTx  ReasonCode = "NEW_ACCOUNT_LARGE_TX"
	ReasonIPCountryMismatch  ReasonCode = "IP_COUNTRY_MISMATCH"
	ReasonMultipleDevices    ReasonCode = "MULTIPLE_DEVICES"
	ReasonSuspiciousPattern  ReasonCode = "SUSPICIOUS_PATTERN"
	ReasonVPNDetected        ReasonCode = "VPN_DETECTED"
	ReasonKnownFraudster     ReasonCode = "KNOWN_FRAUDSTER"
	ReasonRapidDepositWithdraw ReasonCode = "RAPID_DEPOSIT_WITHDRAW"
	ReasonBonusAbuse         ReasonCode = "BONUS_ABUSE"
	ReasonMLHighRisk         ReasonCode = "ML_HIGH_RISK"
)

// Action recommended by scoring engine
type Action string

const (
	ActionApprove Action = "approve"
	ActionReview  Action = "review"
	ActionBlock   Action = "block"
)

// ScoreRequest input for scoring engine
type ScoreRequest struct {
	AccountID   uuid.UUID
	PlayerID    string
	Amount      int64
	TxType      string
	Currency    string
	GameID      string
	IP          string
	DeviceID    string
	Fingerprint string
	UserAgent   string
	SessionID   string
	Timestamp   time.Time
}

// ScoreResponse output from scoring engine
type ScoreResponse struct {
	Score          int           `json:"score"`            // 0-100
	Action         Action        `json:"action"`           // approve, review, block
	ReasonCodes    []ReasonCode  `json:"reason_codes"`     // triggered rules
	RuleScore      int           `json:"rule_score"`       // contribution from rules
	MLScore        float64       `json:"ml_score"`         // contribution from ML (0-1)
	ResponseTimeMs int64         `json:"response_time_ms"` // processing time
	Features       FeatureVector `json:"features"`         // extracted features
}

// FeatureVector contains all features for scoring
type FeatureVector struct {
	// Velocity features
	TxCount1Min     int     `json:"tx_count_1m"`
	TxCount5Min     int     `json:"tx_count_5m"`
	TxCount1Hour    int     `json:"tx_count_1h"`
	TxSum1Hour      int64   `json:"tx_sum_1h"`
	TxAvg1Hour      float64 `json:"tx_avg_1h"`
	
	// Device features
	UniqueDevices24h  int `json:"unique_devices_24h"`
	UniqueIPs24h      int `json:"unique_ips_24h"`
	IPCountryChanges  int `json:"ip_country_changes_7d"`
	DeviceAge         int `json:"device_age_days"`
	
	// Account features
	AccountAge        int     `json:"account_age_days"`
	TotalDeposits     int64   `json:"total_deposits"`
	TotalWithdrawals  int64   `json:"total_withdrawals"`
	NetDeposit        int64   `json:"net_deposit"`
	DepositCount      int     `json:"deposit_count"`
	WithdrawCount     int     `json:"withdraw_count"`
	
	// Behavioral features
	TimeSinceLastTx   int     `json:"time_since_last_tx_sec"`
	SessionDuration   int     `json:"session_duration_sec"`
	AvgBetSize        float64 `json:"avg_bet_size"`
	WinRate           float64 `json:"win_rate"`
	
	// Risk indicators
	IsVPN             bool    `json:"is_vpn"`
	IsProxy           bool    `json:"is_proxy"`
	IsTor             bool    `json:"is_tor"`
	DisposableEmail   bool    `json:"disposable_email"`
	
	// Bonus features
	BonusClaimCount   int     `json:"bonus_claim_count"`
	BonusWagerRate    float64 `json:"bonus_wager_completion_rate"`
	BonusOnlyPlayer   bool    `json:"bonus_only_player"`
}

// FeatureStore interface for feature retrieval
type FeatureStore interface {
	GetRealTimeFeatures(ctx context.Context, accountID uuid.UUID) (*RealTimeFeatures, error)
	GetBatchFeatures(ctx context.Context, accountID uuid.UUID) (*BatchFeatures, error)
	UpdateRealTimeFeatures(ctx context.Context, accountID uuid.UUID, tx *TransactionEvent) error
}

// RealTimeFeatures from Redis
type RealTimeFeatures struct {
	TxCount1Min      int     `redis:"tx_count_1m"`
	TxCount5Min      int     `redis:"tx_count_5m"`
	TxCount1Hour     int     `redis:"tx_count_1h"`
	TxSum1Hour       int64   `redis:"tx_sum_1h"`
	UniqueDevices24h int     `redis:"unique_devices_24h"`
	UniqueIPs24h     int     `redis:"unique_ips_24h"`
	LastTxTimestamp  int64   `redis:"last_tx_timestamp"`
	SessionStart     int64   `redis:"session_start"`
}

// BatchFeatures from ClickHouse (computed hourly)
type BatchFeatures struct {
	TotalDeposits      int64   `db:"total_deposits"`
	TotalWithdrawals   int64   `db:"total_withdrawals"`
	DepositCount       int     `db:"deposit_count"`
	WithdrawCount      int     `db:"withdraw_count"`
	TotalBets          int64   `db:"total_bets"`
	TotalWins          int64   `db:"total_wins"`
	BetCount           int     `db:"bet_count"`
	WinCount           int     `db:"win_count"`
	AvgBetSize         float64 `db:"avg_bet_size"`
	AccountCreatedAt   time.Time `db:"account_created_at"`
	BonusClaimCount    int     `db:"bonus_claim_count"`
	BonusWagerComplete float64 `db:"bonus_wager_complete_rate"`
}

// TransactionEvent for feature updates
type TransactionEvent struct {
	AccountID  uuid.UUID
	Amount     int64
	TxType     string
	IP         string
	DeviceID   string
	Timestamp  time.Time
}

// MLModel interface for ML predictions
type MLModel interface {
	Predict(ctx context.Context, features FeatureVector) (float64, error)
}

// IPIntelligence interface for IP analysis
type IPIntelligence interface {
	Analyze(ctx context.Context, ip string) (*IPInfo, error)
}

// IPInfo from IP intelligence service
type IPInfo struct {
	Country     string
	City        string
	ISP         string
	IsVPN       bool
	IsProxy     bool
	IsTor       bool
	RiskScore   int
}

// Blacklist interface for known bad actors
type Blacklist interface {
	Check(ctx context.Context, deviceID, fingerprint, ip string) (bool, error)
}

// ScoringEngine performs fraud scoring
type ScoringEngine struct {
	features    FeatureStore
	model       MLModel
	ipIntel     IPIntelligence
	blacklist   Blacklist
	logger      *slog.Logger
	
	// Configuration
	config      ScoringConfig
	
	// Rule weights
	ruleWeights map[ReasonCode]int
	
	mu sync.RWMutex
}

// ScoringConfig holds scoring configuration
type ScoringConfig struct {
	// Thresholds for final action
	BlockThreshold  int // >= this score = block
	ReviewThreshold int // >= this score = review
	
	// Rule thresholds
	MaxTxPerMinute      int
	MaxTxPerHour        int
	NewAccountDays      int
	LargeDepositAmount  int64
	MaxDevicesPerDay    int
	MaxIPsPerDay        int
	
	// ML weight in ensemble
	MLWeight   float64 // 0-1, e.g. 0.6
	RuleWeight float64 // 0-1, e.g. 0.4
}

// DefaultConfig returns sensible defaults
func DefaultConfig() ScoringConfig {
	return ScoringConfig{
		BlockThreshold:      80,
		ReviewThreshold:     50,
		MaxTxPerMinute:      10,
		MaxTxPerHour:        100,
		NewAccountDays:      7,
		LargeDepositAmount:  100000, // $1000 in cents
		MaxDevicesPerDay:    3,
		MaxIPsPerDay:        5,
		MLWeight:            0.6,
		RuleWeight:          0.4,
	}
}

// NewScoringEngine creates new scoring engine
func NewScoringEngine(
	features FeatureStore,
	model MLModel,
	ipIntel IPIntelligence,
	blacklist Blacklist,
	logger *slog.Logger,
	config ScoringConfig,
) *ScoringEngine {
	return &ScoringEngine{
		features:  features,
		model:     model,
		ipIntel:   ipIntel,
		blacklist: blacklist,
		logger:    logger,
		config:    config,
		ruleWeights: map[ReasonCode]int{
			ReasonHighVelocity:         20,
			ReasonNewAccountLargeTx:    30,
			ReasonIPCountryMismatch:    25,
			ReasonMultipleDevices:      15,
			ReasonSuspiciousPattern:    20,
			ReasonVPNDetected:          15,
			ReasonKnownFraudster:       50,
			ReasonRapidDepositWithdraw: 25,
			ReasonBonusAbuse:           20,
			ReasonMLHighRisk:           30,
		},
	}
}

// Score performs full scoring pipeline
func (e *ScoringEngine) Score(ctx context.Context, req ScoreRequest) (*ScoreResponse, error) {
	start := time.Now()
	
	// 1. Extract features
	features, err := e.extractFeatures(ctx, req)
	if err != nil {
		e.logger.Error("feature extraction failed", "error", err)
		// Continue with partial features
	}
	
	// 2. Apply rules (instant, explainable)
	ruleScore, reasons := e.applyRules(ctx, req, features)
	
	// 3. Get ML prediction
	var mlScore float64
	if e.model != nil {
		mlScore, err = e.model.Predict(ctx, features)
		if err != nil {
			e.logger.Warn("ML prediction failed", "error", err)
			mlScore = 0.5 // neutral if unavailable
		}
		
		// Add ML reason if high risk
		if mlScore > 0.7 {
			reasons = append(reasons, ReasonMLHighRisk)
		}
	}
	
	// 4. Ensemble: combine rule and ML scores
	finalScore := int(
		e.config.RuleWeight*float64(ruleScore) + 
		e.config.MLWeight*(mlScore*100),
	)
	
	// Cap at 100
	if finalScore > 100 {
		finalScore = 100
	}
	
	// 5. Determine action
	var action Action
	switch {
	case finalScore >= e.config.BlockThreshold:
		action = ActionBlock
	case finalScore >= e.config.ReviewThreshold:
		action = ActionReview
	default:
		action = ActionApprove
	}
	
	responseTime := time.Since(start).Milliseconds()
	
	return &ScoreResponse{
		Score:          finalScore,
		Action:         action,
		ReasonCodes:    reasons,
		RuleScore:      ruleScore,
		MLScore:        mlScore,
		ResponseTimeMs: responseTime,
		Features:       features,
	}, nil
}

// extractFeatures builds feature vector from various sources
func (e *ScoringEngine) extractFeatures(ctx context.Context, req ScoreRequest) (FeatureVector, error) {
	var features FeatureVector
	var wg sync.WaitGroup
	var mu sync.Mutex
	
	// Parallel feature extraction
	
	// Real-time features from Redis
	wg.Add(1)
	go func() {
		defer wg.Done()
		rt, err := e.features.GetRealTimeFeatures(ctx, req.AccountID)
		if err != nil {
			e.logger.Warn("real-time features unavailable", "error", err)
			return
		}
		mu.Lock()
		features.TxCount1Min = rt.TxCount1Min
		features.TxCount5Min = rt.TxCount5Min
		features.TxCount1Hour = rt.TxCount1Hour
		features.TxSum1Hour = rt.TxSum1Hour
		features.UniqueDevices24h = rt.UniqueDevices24h
		features.UniqueIPs24h = rt.UniqueIPs24h
		if rt.LastTxTimestamp > 0 {
			features.TimeSinceLastTx = int(time.Now().Unix() - rt.LastTxTimestamp)
		}
		if rt.SessionStart > 0 {
			features.SessionDuration = int(time.Now().Unix() - rt.SessionStart)
		}
		mu.Unlock()
	}()
	
	// Batch features from ClickHouse
	wg.Add(1)
	go func() {
		defer wg.Done()
		batch, err := e.features.GetBatchFeatures(ctx, req.AccountID)
		if err != nil {
			e.logger.Warn("batch features unavailable", "error", err)
			return
		}
		mu.Lock()
		features.TotalDeposits = batch.TotalDeposits
		features.TotalWithdrawals = batch.TotalWithdrawals
		features.NetDeposit = batch.TotalDeposits - batch.TotalWithdrawals
		features.DepositCount = batch.DepositCount
		features.WithdrawCount = batch.WithdrawCount
		features.AvgBetSize = batch.AvgBetSize
		features.AccountAge = int(time.Since(batch.AccountCreatedAt).Hours() / 24)
		features.BonusClaimCount = batch.BonusClaimCount
		features.BonusWagerRate = batch.BonusWagerComplete
		
		// Calculate win rate
		if batch.BetCount > 0 {
			features.WinRate = float64(batch.WinCount) / float64(batch.BetCount)
		}
		
		// Bonus only player detection
		if batch.BonusClaimCount > 3 && batch.TotalDeposits < 5000 { // less than $50 deposited
			features.BonusOnlyPlayer = true
		}
		mu.Unlock()
	}()
	
	// IP intelligence
	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.ipIntel == nil || req.IP == "" {
			return
		}
		ipInfo, err := e.ipIntel.Analyze(ctx, req.IP)
		if err != nil {
			e.logger.Warn("IP intel unavailable", "error", err)
			return
		}
		mu.Lock()
		features.IsVPN = ipInfo.IsVPN
		features.IsProxy = ipInfo.IsProxy
		features.IsTor = ipInfo.IsTor
		mu.Unlock()
	}()
	
	wg.Wait()
	
	// Calculate average
	if features.TxCount1Hour > 0 {
		features.TxAvg1Hour = float64(features.TxSum1Hour) / float64(features.TxCount1Hour)
	}
	
	return features, nil
}

// applyRules applies rule-based scoring
func (e *ScoringEngine) applyRules(ctx context.Context, req ScoreRequest, features FeatureVector) (int, []ReasonCode) {
	var totalScore int
	var reasons []ReasonCode
	
	// Rule 1: High velocity
	if features.TxCount1Min > e.config.MaxTxPerMinute {
		totalScore += e.ruleWeights[ReasonHighVelocity]
		reasons = append(reasons, ReasonHighVelocity)
	}
	
	// Rule 2: New account + large transaction
	if features.AccountAge < e.config.NewAccountDays && req.Amount > e.config.LargeDepositAmount {
		totalScore += e.ruleWeights[ReasonNewAccountLargeTx]
		reasons = append(reasons, ReasonNewAccountLargeTx)
	}
	
	// Rule 3: Multiple devices
	if features.UniqueDevices24h > e.config.MaxDevicesPerDay {
		totalScore += e.ruleWeights[ReasonMultipleDevices]
		reasons = append(reasons, ReasonMultipleDevices)
	}
	
	// Rule 4: Multiple IPs
	if features.UniqueIPs24h > e.config.MaxIPsPerDay {
		totalScore += e.ruleWeights[ReasonIPCountryMismatch]
		reasons = append(reasons, ReasonIPCountryMismatch)
	}
	
	// Rule 5: VPN/Proxy/Tor detection
	if features.IsVPN || features.IsProxy || features.IsTor {
		totalScore += e.ruleWeights[ReasonVPNDetected]
		reasons = append(reasons, ReasonVPNDetected)
	}
	
	// Rule 6: Rapid deposit-withdraw (money laundering signal)
	if features.TimeSinceLastTx < 300 && req.TxType == "withdraw" { // withdraw within 5 min of deposit
		if features.DepositCount > 0 && features.TotalWithdrawals > features.TotalDeposits*80/100 {
			totalScore += e.ruleWeights[ReasonRapidDepositWithdraw]
			reasons = append(reasons, ReasonRapidDepositWithdraw)
		}
	}
	
	// Rule 7: Bonus abuse
	if features.BonusOnlyPlayer {
		totalScore += e.ruleWeights[ReasonBonusAbuse]
		reasons = append(reasons, ReasonBonusAbuse)
	}
	
	// Rule 8: Blacklist check
	if e.blacklist != nil {
		isBlacklisted, _ := e.blacklist.Check(ctx, req.DeviceID, req.Fingerprint, req.IP)
		if isBlacklisted {
			totalScore += e.ruleWeights[ReasonKnownFraudster]
			reasons = append(reasons, ReasonKnownFraudster)
		}
	}
	
	// Cap rule score at 100
	if totalScore > 100 {
		totalScore = 100
	}
	
	return totalScore, reasons
}

// UpdateFeatures updates real-time features after transaction
func (e *ScoringEngine) UpdateFeatures(ctx context.Context, event *TransactionEvent) error {
	return e.features.UpdateRealTimeFeatures(ctx, event.AccountID, event)
}

// GetThresholds returns current thresholds (for monitoring)
func (e *ScoringEngine) GetThresholds() (block, review int) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.BlockThreshold, e.config.ReviewThreshold
}

// SetThresholds updates thresholds (for tuning)
func (e *ScoringEngine) SetThresholds(block, review int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.config.BlockThreshold = block
	e.config.ReviewThreshold = review
	e.logger.Info("thresholds updated", "block", block, "review", review)
}

// ScoreWithExplanation returns detailed explanation for debugging
func (e *ScoringEngine) ScoreWithExplanation(ctx context.Context, req ScoreRequest) (string, error) {
	resp, err := e.Score(ctx, req)
	if err != nil {
		return "", err
	}
	
	explanation := fmt.Sprintf(`
Fraud Score Analysis
====================
Final Score: %d/100
Action: %s
Response Time: %dms

Rule Contribution: %d
ML Contribution: %.2f (%.2f * 100)

Triggered Rules:
`, resp.Score, resp.Action, resp.ResponseTimeMs, resp.RuleScore, resp.MLScore, resp.MLScore)

	for _, reason := range resp.ReasonCodes {
		explanation += fmt.Sprintf("  - %s (+%d)\n", reason, e.ruleWeights[reason])
	}
	
	explanation += fmt.Sprintf(`
Key Features:
  - Transaction velocity (1h): %d txs, sum: %d
  - Unique devices (24h): %d
  - Account age: %d days
  - VPN/Proxy: %v
  - Bonus abuse signal: %v
`, resp.Features.TxCount1Hour, resp.Features.TxSum1Hour,
		resp.Features.UniqueDevices24h, resp.Features.AccountAge,
		resp.Features.IsVPN || resp.Features.IsProxy,
		resp.Features.BonusOnlyPlayer)
	
	return explanation, nil
}
