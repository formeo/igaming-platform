// Package domain contains core business entities for Wallet Service
package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Errors
var (
	ErrAccountNotFound      = errors.New("account not found")
	ErrAccountLocked        = errors.New("account is locked")
	ErrAccountSuspended     = errors.New("account is suspended")
	ErrInsufficientBalance  = errors.New("insufficient balance")
	ErrDuplicateTransaction = errors.New("duplicate transaction (idempotency)")
	ErrInvalidOperation     = errors.New("invalid operation")
	ErrTransactionFailed    = errors.New("transaction failed")
	ErrConcurrentUpdate     = errors.New("concurrent update detected")
)

// AccountStatus represents account state
type AccountStatus string

const (
	AccountStatusActive    AccountStatus = "active"
	AccountStatusSuspended AccountStatus = "suspended"
	AccountStatusClosed    AccountStatus = "closed"
)

// Account represents a player's wallet account
type Account struct {
	ID        uuid.UUID     `json:"id" db:"id"`
	PlayerID  string        `json:"player_id" db:"player_id"`
	Currency  string        `json:"currency" db:"currency"`
	Balance   int64         `json:"balance" db:"balance"` // in cents
	Bonus     int64         `json:"bonus" db:"bonus"`     // bonus balance in cents
	Status    AccountStatus `json:"status" db:"status"`
	Version   int64         `json:"version" db:"version"` // for optimistic locking
	CreatedAt time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt time.Time     `json:"updated_at" db:"updated_at"`
}

