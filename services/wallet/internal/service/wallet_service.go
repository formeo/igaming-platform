// Package service implements wallet business logic
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Repository interfaces for dependency injection
type AccountRepository interface {
	Create(ctx context.Context, account *Account) error
	GetByID(ctx context.Context, id uuid.UUID) (*Account, error)
	GetByPlayerID(ctx context.Context, playerID string) (*Account, error)
	UpdateBalance(ctx context.Context, id uuid.UUID, balance, bonus, version int64) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status AccountStatus) error
}

type TransactionRepository interface {
	Create(ctx context.Context, tx *Transaction) error
	GetByID(ctx context.Context, id uuid.UUID) (*Transaction, error)
	GetByIdempotencyKey(ctx context.Context, accountID uuid.UUID, key string) (*Transaction, error)
	Update(ctx context.Context, tx *Transaction) error
	ListByAccount(ctx context.Context, accountID uuid.UUID, limit, offset int) ([]*Transaction, error)
}

type LedgerRepository interface {
	Create(ctx context.Context, entry *LedgerEntry) error
	GetByTransaction(ctx context.Context, txID uuid.UUID) ([]*LedgerEntry, error)
	GetAccountBalance(ctx context.Context, accountID uuid.UUID) (int64, error)
}

type EventPublisher interface {
	Publish(ctx context.Context, event Event) error
}

type RiskService interface {
	ScoreTransaction(ctx context.Context, req *RiskScoreRequest) (*RiskScoreResponse, error)
}

