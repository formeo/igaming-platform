// Package bonus implements configurable bonus rules engine
package bonus

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// BonusType represents type of bonus
type BonusType string

const (
	BonusTypeDepositMatch BonusType = "deposit_match"
	BonusTypeFreeSpins    BonusType = "free_spins"
	BonusTypeCashback     BonusType = "cashback"
	BonusTypeNoDeposit    BonusType = "no_deposit"
	BonusTypeFreebet      BonusType = "freebet"
)

// BonusStatus represents bonus state
type BonusStatus string

const (
	BonusStatusPending   BonusStatus = "pending"
	BonusStatusActive    BonusStatus = "active"
	BonusStatusCompleted BonusStatus = "completed"
	BonusStatusExpired   BonusStatus = "expired"
	BonusStatusCancelled BonusStatus = "cancelled"
	BonusStatusForfeited BonusStatus = "forfeited"
)

// BonusRule defines a configurable bonus rule
type BonusRule struct {
	ID                 string    `yaml:"id"`
	Name               string    `yaml:"name"`
	Type               BonusType `yaml:"type"`
	Description        string    `yaml:"description"`
	
	// Matching criteria
	MatchPercent       int       `yaml:"match_percent,omitempty"`       // for deposit_match
	MaxBonus           int64     `yaml:"max_bonus"`                     // max bonus amount in cents
	MinDeposit         int64     `yaml:"min_deposit,omitempty"`         // minimum qualifying deposit
	FixedAmount        int64     `yaml:"fixed_amount,omitempty"`        // for no_deposit/freebet
	FreeSpinsCount     int       `yaml:"free_spins_count,omitempty"`    // for free_spins
	CashbackPercent    int       `yaml:"cashback_percent,omitempty"`    // for cashback
	
	// Wagering requirements
	WageringMultiplier int       `yaml:"wagering_multiplier"`           // e.g., 35x
	MaxBetPercent      int       `yaml:"max_bet_percent"`               // max bet as % of bonus
	MaxBetAbsolute     int64     `yaml:"max_bet_absolute,omitempty"`    // max bet absolute
	
	// Game restrictions
	EligibleGames      []string  `yaml:"eligible_games,omitempty"`      // game categories
	ExcludedGames      []string  `yaml:"excluded_games,omitempty"`
	GameWeights        map[string]int `yaml:"game_weights,omitempty"`   // contribution % per game
	
	// Timing
	ExpiryDays         int       `yaml:"expiry_days"`
	Schedule           *Schedule `yaml:"schedule,omitempty"`
	
	// Player eligibility
	Conditions         *Conditions `yaml:"conditions,omitempty"`
	
	// Flags
	Active             bool      `yaml:"active"`
	OneTime            bool      `yaml:"one_time"`                      // can only be claimed once
	PromoCode          string    `yaml:"promo_code,omitempty"`
}

// Schedule for time-limited bonuses
type Schedule struct {
	DaysOfWeek   []string `yaml:"days_of_week,omitempty"` // monday, tuesday, etc.
	StartTime    string   `yaml:"start_time,omitempty"`   // HH:MM
	EndTime      string   `yaml:"end_time,omitempty"`
	StartDate    string   `yaml:"start_date,omitempty"`   // YYYY-MM-DD
	EndDate      string   `yaml:"end_date,omitempty"`
}

// Conditions for player eligibility
type Conditions struct {
	MinDepositsLifetime  int    `yaml:"min_deposits_lifetime,omitempty"`
	MinAccountAgeDays    int    `yaml:"min_account_age_days,omitempty"`
	MaxAccountAgeDays    int    `yaml:"max_account_age_days,omitempty"`
	RequiredSegment      string `yaml:"required_segment,omitempty"`     // vip, high, etc.
	ExcludedSegments     []string `yaml:"excluded_segments,omitempty"`
	Countries            []string `yaml:"countries,omitempty"`          // allowed countries
	ExcludedCountries    []string `yaml:"excluded_countries,omitempty"`
}

// BonusConfig holds all bonus rules
type BonusConfig struct {
	Rules []BonusRule `yaml:"bonus_rules"`
}

