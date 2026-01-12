// Package prediction implements player lifetime value prediction
package prediction

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
)

// LTVSegment represents player value segment
type LTVSegment string

const (
	SegmentVIP      LTVSegment = "vip"       // Top 1%, LTV > $10,000
	SegmentHigh     LTVSegment = "high"      // Top 10%, LTV > $1,000
	SegmentMedium   LTVSegment = "medium"    // Top 50%, LTV > $100
	SegmentLow      LTVSegment = "low"       // Bottom 50%
	SegmentChurning LTVSegment = "churning"  // High churn risk
)

// LTVPrediction represents predicted lifetime value
type LTVPrediction struct {
	AccountID      uuid.UUID    `json:"account_id"`
	PredictedLTV   float64      `json:"predicted_ltv"`      // in dollars
	Segment        LTVSegment   `json:"segment"`
	ChurnRisk      float64      `json:"churn_risk"`         // 0-1
	DaysSurvival   int          `json:"predicted_days"`     // predicted active days
	Confidence     float64      `json:"confidence"`         // 0-1
	NextBestAction string       `json:"next_best_action"`   // recommended action
	PredictedAt    time.Time    `json:"predicted_at"`
}

// PlayerFeatures for LTV prediction
type PlayerFeatures struct {
	// Activity metrics
	DaysSinceRegistration   int     `json:"days_since_registration"`
	DaysSinceLastDeposit    int     `json:"days_since_last_deposit"`
	DaysSinceLastBet        int     `json:"days_since_last_bet"`
	TotalActiveDays         int     `json:"total_active_days"`
	SessionsPerWeek         float64 `json:"sessions_per_week"`
	AvgSessionDuration      float64 `json:"avg_session_duration_min"`
	
	// Financial metrics
	TotalDeposits           float64 `json:"total_deposits"`
	TotalWithdrawals        float64 `json:"total_withdrawals"`
	NetRevenue              float64 `json:"net_revenue"` // deposits - withdrawals - bonuses
	AvgDepositAmount        float64 `json:"avg_deposit_amount"`
	DepositFrequency        float64 `json:"deposits_per_month"`
	LargestDeposit          float64 `json:"largest_deposit"`
	
	// Gaming behavior
	TotalBets               float64 `json:"total_bets"`
	TotalWins               float64 `json:"total_wins"`
	BetCount                int     `json:"bet_count"`
	WinRate                 float64 `json:"win_rate"`
	AvgBetSize              float64 `json:"avg_bet_size"`
	FavoriteGameCategory    string  `json:"favorite_game_category"`
	GamesPlayed             int     `json:"games_played"`
	
	// Bonus behavior
	BonusesClaimed          int     `json:"bonuses_claimed"`
	BonusWageringCompleted  int     `json:"bonus_wagering_completed"`
	BonusConversionRate     float64 `json:"bonus_conversion_rate"`
	
	// Engagement
	PushNotificationEnabled bool    `json:"push_enabled"`
	EmailOptIn              bool    `json:"email_opt_in"`
	HasVIPManager           bool    `json:"has_vip_manager"`
	SupportTickets          int     `json:"support_tickets"`
	
	// Demographics
	Country                 string  `json:"country"`
	PaymentMethod           string  `json:"primary_payment_method"`
}

// PlayerDataSource interface for retrieving player data
type PlayerDataSource interface {
	GetPlayerFeatures(ctx context.Context, accountID uuid.UUID) (*PlayerFeatures, error)
	GetHistoricalLTV(ctx context.Context, accountID uuid.UUID) (float64, error)
}

// LTVPredictor predicts player lifetime value
type LTVPredictor struct {
	dataSource PlayerDataSource
	logger     *slog.Logger
	
	// Segment thresholds (in dollars)
	vipThreshold    float64
	highThreshold   float64
	mediumThreshold float64
	
	// Churn risk threshold (days inactive)
	churnInactiveDays int
}

// NewLTVPredictor creates new LTV predictor
func NewLTVPredictor(dataSource PlayerDataSource, logger *slog.Logger) *LTVPredictor {
	return &LTVPredictor{
		dataSource:        dataSource,
		logger:            logger,
		vipThreshold:      10000,
		highThreshold:     1000,
		mediumThreshold:   100,
		churnInactiveDays: 14,
	}
}

