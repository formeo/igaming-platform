// Package repository implements data access layer for Wallet Service
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

// Domain types (imported from domain package in real project)
type Account struct {
	ID        uuid.UUID     `db:"id"`
	PlayerID  string        `db:"player_id"`
	Currency  string        `db:"currency"`
	Balance   int64         `db:"balance"`
	Bonus     int64         `db:"bonus"`
	Status    string        `db:"status"`
	Version   int64         `db:"version"`
	CreatedAt time.Time     `db:"created_at"`
	UpdatedAt time.Time     `db:"updated_at"`
}

type Transaction struct {
	ID             uuid.UUID  `db:"id"`
	AccountID      uuid.UUID  `db:"account_id"`
	IdempotencyKey string     `db:"idempotency_key"`
	Type           string     `db:"type"`
	Amount         int64      `db:"amount"`
	BalanceBefore  int64      `db:"balance_before"`
	BalanceAfter   int64      `db:"balance_after"`
	Status         string     `db:"status"`
	Reference      string     `db:"reference"`
	GameID         *string    `db:"game_id"`
	RoundID        *string    `db:"round_id"`
	RiskScore      *int       `db:"risk_score"`
	Metadata       []byte     `db:"metadata"`
	CreatedAt      time.Time  `db:"created_at"`
	CompletedAt    *time.Time `db:"completed_at"`
}

type LedgerEntry struct {
	ID            uuid.UUID `db:"id"`
	TransactionID uuid.UUID `db:"transaction_id"`
	AccountID     uuid.UUID `db:"account_id"`
	EntryType     string    `db:"entry_type"`
	Amount        int64     `db:"amount"`
	BalanceAfter  int64     `db:"balance_after"`
	Description   string    `db:"description"`
	CreatedAt     time.Time `db:"created_at"`
}

// Errors
var (
	ErrNotFound          = errors.New("not found")
	ErrDuplicateKey      = errors.New("duplicate key")
	ErrConcurrentUpdate  = errors.New("concurrent update")
)

// PostgresAccountRepository implements AccountRepository
type PostgresAccountRepository struct {
	db *sqlx.DB
}

// NewPostgresAccountRepository creates new repository
func NewPostgresAccountRepository(db *sqlx.DB) *PostgresAccountRepository {
	return &PostgresAccountRepository{db: db}
}