// PlayerBonus represents an awarded bonus
type PlayerBonus struct {
	ID               uuid.UUID   `json:"id" db:"id"`
	AccountID        uuid.UUID   `json:"account_id" db:"account_id"`
	RuleID           string      `json:"rule_id" db:"rule_id"`
	Type             BonusType   `json:"type" db:"type"`
	Status           BonusStatus `json:"status" db:"status"`
	
	// Amounts
	BonusAmount      int64       `json:"bonus_amount" db:"bonus_amount"`         // awarded amount
	WageringRequired int64       `json:"wagering_required" db:"wagering_required"` // total to wager
	WageringProgress int64       `json:"wagering_progress" db:"wagering_progress"` // wagered so far
	
	// For free spins
	FreeSpinsTotal   int         `json:"free_spins_total" db:"free_spins_total"`
	FreeSpinsUsed    int         `json:"free_spins_used" db:"free_spins_used"`
	
	// Timestamps
	AwardedAt        time.Time   `json:"awarded_at" db:"awarded_at"`
	ExpiresAt        time.Time   `json:"expires_at" db:"expires_at"`
	CompletedAt      *time.Time  `json:"completed_at,omitempty" db:"completed_at"`
	
	// Trigger
	TriggerTxID      *uuid.UUID  `json:"trigger_tx_id,omitempty" db:"trigger_tx_id"` // deposit that triggered
	PromoCode        *string     `json:"promo_code,omitempty" db:"promo_code"`
}

// BonusRepository for persistence
type BonusRepository interface {
	Create(ctx context.Context, bonus *PlayerBonus) error
	GetByID(ctx context.Context, id uuid.UUID) (*PlayerBonus, error)
	GetActiveByAccount(ctx context.Context, accountID uuid.UUID) ([]*PlayerBonus, error)
	Update(ctx context.Context, bonus *PlayerBonus) error
	CountByRuleAndAccount(ctx context.Context, ruleID string, accountID uuid.UUID) (int, error)
	GetExpiredBonuses(ctx context.Context) ([]*PlayerBonus, error)
}

// RiskChecker interface for abuse detection
type RiskChecker interface {
	CheckBonusAbuse(ctx context.Context, accountID uuid.UUID) (bool, error)
}

// PlayerDataProvider for eligibility checks
type PlayerDataProvider interface {
	GetPlayerInfo(ctx context.Context, accountID uuid.UUID) (*PlayerInfo, error)
}

// PlayerInfo for eligibility
type PlayerInfo struct {
	AccountID        uuid.UUID
	AccountAgeDays   int
	TotalDeposits    int
	Segment          string
	Country          string
	TotalBonusClaims int
}

// BonusEngine manages bonus rules and awards
type BonusEngine struct {
	rules      []BonusRule
	repo       BonusRepository
	risk       RiskChecker
	playerData PlayerDataProvider
	logger     *slog.Logger
	
	// Index for fast lookup
	rulesByID map[string]*BonusRule
}

// NewBonusEngine creates new bonus engine from config file
func NewBonusEngine(
	configPath string,
	repo BonusRepository,
	risk RiskChecker,
	playerData PlayerDataProvider,
	logger *slog.Logger,
) (*BonusEngine, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}
	
	var config BonusConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}
	
	engine := &BonusEngine{
		rules:      config.Rules,
		repo:       repo,
		risk:       risk,
		playerData: playerData,
		logger:     logger,
		rulesByID:  make(map[string]*BonusRule),
	}
	
	for i := range engine.rules {
		engine.rulesByID[engine.rules[i].ID] = &engine.rules[i]
	}
	
	logger.Info("bonus engine initialized", "rules_count", len(engine.rules))
	
	return engine, nil
}

// GetEligibleBonuses returns bonuses player can claim
func (e *BonusEngine) GetEligibleBonuses(ctx context.Context, accountID uuid.UUID) ([]BonusRule, error) {
	playerInfo, err := e.playerData.GetPlayerInfo(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player info: %w", err)
	}
	
	var eligible []BonusRule
	
	for _, rule := range e.rules {
		if !rule.Active {
			continue
		}
		
		// Check if already claimed (for one-time bonuses)
		if rule.OneTime {
			count, _ := e.repo.CountByRuleAndAccount(ctx, rule.ID, accountID)
			if count > 0 {
				continue
			}
		}
		
		// Check conditions
		if !e.checkConditions(&rule, playerInfo) {
			continue
		}
		
		// Check schedule
		if !e.checkSchedule(&rule) {
			continue
		}
		
		eligible = append(eligible, rule)
	}
	
	return eligible, nil
}