// Predict calculates LTV prediction for a player
func (p *LTVPredictor) Predict(ctx context.Context, accountID uuid.UUID) (*LTVPrediction, error) {
	features, err := p.dataSource.GetPlayerFeatures(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player features: %w", err)
	}
	
	// Calculate base LTV using simplified model
	// In production, this would use a trained ML model (XGBoost, etc.)
	ltv := p.calculateLTV(features)
	
	// Calculate churn risk
	churnRisk := p.calculateChurnRisk(features)
	
	// Adjust LTV for churn risk
	adjustedLTV := ltv * (1 - churnRisk*0.5)
	
	// Determine segment
	segment := p.determineSegment(adjustedLTV, churnRisk)
	
	// Predict survival days
	survivalDays := p.predictSurvival(features, churnRisk)
	
	// Determine next best action
	action := p.getNextBestAction(segment, features, churnRisk)
	
	// Calculate confidence based on data quality
	confidence := p.calculateConfidence(features)
	
	return &LTVPrediction{
		AccountID:      accountID,
		PredictedLTV:   adjustedLTV,
		Segment:        segment,
		ChurnRisk:      churnRisk,
		DaysSurvival:   survivalDays,
		Confidence:     confidence,
		NextBestAction: action,
		PredictedAt:    time.Now().UTC(),
	}, nil
}

// calculateLTV estimates lifetime value
// This is a simplified model - real implementation would use trained ML
func (p *LTVPredictor) calculateLTV(f *PlayerFeatures) float64 {
	// Current realized value
	currentValue := f.NetRevenue
	
	// If player is new, project based on early behavior
	if f.DaysSinceRegistration < 30 {
		// Project monthly value based on current activity
		monthlyRate := f.NetRevenue / float64(max(f.DaysSinceRegistration, 1)) * 30
		// Assume 12 month average lifespan for new players
		projectedValue := monthlyRate * 12
		return projectedValue
	}
	
	// For established players, use historical patterns
	monthlyValue := f.NetRevenue / float64(f.DaysSinceRegistration) * 30
	
	// Estimate remaining months based on engagement
	engagementScore := p.calculateEngagementScore(f)
	remainingMonths := 12.0 * engagementScore // up to 12 more months
	
	futureValue := monthlyValue * remainingMonths
	
	return currentValue + futureValue
}

// calculateEngagementScore returns 0-1 score based on engagement signals
func (p *LTVPredictor) calculateEngagementScore(f *PlayerFeatures) float64 {
	var score float64
	
	// Recent activity (most important)
	if f.DaysSinceLastBet < 3 {
		score += 0.3
	} else if f.DaysSinceLastBet < 7 {
		score += 0.2
	} else if f.DaysSinceLastBet < 14 {
		score += 0.1
	}
	
	// Session frequency
	if f.SessionsPerWeek >= 5 {
		score += 0.2
	} else if f.SessionsPerWeek >= 3 {
		score += 0.15
	} else if f.SessionsPerWeek >= 1 {
		score += 0.1
	}
	
	// Deposit frequency
	if f.DepositFrequency >= 4 {
		score += 0.2
	} else if f.DepositFrequency >= 2 {
		score += 0.15
	} else if f.DepositFrequency >= 1 {
		score += 0.1
	}
	
	// Communication opt-in
	if f.PushNotificationEnabled {
		score += 0.1
	}
	if f.EmailOptIn {
		score += 0.1
	}
	
	// VIP status
	if f.HasVIPManager {
		score += 0.1
	}
	
	return math.Min(score, 1.0)
}

// calculateChurnRisk returns 0-1 churn probability
func (p *LTVPredictor) calculateChurnRisk(f *PlayerFeatures) float64 {
	var risk float64
	
	// Inactivity (strongest signal)
	if f.DaysSinceLastBet > 30 {
		risk += 0.5
	} else if f.DaysSinceLastBet > 14 {
		risk += 0.3
	} else if f.DaysSinceLastBet > 7 {
		risk += 0.15
	}
	
	// Declining activity pattern
	// (In production, compare recent vs historical activity)
	if f.SessionsPerWeek < 1 && f.DaysSinceRegistration > 30 {
		risk += 0.2
	}
	
	// No deposits recently
	if f.DaysSinceLastDeposit > 30 {
		risk += 0.2
	}
	
	// Support issues (frustration signal)
	if f.SupportTickets > 3 {
		risk += 0.1
	}
	
	// Negative balance trajectory
	if f.TotalWithdrawals > f.TotalDeposits {
		risk += 0.1 // Likely bonus abuser or lucky player leaving
	}
	
	return math.Min(risk, 1.0)
}