// Import domain types (in real project these would be in domain package)
type Account = struct {
	ID        uuid.UUID
	PlayerID  string
	Currency  string
	Balance   int64
	Bonus     int64
	Status    AccountStatus
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type AccountStatus string

const (
	AccountStatusActive    AccountStatus = "active"
	AccountStatusSuspended AccountStatus = "suspended"
)

type Transaction = struct {
	ID             uuid.UUID
	AccountID      uuid.UUID
	IdempotencyKey string
	Type           TransactionType
	Amount         int64
	BalanceBefore  int64
	BalanceAfter   int64
	Status         TransactionStatus
	Reference      string
	GameID         *string
	RoundID        *string
	RiskScore      *int
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

type TransactionType string

const (
	TxTypeDeposit  TransactionType = "deposit"
	TxTypeWithdraw TransactionType = "withdraw"
	TxTypeBet      TransactionType = "bet"
	TxTypeWin      TransactionType = "win"
	TxTypeRefund   TransactionType = "refund"
)

type TransactionStatus string

const (
	TxStatusPending   TransactionStatus = "pending"
	TxStatusCompleted TransactionStatus = "completed"
	TxStatusFailed    TransactionStatus = "failed"
)

type LedgerEntry = struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	EntryType     LedgerEntryType
	Amount        int64
	BalanceAfter  int64
	Description   string
	CreatedAt     time.Time
}

type LedgerEntryType string

const (
	LedgerDebit  LedgerEntryType = "debit"
	LedgerCredit LedgerEntryType = "credit"
)

type Event struct {
	Type      string
	Payload   interface{}
	Timestamp time.Time
}

type RiskScoreRequest struct {
	AccountID   uuid.UUID
	Amount      int64
	TxType      TransactionType
	GameID      string
	IP          string
	DeviceID    string
	Fingerprint string
}

type RiskScoreResponse struct {
	Score        int      // 0-100
	Action       string   // approve, review, block
	ReasonCodes  []string // triggered rules
	ResponseTime time.Duration
}

// WalletService handles all wallet operations
type WalletService struct {
	accounts     AccountRepository
	transactions TransactionRepository
	ledger       LedgerRepository
	events       EventPublisher
	risk         RiskService
	logger       *slog.Logger

	// Configuration
	riskThresholdBlock  int
	riskThresholdReview int
}

// Config for wallet service
type Config struct {
	RiskThresholdBlock  int
	RiskThresholdReview int
}

// NewWalletService creates new wallet service instance
func NewWalletService(
	accounts AccountRepository,
	transactions TransactionRepository,
	ledger LedgerRepository,
	events EventPublisher,
	risk RiskService,
	logger *slog.Logger,
	cfg Config,
) *WalletService {
	return &WalletService{
		accounts:            accounts,
		transactions:        transactions,
		ledger:              ledger,
		events:              events,
		risk:                risk,
		logger:              logger,
		riskThresholdBlock:  cfg.RiskThresholdBlock,
		riskThresholdReview: cfg.RiskThresholdReview,
	}
}

// CreateAccountRequest for creating new account
type CreateAccountRequest struct {
	PlayerID string
	Currency string
}

// CreateAccount creates a new wallet account
func (s *WalletService) CreateAccount(ctx context.Context, req CreateAccountRequest) (*Account, error) {
	// Check if account already exists
	existing, _ := s.accounts.GetByPlayerID(ctx, req.PlayerID)
	if existing != nil {
		return existing, nil // idempotent
	}

	account := &Account{
		ID:        uuid.New(),
		PlayerID:  req.PlayerID,
		Currency:  req.Currency,
		Balance:   0,
		Bonus:     0,
		Status:    AccountStatusActive,
		Version:   1,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.accounts.Create(ctx, account); err != nil {
		return nil, fmt.Errorf("failed to create account: %w", err)
	}

	s.events.Publish(ctx, Event{
		Type:      "account.created",
		Payload:   account,
		Timestamp: time.Now().UTC(),
	})

	return account, nil
}

// DepositRequest for deposit operation
type DepositRequest struct {
	AccountID      uuid.UUID
	Amount         int64 // in cents
	IdempotencyKey string
	PaymentMethod  string
	Reference      string
	IP             string
	DeviceID       string
}

// DepositResponse returned after deposit
type DepositResponse struct {
	Transaction *Transaction
	NewBalance  int64
	RiskScore   *int
}

// Deposit adds funds to account
func (s *WalletService) Deposit(ctx context.Context, req DepositRequest) (*DepositResponse, error) {
	// 1. Check idempotency
	existing, _ := s.transactions.GetByIdempotencyKey(ctx, req.AccountID, req.IdempotencyKey)
	if existing != nil {
		return &DepositResponse{
			Transaction: existing,
			NewBalance:  existing.BalanceAfter,
		}, nil
	}

	// 2. Get and validate account
	account, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	if account.Status != AccountStatusActive {
		return nil, fmt.Errorf("account is not active: %s", account.Status)
	}

	// 3. Risk check
	var riskScore *int
	if s.risk != nil {
		riskResp, err := s.risk.ScoreTransaction(ctx, &RiskScoreRequest{
			AccountID: req.AccountID,
			Amount:    req.Amount,
			TxType:    TxTypeDeposit,
			IP:        req.IP,
			DeviceID:  req.DeviceID,
		})
		if err != nil {
			s.logger.Warn("risk service unavailable", "error", err)
		} else {
			riskScore = &riskResp.Score
			if riskResp.Score >= s.riskThresholdBlock {
				return nil, fmt.Errorf("transaction blocked by risk: score=%d, reasons=%v",
					riskResp.Score, riskResp.ReasonCodes)
			}
		}
	}

	// 4. Create transaction
	tx := &Transaction{
		ID:             uuid.New(),
		AccountID:      req.AccountID,
		IdempotencyKey: req.IdempotencyKey,
		Type:           TxTypeDeposit,
		Amount:         req.Amount,
		BalanceBefore:  account.Balance,
		BalanceAfter:   account.Balance + req.Amount,
		Status:         TxStatusPending,
		Reference:      req.Reference,
		RiskScore:      riskScore,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.transactions.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// 5. Update balance with optimistic locking
	newBalance := account.Balance + req.Amount
	err = s.accounts.UpdateBalance(ctx, account.ID, newBalance, account.Bonus, account.Version)
	if err != nil {
		tx.Status = TxStatusFailed
		s.transactions.Update(ctx, tx)
		return nil, fmt.Errorf("failed to update balance: %w", err)
	}

	// 6. Create ledger entries (double-entry)
	s.createLedgerEntries(ctx, tx, "Deposit")

	// 7. Complete transaction
	now := time.Now().UTC()
	tx.Status = TxStatusCompleted
	tx.CompletedAt = &now
	s.transactions.Update(ctx, tx)

	// 8. Publish event
	s.events.Publish(ctx, Event{
		Type:      "transaction.completed",
		Payload:   tx,
		Timestamp: time.Now().UTC(),
	})

	return &DepositResponse{
		Transaction: tx,
		NewBalance:  newBalance,
		RiskScore:   riskScore,
	}, nil
}

// BetRequest for placing a bet
type BetRequest struct {
	AccountID      uuid.UUID
	Amount         int64
	IdempotencyKey string
	GameID         string
	RoundID        string
	IP             string
	DeviceID       string
}

// BetResponse returned after bet
type BetResponse struct {
	Transaction *Transaction
	NewBalance  int64
	RiskScore   *int
}

// Bet deducts funds for a bet
func (s *WalletService) Bet(ctx context.Context, req BetRequest) (*BetResponse, error) {
	// 1. Check idempotency
	existing, _ := s.transactions.GetByIdempotencyKey(ctx, req.AccountID, req.IdempotencyKey)
	if existing != nil {
		return &BetResponse{
			Transaction: existing,
			NewBalance:  existing.BalanceAfter,
		}, nil
	}

	// 2. Get and validate account
	account, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	if account.Status != AccountStatusActive {
		return nil, fmt.Errorf("account is not active")
	}

	// 3. Check sufficient balance (real + bonus)
	totalBalance := account.Balance + account.Bonus
	if totalBalance < req.Amount {
		return nil, fmt.Errorf("insufficient balance: available=%d, required=%d", totalBalance, req.Amount)
	}

	// 4. Risk check
	var riskScore *int
	if s.risk != nil {
		riskResp, err := s.risk.ScoreTransaction(ctx, &RiskScoreRequest{
			AccountID: req.AccountID,
			Amount:    req.Amount,
			TxType:    TxTypeBet,
			GameID:    req.GameID,
			IP:        req.IP,
			DeviceID:  req.DeviceID,
		})
		if err != nil {
			s.logger.Warn("risk service unavailable", "error", err)
		} else {
			riskScore = &riskResp.Score
			if riskResp.Score >= s.riskThresholdBlock {
				return nil, fmt.Errorf("bet blocked by risk: score=%d", riskResp.Score)
			}
		}
	}

	// 5. Calculate balance deduction (bonus first, then real)
	var newBalance, newBonus int64
	if account.Bonus >= req.Amount {
		// Use bonus only
		newBalance = account.Balance
		newBonus = account.Bonus - req.Amount
	} else {
		// Use bonus + real
		newBonus = 0
		newBalance = account.Balance - (req.Amount - account.Bonus)
	}

	// 6. Create transaction
	gameID := req.GameID
	roundID := req.RoundID
	tx := &Transaction{
		ID:             uuid.New(),
		AccountID:      req.AccountID,
		IdempotencyKey: req.IdempotencyKey,
		Type:           TxTypeBet,
		Amount:         req.Amount,
		BalanceBefore:  totalBalance,
		BalanceAfter:   newBalance + newBonus,
		Status:         TxStatusPending,
		Reference:      fmt.Sprintf("game:%s:round:%s", req.GameID, req.RoundID),
		GameID:         &gameID,
		RoundID:        &roundID,
		RiskScore:      riskScore,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.transactions.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// 7. Update balance
	err = s.accounts.UpdateBalance(ctx, account.ID, newBalance, newBonus, account.Version)
	if err != nil {
		tx.Status = TxStatusFailed
		s.transactions.Update(ctx, tx)
		return nil, fmt.Errorf("failed to update balance: %w", err)
	}

	// 8. Create ledger entries
	s.createLedgerEntries(ctx, tx, "Bet")

	// 9. Complete transaction
	now := time.Now().UTC()
	tx.Status = TxStatusCompleted
	tx.CompletedAt = &now
	s.transactions.Update(ctx, tx)

	// 10. Publish event
	s.events.Publish(ctx, Event{
		Type:      "transaction.completed",
		Payload:   tx,
		Timestamp: time.Now().UTC(),
	})

	return &BetResponse{
		Transaction: tx,
		NewBalance:  newBalance + newBonus,
		RiskScore:   riskScore,
	}, nil
}

// WinRequest for crediting win
type WinRequest struct {
	AccountID      uuid.UUID
	Amount         int64
	IdempotencyKey string
	GameID         string
	RoundID        string
	BetTxID        uuid.UUID // reference to original bet
}

// WinResponse returned after win
type WinResponse struct {
	Transaction *Transaction
	NewBalance  int64
}

// Win credits winnings to account
func (s *WalletService) Win(ctx context.Context, req WinRequest) (*WinResponse, error) {
	// 1. Check idempotency
	existing, _ := s.transactions.GetByIdempotencyKey(ctx, req.AccountID, req.IdempotencyKey)
	if existing != nil {
		return &WinResponse{
			Transaction: existing,
			NewBalance:  existing.BalanceAfter,
		}, nil
	}

	// 2. Get account
	account, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	// 3. Create transaction (wins go to real balance, not bonus)
	gameID := req.GameID
	roundID := req.RoundID
	newBalance := account.Balance + req.Amount

	tx := &Transaction{
		ID:             uuid.New(),
		AccountID:      req.AccountID,
		IdempotencyKey: req.IdempotencyKey,
		Type:           TxTypeWin,
		Amount:         req.Amount,
		BalanceBefore:  account.Balance + account.Bonus,
		BalanceAfter:   newBalance + account.Bonus,
		Status:         TxStatusPending,
		Reference:      fmt.Sprintf("win:game:%s:round:%s:bet:%s", req.GameID, req.RoundID, req.BetTxID),
		GameID:         &gameID,
		RoundID:        &roundID,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.transactions.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// 4. Update balance
	err = s.accounts.UpdateBalance(ctx, account.ID, newBalance, account.Bonus, account.Version)
	if err != nil {
		tx.Status = TxStatusFailed
		s.transactions.Update(ctx, tx)
		return nil, fmt.Errorf("failed to update balance: %w", err)
	}

	// 5. Create ledger entries
	s.createLedgerEntries(ctx, tx, "Win")

	// 6. Complete transaction
	now := time.Now().UTC()
	tx.Status = TxStatusCompleted
	tx.CompletedAt = &now
	s.transactions.Update(ctx, tx)

	// 7. Publish event
	s.events.Publish(ctx, Event{
		Type:      "transaction.completed",
		Payload:   tx,
		Timestamp: time.Now().UTC(),
	})

	return &WinResponse{
		Transaction: tx,
		NewBalance:  newBalance + account.Bonus,
	}, nil
}

// WithdrawRequest for withdrawal
type WithdrawRequest struct {
	AccountID      uuid.UUID
	Amount         int64
	IdempotencyKey string
	PayoutMethod   string
	IP             string
	DeviceID       string
}

// WithdrawResponse returned after withdrawal
type WithdrawResponse struct {
	Transaction *Transaction
	NewBalance  int64
	RiskScore   *int
}

// Withdraw removes funds from account
func (s *WalletService) Withdraw(ctx context.Context, req WithdrawRequest) (*WithdrawResponse, error) {
	// 1. Idempotency check
	existing, _ := s.transactions.GetByIdempotencyKey(ctx, req.AccountID, req.IdempotencyKey)
	if existing != nil {
		return &WithdrawResponse{
			Transaction: existing,
			NewBalance:  existing.BalanceAfter,
		}, nil
	}

	// 2. Get account
	account, err := s.accounts.GetByID(ctx, req.AccountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	if account.Status != AccountStatusActive {
		return nil, fmt.Errorf("account is not active")
	}

	// 3. Check balance (can only withdraw real balance, not bonus)
	if account.Balance < req.Amount {
		return nil, fmt.Errorf("insufficient balance for withdrawal: available=%d, required=%d",
			account.Balance, req.Amount)
	}

	// 4. Risk check (withdrawal fraud is critical)
	var riskScore *int
	if s.risk != nil {
		riskResp, err := s.risk.ScoreTransaction(ctx, &RiskScoreRequest{
			AccountID: req.AccountID,
			Amount:    req.Amount,
			TxType:    TxTypeWithdraw,
			IP:        req.IP,
			DeviceID:  req.DeviceID,
		})
		if err != nil {
			s.logger.Warn("risk service unavailable, blocking withdrawal", "error", err)
			return nil, fmt.Errorf("withdrawal pending: risk service unavailable")
		}
		riskScore = &riskResp.Score
		// Stricter threshold for withdrawals
		if riskResp.Score >= s.riskThresholdReview {
			return nil, fmt.Errorf("withdrawal requires review: score=%d, reasons=%v",
				riskResp.Score, riskResp.ReasonCodes)
		}
	}

	// 5. Create transaction
	newBalance := account.Balance - req.Amount
	tx := &Transaction{
		ID:             uuid.New(),
		AccountID:      req.AccountID,
		IdempotencyKey: req.IdempotencyKey,
		Type:           TxTypeWithdraw,
		Amount:         req.Amount,
		BalanceBefore:  account.Balance + account.Bonus,
		BalanceAfter:   newBalance + account.Bonus,
		Status:         TxStatusPending,
		Reference:      fmt.Sprintf("payout:%s", req.PayoutMethod),
		RiskScore:      riskScore,
		CreatedAt:      time.Now().UTC(),
	}

	if err := s.transactions.Create(ctx, tx); err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// 6. Update balance
	err = s.accounts.UpdateBalance(ctx, account.ID, newBalance, account.Bonus, account.Version)
	if err != nil {
		tx.Status = TxStatusFailed
		s.transactions.Update(ctx, tx)
		return nil, fmt.Errorf("failed to update balance: %w", err)
	}

	// 7. Ledger entries
	s.createLedgerEntries(ctx, tx, "Withdrawal")

	// 8. Complete
	now := time.Now().UTC()
	tx.Status = TxStatusCompleted
	tx.CompletedAt = &now
	s.transactions.Update(ctx, tx)

	// 9. Publish event
	s.events.Publish(ctx, Event{
		Type:      "withdrawal.completed",
		Payload:   tx,
		Timestamp: time.Now().UTC(),
	})

	return &WithdrawResponse{
		Transaction: tx,
		NewBalance:  newBalance + account.Bonus,
		RiskScore:   riskScore,
	}, nil
}

// GetBalance returns current balance
func (s *WalletService) GetBalance(ctx context.Context, accountID uuid.UUID) (*Account, error) {
	return s.accounts.GetByID(ctx, accountID)
}

// GetTransactionHistory returns transaction history
func (s *WalletService) GetTransactionHistory(ctx context.Context, accountID uuid.UUID, limit, offset int) ([]*Transaction, error) {
	return s.transactions.ListByAccount(ctx, accountID, limit, offset)
}

// createLedgerEntries creates double-entry bookkeeping records
func (s *WalletService) createLedgerEntries(ctx context.Context, tx *Transaction, description string) {
	// For each transaction, we create debit and credit entries
	// This is simplified - in production you'd have proper chart of accounts

	var entryType LedgerEntryType
	if tx.Type == TxTypeDeposit || tx.Type == TxTypeWin || tx.Type == TxTypeRefund {
		entryType = LedgerCredit
	} else {
		entryType = LedgerDebit
	}

	entry := &LedgerEntry{
		ID:            uuid.New(),
		TransactionID: tx.ID,
		AccountID:     tx.AccountID,
		EntryType:     entryType,
		Amount:        tx.Amount,
		BalanceAfter:  tx.BalanceAfter,
		Description:   description,
		CreatedAt:     time.Now().UTC(),
	}

	if err := s.ledger.Create(ctx, entry); err != nil {
		s.logger.Error("failed to create ledger entry", "error", err, "tx_id", tx.ID)
	}
}