// AwardBonus awards a bonus to player
func (e *BonusEngine) AwardBonus(ctx context.Context, req AwardBonusRequest) (*PlayerBonus, error) {
	// 1. Find rule
	rule, ok := e.rulesByID[req.RuleID]
	if !ok {
		return nil, fmt.Errorf("bonus rule not found: %s", req.RuleID)
	}
	
	if !rule.Active {
		return nil, fmt.Errorf("bonus rule is not active")
	}
	
	// 2. Get player info
	playerInfo, err := e.playerData.GetPlayerInfo(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player info: %w", err)
	}
	
	// 3. Check eligibility
	if !e.checkConditions(rule, playerInfo) {
		return nil, fmt.Errorf("player not eligible for this bonus")
	}
	
	// 4. Check for abuse
	if e.risk != nil {
		isAbuser, err := e.risk.CheckBonusAbuse(ctx, req.AccountID)
		if err != nil {
			e.logger.Warn("risk check failed", "error", err)
		} else if isAbuser {
			return nil, fmt.Errorf("bonus blocked: suspected abuse")
		}
	}
	
	// 5. Check one-time limit
	if rule.OneTime {
		count, _ := e.repo.CountByRuleAndAccount(ctx, rule.ID, req.AccountID)
		if count > 0 {
			return nil, fmt.Errorf("bonus already claimed")
		}
	}
	
	// 6. Calculate bonus amount
	bonusAmount := e.calculateBonusAmount(rule, req.DepositAmount)
	if bonusAmount == 0 {
		return nil, fmt.Errorf("calculated bonus amount is zero")
	}
	
	// 7. Calculate wagering requirement
	wageringRequired := bonusAmount * int64(rule.WageringMultiplier)
	
	// 8. Create bonus
	now := time.Now().UTC()
	bonus := &PlayerBonus{
		ID:               uuid.New(),
		AccountID:        req.AccountID,
		RuleID:           rule.ID,
		Type:             rule.Type,
		Status:           BonusStatusActive,
		BonusAmount:      bonusAmount,
		WageringRequired: wageringRequired,
		WageringProgress: 0,
		FreeSpinsTotal:   rule.FreeSpinsCount,
		FreeSpinsUsed:    0,
		AwardedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, rule.ExpiryDays),
		TriggerTxID:      req.TriggerTxID,
		PromoCode:        req.PromoCode,
	}
	
	if err := e.repo.Create(ctx, bonus); err != nil {
		return nil, fmt.Errorf("failed to save bonus: %w", err)
	}
	
	e.logger.Info("bonus awarded",
		"bonus_id", bonus.ID,
		"account_id", req.AccountID,
		"rule_id", rule.ID,
		"amount", bonusAmount,
		"wagering", wageringRequired,
	)
	
	return bonus, nil
}

// AwardBonusRequest for awarding bonus
type AwardBonusRequest struct {
	AccountID     uuid.UUID
	RuleID        string
	DepositAmount int64      // for deposit match
	TriggerTxID   *uuid.UUID // triggering transaction
	PromoCode     *string
}

// ProcessWager updates wagering progress after a bet
func (e *BonusEngine) ProcessWager(ctx context.Context, req ProcessWagerRequest) error {
	// Get active bonuses for account
	bonuses, err := e.repo.GetActiveByAccount(ctx, req.AccountID)
	if err != nil {
		return err
	}
	
	for _, bonus := range bonuses {
		rule := e.rulesByID[bonus.RuleID]
		if rule == nil {
			continue
		}
		
		// Check if game contributes to wagering
		contribution := e.calculateWagerContribution(rule, req.GameCategory, req.BetAmount)
		if contribution == 0 {
			continue
		}
		
		// Update progress
		bonus.WageringProgress += contribution
		
		// Check if wagering completed
		if bonus.WageringProgress >= bonus.WageringRequired {
			bonus.Status = BonusStatusCompleted
			now := time.Now().UTC()
			bonus.CompletedAt = &now
			
			e.logger.Info("bonus wagering completed",
				"bonus_id", bonus.ID,
				"account_id", req.AccountID,
			)
		}
		
		if err := e.repo.Update(ctx, bonus); err != nil {
			e.logger.Error("failed to update bonus", "error", err)
		}
	}
	
	return nil
}

// ProcessWagerRequest for processing wagers
type ProcessWagerRequest struct {
	AccountID    uuid.UUID
	BetAmount    int64
	GameID       string
	GameCategory string
}

// CheckMaxBet verifies bet doesn't exceed bonus limits
func (e *BonusEngine) CheckMaxBet(ctx context.Context, accountID uuid.UUID, betAmount int64) error {
	bonuses, err := e.repo.GetActiveByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	
	for _, bonus := range bonuses {
		rule := e.rulesByID[bonus.RuleID]
		if rule == nil {
			continue
		}
		
		// Check percentage limit
		if rule.MaxBetPercent > 0 {
			maxBet := bonus.BonusAmount * int64(rule.MaxBetPercent) / 100
			if betAmount > maxBet {
				return fmt.Errorf("bet exceeds max bet limit: %d > %d (max %d%% of bonus)",
					betAmount, maxBet, rule.MaxBetPercent)
			}
		}
		
		// Check absolute limit
		if rule.MaxBetAbsolute > 0 && betAmount > rule.MaxBetAbsolute {
			return fmt.Errorf("bet exceeds absolute max bet: %d > %d",
				betAmount, rule.MaxBetAbsolute)
		}
	}
	
	return nil
}