// determineSegment assigns player to value segment
func (p *LTVPredictor) determineSegment(ltv float64, churnRisk float64) LTVSegment {
	// High churn risk overrides value segment
	if churnRisk > 0.7 {
		return SegmentChurning
	}
	
	switch {
	case ltv >= p.vipThreshold:
		return SegmentVIP
	case ltv >= p.highThreshold:
		return SegmentHigh
	case ltv >= p.mediumThreshold:
		return SegmentMedium
	default:
		return SegmentLow
	}
}

// predictSurvival estimates remaining active days
func (p *LTVPredictor) predictSurvival(f *PlayerFeatures, churnRisk float64) int {
	// Base survival from engagement
	baseDays := 90 // 3 months average
	
	// Adjust for engagement
	engagementMultiplier := 1.0 + p.calculateEngagementScore(f)
	
	// Adjust for churn risk
	churnMultiplier := 1.0 - churnRisk
	
	survivalDays := float64(baseDays) * engagementMultiplier * churnMultiplier
	
	return int(math.Max(survivalDays, 0))
}

// getNextBestAction recommends action based on player state
func (p *LTVPredictor) getNextBestAction(segment LTVSegment, f *PlayerFeatures, churnRisk float64) string {
	switch segment {
	case SegmentChurning:
		if f.NetRevenue > 0 {
			return "SEND_WINBACK_BONUS"
		}
		return "SEND_ENGAGEMENT_EMAIL"
		
	case SegmentVIP:
		if f.DaysSinceLastDeposit > 7 {
			return "VIP_MANAGER_CALL"
		}
		return "EXCLUSIVE_EVENT_INVITE"
		
	case SegmentHigh:
		if !f.HasVIPManager {
			return "ASSIGN_VIP_MANAGER"
		}
		if churnRisk > 0.3 {
			return "RETENTION_BONUS"
		}
		return "LOYALTY_REWARD"
		
	case SegmentMedium:
		if f.BonusesClaimed < 3 {
			return "SUGGEST_BONUS"
		}
		if f.GamesPlayed < 5 {
			return "RECOMMEND_NEW_GAMES"
		}
		return "STANDARD_PROMOTION"
		
	case SegmentLow:
		if f.DaysSinceRegistration < 7 {
			return "ONBOARDING_GUIDE"
		}
		if f.BonusConversionRate > 0.8 {
			return "NO_ACTION" // Likely bonus abuser
		}
		return "SMALL_DEPOSIT_BONUS"
	}
	
	return "NO_ACTION"
}

// calculateConfidence estimates prediction reliability
func (p *LTVPredictor) calculateConfidence(f *PlayerFeatures) float64 {
	var confidence float64
	
	// More data = more confidence
	if f.DaysSinceRegistration > 90 {
		confidence += 0.3
	} else if f.DaysSinceRegistration > 30 {
		confidence += 0.2
	} else {
		confidence += 0.1
	}
	
	// Transaction count
	if f.BetCount > 100 {
		confidence += 0.3
	} else if f.BetCount > 20 {
		confidence += 0.2
	} else {
		confidence += 0.1
	}
	
	// Deposit history
	if f.DepositFrequency > 2 {
		confidence += 0.2
	} else if f.DepositFrequency > 0 {
		confidence += 0.1
	}
	
	// Recent activity (stale data = lower confidence)
	if f.DaysSinceLastBet < 7 {
		confidence += 0.2
	} else if f.DaysSinceLastBet < 30 {
		confidence += 0.1
	}
	
	return math.Min(confidence, 1.0)
}

// BatchPredict runs LTV prediction for multiple players
func (p *LTVPredictor) BatchPredict(ctx context.Context, accountIDs []uuid.UUID) ([]*LTVPrediction, error) {
	predictions := make([]*LTVPrediction, 0, len(accountIDs))
	
	for _, id := range accountIDs {
		pred, err := p.Predict(ctx, id)
		if err != nil {
			p.logger.Warn("failed to predict LTV", "account_id", id, "error", err)
			continue
		}
		predictions = append(predictions, pred)
	}
	
	return predictions, nil
}

// SegmentPlayers groups players by LTV segment
func (p *LTVPredictor) SegmentPlayers(ctx context.Context, accountIDs []uuid.UUID) (map[LTVSegment][]uuid.UUID, error) {
	segments := make(map[LTVSegment][]uuid.UUID)
	
	predictions, err := p.BatchPredict(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	
	for _, pred := range predictions {
		segments[pred.Segment] = append(segments[pred.Segment], pred.AccountID)
	}
	
	return segments, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