// NewAccount creates a new account for a player
func NewAccount(playerID, currency string) *Account {
	now := time.Now().UTC()
	return &Account{
		ID:        uuid.New(),
		PlayerID:  playerID,
		Currency:  currency,
		Balance:   0,
		Bonus:     0,
		Status:    AccountStatusActive,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// CanTransact returns true if account can perform transactions
func (a *Account) CanTransact() bool {
	return a.Status == AccountStatusActive
}

// TotalBalance returns sum of real and bonus balance
func (a *Account) TotalBalance() int64 {
	return a.Balance + a.Bonus
}

// AvailableForWithdraw returns balance available for withdrawal (excluding bonus)
func (a *Account) AvailableForWithdraw() int64 {
	return a.Balance
}

// TransactionType represents type of financial operation
type TransactionType string

const (
	TxTypeDeposit    TransactionType = "deposit"
	TxTypeWithdraw   TransactionType = "withdraw"
	TxTypeBet        TransactionType = "bet"
	TxTypeWin        TransactionType = "win"
	TxTypeRefund     TransactionType = "refund"
	TxTypeBonusGrant TransactionType = "bonus_grant"
	TxTypeBonusWager TransactionType = "bonus_wager"
	TxTypeAdjustment TransactionType = "adjustment"
)

// TransactionStatus represents transaction state
type TransactionStatus string

const (
	TxStatusPending   TransactionStatus = "pending"
	TxStatusCompleted TransactionStatus = "completed"
	TxStatusFailed    TransactionStatus = "failed"
	TxStatusReversed  TransactionStatus = "reversed"
)

// Transaction represents a financial operation
type Transaction struct {
	ID             uuid.UUID         `json:"id" db:"id"`
	AccountID      uuid.UUID         `json:"account_id" db:"account_id"`
	IdempotencyKey string            `json:"idempotency_key" db:"idempotency_key"`
	Type           TransactionType   `json:"type" db:"type"`
	Amount         int64             `json:"amount" db:"amount"` // always positive, in cents
	BalanceBefore  int64             `json:"balance_before" db:"balance_before"`
	BalanceAfter   int64             `json:"balance_after" db:"balance_after"`
	Status         TransactionStatus `json:"status" db:"status"`
	Reference      string            `json:"reference" db:"reference"` // external reference (game round, payment ID)
	GameID         *string           `json:"game_id,omitempty" db:"game_id"`
	RoundID        *string           `json:"round_id,omitempty" db:"round_id"`
	Metadata       JSON              `json:"metadata" db:"metadata"`
	RiskScore      *int              `json:"risk_score,omitempty" db:"risk_score"`
	CreatedAt      time.Time         `json:"created_at" db:"created_at"`
	CompletedAt    *time.Time        `json:"completed_at,omitempty" db:"completed_at"`
}

// JSON is a helper type for JSONB columns
type JSON map[string]interface{}

// NewTransaction creates a new transaction
func NewTransaction(
	accountID uuid.UUID,
	idempotencyKey string,
	txType TransactionType,
	amount int64,
	balanceBefore int64,
	reference string,
) *Transaction {
	balanceAfter := balanceBefore
	switch txType {
	case TxTypeDeposit, TxTypeWin, TxTypeRefund, TxTypeBonusGrant:
		balanceAfter = balanceBefore + amount
	case TxTypeWithdraw, TxTypeBet, TxTypeBonusWager:
		balanceAfter = balanceBefore - amount
	}

	now := time.Now().UTC()
	return &Transaction{
		ID:             uuid.New(),
		AccountID:      accountID,
		IdempotencyKey: idempotencyKey,
		Type:           txType,
		Amount:         amount,
		BalanceBefore:  balanceBefore,
		BalanceAfter:   balanceAfter,
		Status:         TxStatusPending,
		Reference:      reference,
		Metadata:       make(JSON),
		CreatedAt:      now,
	}
}

// Complete marks transaction as completed
func (t *Transaction) Complete() {
	now := time.Now().UTC()
	t.Status = TxStatusCompleted
	t.CompletedAt = &now
}

// Fail marks transaction as failed
func (t *Transaction) Fail() {
	t.Status = TxStatusFailed
}

// IsCredit returns true if transaction increases balance
func (t *Transaction) IsCredit() bool {
	return t.Type == TxTypeDeposit || t.Type == TxTypeWin || t.Type == TxTypeRefund
}

// IsDebit returns true if transaction decreases balance
func (t *Transaction) IsDebit() bool {
	return t.Type == TxTypeWithdraw || t.Type == TxTypeBet
}

// LedgerEntryType for double-entry bookkeeping
type LedgerEntryType string

const (
	LedgerDebit  LedgerEntryType = "debit"
	LedgerCredit LedgerEntryType = "credit"
)

// LedgerEntry represents a double-entry bookkeeping record
type LedgerEntry struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	TransactionID uuid.UUID       `json:"transaction_id" db:"transaction_id"`
	AccountID     uuid.UUID       `json:"account_id" db:"account_id"`
	EntryType     LedgerEntryType `json:"entry_type" db:"entry_type"`
	Amount        int64           `json:"amount" db:"amount"`
	BalanceAfter  int64           `json:"balance_after" db:"balance_after"`
	Description   string          `json:"description" db:"description"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
}

// NewLedgerEntry creates a ledger entry
func NewLedgerEntry(
	txID, accountID uuid.UUID,
	entryType LedgerEntryType,
	amount, balanceAfter int64,
	description string,
) *LedgerEntry {
	return &LedgerEntry{
		ID:            uuid.New(),
		TransactionID: txID,
		AccountID:     accountID,
		EntryType:     entryType,
		Amount:        amount,
		BalanceAfter:  balanceAfter,
		Description:   description,
		CreatedAt:     time.Now().UTC(),
	}
}

// BalanceSnapshot for audit purposes
type BalanceSnapshot struct {
	AccountID   uuid.UUID `json:"account_id" db:"account_id"`
	Balance     int64     `json:"balance" db:"balance"`
	Bonus       int64     `json:"bonus" db:"bonus"`
	SnapshotAt  time.Time `json:"snapshot_at" db:"snapshot_at"`
	TxCount     int64     `json:"tx_count" db:"tx_count"`
	TotalDebit  int64     `json:"total_debit" db:"total_debit"`
	TotalCredit int64     `json:"total_credit" db:"total_credit"`
}