// ExpireOldBonuses marks expired bonuses
func (e *BonusEngine) ExpireOldBonuses(ctx context.Context) (int, error) {
	expired, err := e.repo.GetExpiredBonuses(ctx)
	if err != nil {
		return 0, err
	}
	
	count := 0
	for _, bonus := range expired {
		bonus.Status = BonusStatusExpired
		if err := e.repo.Update(ctx, bonus); err != nil {
			e.logger.Error("failed to expire bonus", "bonus_id", bonus.ID, "error", err)
			continue
		}
		count++
	}
	
	if count > 0 {
		e.logger.Info("expired bonuses", "count", count)
	}
	
	return count, nil
}

// ForfeitBonus forfeits bonus (e.g., on early withdrawal)
func (e *BonusEngine) ForfeitBonus(ctx context.Context, accountID uuid.UUID) error {
	bonuses, err := e.repo.GetActiveByAccount(ctx, accountID)
	if err != nil {
		return err
	}
	
	for _, bonus := range bonuses {
		bonus.Status = BonusStatusForfeited
		if err := e.repo.Update(ctx, bonus); err != nil {
			return err
		}
		e.logger.Info("bonus forfeited", "bonus_id", bonus.ID, "account_id", accountID)
	}
	
	return nil
}

// Helper methods

func (e *BonusEngine) calculateBonusAmount(rule *BonusRule, depositAmount int64) int64 {
	switch rule.Type {
	case BonusTypeDepositMatch:
		bonus := depositAmount * int64(rule.MatchPercent) / 100
		if bonus > rule.MaxBonus {
			return rule.MaxBonus
		}
		return bonus
		
	case BonusTypeNoDeposit, BonusTypeFreebet:
		return rule.FixedAmount
		
	case BonusTypeCashback:
		// Cashback is calculated on losses, handled separately
		return 0
		
	default:
		return rule.FixedAmount
	}
}

func (e *BonusEngine) calculateWagerContribution(rule *BonusRule, gameCategory string, betAmount int64) int64 {
	// Check excluded games
	for _, excluded := range rule.ExcludedGames {
		if excluded == gameCategory {
			return 0
		}
	}
	
	// Check eligible games (if specified)
	if len(rule.EligibleGames) > 0 {
		eligible := false
		for _, g := range rule.EligibleGames {
			if g == gameCategory {
				eligible = true
				break
			}
		}
		if !eligible {
			return 0
		}
	}
	
	// Apply game weight
	weight := 100 // default 100%
	if w, ok := rule.GameWeights[gameCategory]; ok {
		weight = w
	}
	
	return betAmount * int64(weight) / 100
}

func (e *BonusEngine) checkConditions(rule *BonusRule, player *PlayerInfo) bool {
	c := rule.Conditions
	if c == nil {
		return true
	}
	
	if c.MinDepositsLifetime > 0 && player.TotalDeposits < c.MinDepositsLifetime {
		return false
	}
	
	if c.MinAccountAgeDays > 0 && player.AccountAgeDays < c.MinAccountAgeDays {
		return false
	}
	
	if c.MaxAccountAgeDays > 0 && player.AccountAgeDays > c.MaxAccountAgeDays {
		return false
	}
	
	if c.RequiredSegment != "" && player.Segment != c.RequiredSegment {
		return false
	}
	
	for _, excluded := range c.ExcludedSegments {
		if player.Segment == excluded {
			return false
		}
	}
	
	if len(c.Countries) > 0 {
		allowed := false
		for _, country := range c.Countries {
			if country == player.Country {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	
	for _, excluded := range c.ExcludedCountries {
		if player.Country == excluded {
			return false
		}
	}
	
	return true
}

func (e *BonusEngine) checkSchedule(rule *BonusRule) bool {
	s := rule.Schedule
	if s == nil {
		return true
	}
	
	now := time.Now()
	
	// Check date range
	if s.StartDate != "" {
		start, _ := time.Parse("2006-01-02", s.StartDate)
		if now.Before(start) {
			return false
		}
	}
	if s.EndDate != "" {
		end, _ := time.Parse("2006-01-02", s.EndDate)
		if now.After(end) {
			return false
		}
	}
	
	// Check day of week
	if len(s.DaysOfWeek) > 0 {
		today := now.Weekday().String()
		found := false
		for _, day := range s.DaysOfWeek {
			if day == today {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	
	return true
}

// GetRule returns a bonus rule by ID
func (e *BonusEngine) GetRule(ruleID string) *BonusRule {
	return e.rulesByID[ruleID]
}

// GetAllRules returns all active rules
func (e *BonusEngine) GetAllRules() []BonusRule {
	var active []BonusRule
	for _, rule := range e.rules {
		if rule.Active {
			active = append(active, rule)
		}
	}
	return active
}