// Create inserts new account
func (r *PostgresAccountRepository) Create(ctx context.Context, account *Account) error {
	query := `
		INSERT INTO accounts (id, player_id, currency, balance, bonus, status, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err := r.db.ExecContext(ctx, query,
		account.ID,
		account.PlayerID,
		account.Currency,
		account.Balance,
		account.Bonus,
		account.Status,
		account.Version,
		account.CreatedAt,
		account.UpdatedAt,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicateKey
		}
		return fmt.Errorf("failed to create account: %w", err)
	}
	return nil
}

// GetByID retrieves account by ID
func (r *PostgresAccountRepository) GetByID(ctx context.Context, id uuid.UUID) (*Account, error) {
	var account Account
	query := `SELECT * FROM accounts WHERE id = $1`
	err := r.db.GetContext(ctx, &account, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get account: %w", err)
	}
	return &account, nil
}

// GetByPlayerID retrieves account by player ID
func (r *PostgresAccountRepository) GetByPlayerID(ctx context.Context, playerID string) (*Account, error) {
	var account Account
	query := `SELECT * FROM accounts WHERE player_id = $1`
	err := r.db.GetContext(ctx, &account, query, playerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get account: %w", err)
	}
	return &account, nil
}

// UpdateBalance updates account balance with optimistic locking
func (r *PostgresAccountRepository) UpdateBalance(ctx context.Context, id uuid.UUID, balance, bonus, expectedVersion int64) error {
	query := `
		UPDATE accounts 
		SET balance = $1, bonus = $2, version = version + 1, updated_at = NOW()
		WHERE id = $3 AND version = $4
	`
	result, err := r.db.ExecContext(ctx, query, balance, bonus, id, expectedVersion)
	if err != nil {
		return fmt.Errorf("failed to update balance: %w", err)
	}
	
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrConcurrentUpdate
	}
	return nil
}

// UpdateStatus updates account status
func (r *PostgresAccountRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status string) error {
	query := `UPDATE accounts SET status = $1, updated_at = NOW() WHERE id = $2`
	_, err := r.db.ExecContext(ctx, query, status, id)
	return err
}

// Lock acquires a row-level lock for transaction
func (r *PostgresAccountRepository) Lock(ctx context.Context, id uuid.UUID) (*Account, error) {
	var account Account
	query := `SELECT * FROM accounts WHERE id = $1 FOR UPDATE`
	err := r.db.GetContext(ctx, &account, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &account, nil
}

// PostgresTransactionRepository implements TransactionRepository
type PostgresTransactionRepository struct {
	db *sqlx.DB
}

// NewPostgresTransactionRepository creates new repository
func NewPostgresTransactionRepository(db *sqlx.DB) *PostgresTransactionRepository {
	return &PostgresTransactionRepository{db: db}
}

// Create inserts new transaction
func (r *PostgresTransactionRepository) Create(ctx context.Context, tx *Transaction) error {
	query := `
		INSERT INTO transactions 
		(id, account_id, idempotency_key, type, amount, balance_before, balance_after, 
		 status, reference, game_id, round_id, risk_score, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`
	_, err := r.db.ExecContext(ctx, query,
		tx.ID,
		tx.AccountID,
		tx.IdempotencyKey,
		tx.Type,
		tx.Amount,
		tx.BalanceBefore,
		tx.BalanceAfter,
		tx.Status,
		tx.Reference,
		tx.GameID,
		tx.RoundID,
		tx.RiskScore,
		tx.Metadata,
		tx.CreatedAt,
	)
	if err != nil {
		if isDuplicateKeyError(err) {
			return ErrDuplicateKey
		}
		return fmt.Errorf("failed to create transaction: %w", err)
	}
	return nil
}

// GetByID retrieves transaction by ID
func (r *PostgresTransactionRepository) GetByID(ctx context.Context, id uuid.UUID) (*Transaction, error) {
	var tx Transaction
	query := `SELECT * FROM transactions WHERE id = $1`
	err := r.db.GetContext(ctx, &tx, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &tx, nil
}

// GetByIdempotencyKey retrieves transaction by idempotency key
func (r *PostgresTransactionRepository) GetByIdempotencyKey(ctx context.Context, accountID uuid.UUID, key string) (*Transaction, error) {
	var tx Transaction
	query := `SELECT * FROM transactions WHERE account_id = $1 AND idempotency_key = $2`
	err := r.db.GetContext(ctx, &tx, query, accountID, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &tx, nil
}

// Update updates transaction
func (r *PostgresTransactionRepository) Update(ctx context.Context, tx *Transaction) error {
	query := `
		UPDATE transactions 
		SET status = $1, completed_at = $2
		WHERE id = $3
	`
	_, err := r.db.ExecContext(ctx, query, tx.Status, tx.CompletedAt, tx.ID)
	return err
}

// ListByAccount retrieves transactions for an account
func (r *PostgresTransactionRepository) ListByAccount(ctx context.Context, accountID uuid.UUID, limit, offset int) ([]*Transaction, error) {
	var txs []*Transaction
	query := `
		SELECT * FROM transactions 
		WHERE account_id = $1 
		ORDER BY created_at DESC 
		LIMIT $2 OFFSET $3
	`
	err := r.db.SelectContext(ctx, &txs, query, accountID, limit, offset)
	if err != nil {
		return nil, err
	}
	return txs, nil
}

// ListByGameRound retrieves transactions for a game round
func (r *PostgresTransactionRepository) ListByGameRound(ctx context.Context, gameID, roundID string) ([]*Transaction, error) {
	var txs []*Transaction
	query := `
		SELECT * FROM transactions 
		WHERE game_id = $1 AND round_id = $2 
		ORDER BY created_at
	`
	err := r.db.SelectContext(ctx, &txs, query, gameID, roundID)
	if err != nil {
		return nil, err
	}
	return txs, nil
}

// GetDailyStats retrieves daily transaction statistics
func (r *PostgresTransactionRepository) GetDailyStats(ctx context.Context, accountID uuid.UUID, date time.Time) (*DailyStats, error) {
	var stats DailyStats
	query := `
		SELECT 
			COUNT(*) as tx_count,
			COALESCE(SUM(CASE WHEN type = 'deposit' THEN amount ELSE 0 END), 0) as total_deposits,
			COALESCE(SUM(CASE WHEN type = 'withdraw' THEN amount ELSE 0 END), 0) as total_withdrawals,
			COALESCE(SUM(CASE WHEN type = 'bet' THEN amount ELSE 0 END), 0) as total_bets,
			COALESCE(SUM(CASE WHEN type = 'win' THEN amount ELSE 0 END), 0) as total_wins
		FROM transactions
		WHERE account_id = $1 
		AND created_at >= $2 
		AND created_at < $3
		AND status = 'completed'
	`
	startOfDay := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.AddDate(0, 0, 1)
	
	err := r.db.GetContext(ctx, &stats, query, accountID, startOfDay, endOfDay)
	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// DailyStats holds daily statistics
type DailyStats struct {
	TxCount          int   `db:"tx_count"`
	TotalDeposits    int64 `db:"total_deposits"`
	TotalWithdrawals int64 `db:"total_withdrawals"`
	TotalBets        int64 `db:"total_bets"`
	TotalWins        int64 `db:"total_wins"`
}

// PostgresLedgerRepository implements LedgerRepository
type PostgresLedgerRepository struct {
	db *sqlx.DB
}

// NewPostgresLedgerRepository creates new repository
func NewPostgresLedgerRepository(db *sqlx.DB) *PostgresLedgerRepository {
	return &PostgresLedgerRepository{db: db}
}

// Create inserts ledger entry
func (r *PostgresLedgerRepository) Create(ctx context.Context, entry *LedgerEntry) error {
	query := `
		INSERT INTO ledger_entries 
		(id, transaction_id, account_id, entry_type, amount, balance_after, description, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := r.db.ExecContext(ctx, query,
		entry.ID,
		entry.TransactionID,
		entry.AccountID,
		entry.EntryType,
		entry.Amount,
		entry.BalanceAfter,
		entry.Description,
		entry.CreatedAt,
	)
	return err
}

// GetByTransaction retrieves ledger entries for a transaction
func (r *PostgresLedgerRepository) GetByTransaction(ctx context.Context, txID uuid.UUID) ([]*LedgerEntry, error) {
	var entries []*LedgerEntry
	query := `SELECT * FROM ledger_entries WHERE transaction_id = $1 ORDER BY created_at`
	err := r.db.SelectContext(ctx, &entries, query, txID)
	return entries, err
}

// GetAccountBalance calculates balance from ledger
func (r *PostgresLedgerRepository) GetAccountBalance(ctx context.Context, accountID uuid.UUID) (int64, error) {
	var balance int64
	query := `
		SELECT COALESCE(
			SUM(CASE WHEN entry_type = 'credit' THEN amount ELSE -amount END), 
			0
		) FROM ledger_entries WHERE account_id = $1
	`
	err := r.db.GetContext(ctx, &balance, query, accountID)
	return balance, err
}

// VerifyBalance checks if account balance matches ledger
func (r *PostgresLedgerRepository) VerifyBalance(ctx context.Context, accountID uuid.UUID) (bool, error) {
	query := `
		SELECT 
			a.balance,
			COALESCE(SUM(CASE WHEN l.entry_type = 'credit' THEN l.amount ELSE -l.amount END), 0) as ledger_balance
		FROM accounts a
		LEFT JOIN ledger_entries l ON l.account_id = a.id
		WHERE a.id = $1
		GROUP BY a.id, a.balance
	`
	var result struct {
		Balance       int64 `db:"balance"`
		LedgerBalance int64 `db:"ledger_balance"`
	}
	err := r.db.GetContext(ctx, &result, query, accountID)
	if err != nil {
		return false, err
	}
	return result.Balance == result.LedgerBalance, nil
}

// UnitOfWork for transactional operations
type UnitOfWork struct {
	db *sqlx.DB
	tx *sqlx.Tx
}

// NewUnitOfWork creates new unit of work
func NewUnitOfWork(db *sqlx.DB) *UnitOfWork {
	return &UnitOfWork{db: db}
}

// Begin starts a transaction
func (u *UnitOfWork) Begin(ctx context.Context) error {
	tx, err := u.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	u.tx = tx
	return nil
}

// Commit commits the transaction
func (u *UnitOfWork) Commit() error {
	if u.tx == nil {
		return errors.New("no transaction to commit")
	}
	return u.tx.Commit()
}

// Rollback rolls back the transaction
func (u *UnitOfWork) Rollback() error {
	if u.tx == nil {
		return nil
	}
	return u.tx.Rollback()
}

// Accounts returns account repository with transaction
func (u *UnitOfWork) Accounts() *PostgresAccountRepository {
	if u.tx != nil {
		return &PostgresAccountRepository{db: u.tx.Unsafe()}
	}
	return &PostgresAccountRepository{db: u.db}
}

// Transactions returns transaction repository
func (u *UnitOfWork) Transactions() *PostgresTransactionRepository {
	if u.tx != nil {
		return &PostgresTransactionRepository{db: u.tx.Unsafe()}
	}
	return &PostgresTransactionRepository{db: u.db}
}

// Helper to detect duplicate key errors
func isDuplicateKeyError(err error) bool {
	// PostgreSQL error code 23505 = unique_violation
	return err != nil && (
		// pq driver
		err.Error() == "pq: duplicate key value violates unique constraint" ||
		// pgx driver
		err.Error() == "ERROR: duplicate key value violates unique constraint")
}
