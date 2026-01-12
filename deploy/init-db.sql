-- iGaming Platform Database Schema
-- PostgreSQL 15+

-- Enable extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ============================================
-- WALLET SERVICE TABLES
-- ============================================

-- Accounts table
CREATE TABLE accounts (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    player_id VARCHAR(255) NOT NULL UNIQUE,
    currency VARCHAR(3) NOT NULL DEFAULT 'USD',
    balance BIGINT NOT NULL DEFAULT 0 CHECK (balance >= 0),
    bonus BIGINT NOT NULL DEFAULT 0 CHECK (bonus >= 0),
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_accounts_player_id ON accounts(player_id);
CREATE INDEX idx_accounts_status ON accounts(status);

-- Transactions table
CREATE TABLE transactions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    idempotency_key VARCHAR(255) NOT NULL,
    type VARCHAR(20) NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    balance_before BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending',
    reference VARCHAR(500),
    game_id VARCHAR(255),
    round_id VARCHAR(255),
    risk_score INTEGER,
    metadata JSONB DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    
    UNIQUE(account_id, idempotency_key)
);

CREATE INDEX idx_transactions_account_id ON transactions(account_id);
CREATE INDEX idx_transactions_created_at ON transactions(created_at DESC);
CREATE INDEX idx_transactions_type ON transactions(type);
CREATE INDEX idx_transactions_status ON transactions(status);
CREATE INDEX idx_transactions_game_round ON transactions(game_id, round_id);

-- Ledger entries (double-entry bookkeeping)
CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    transaction_id UUID NOT NULL REFERENCES transactions(id),
    account_id UUID NOT NULL REFERENCES accounts(id),
    entry_type VARCHAR(10) NOT NULL, -- 'debit' or 'credit'
    amount BIGINT NOT NULL,
    balance_after BIGINT NOT NULL,
    description VARCHAR(500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ledger_transaction_id ON ledger_entries(transaction_id);
CREATE INDEX idx_ledger_account_id ON ledger_entries(account_id);

-- ============================================
-- BONUS SERVICE TABLES
-- ============================================

-- Player bonuses
CREATE TABLE player_bonuses (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id UUID NOT NULL REFERENCES accounts(id),
    rule_id VARCHAR(100) NOT NULL,
    type VARCHAR(30) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    
    -- Amounts (in cents)
    bonus_amount BIGINT NOT NULL,
    wagering_required BIGINT NOT NULL,
    wagering_progress BIGINT NOT NULL DEFAULT 0,
    
    -- For free spins
    free_spins_total INTEGER DEFAULT 0,
    free_spins_used INTEGER DEFAULT 0,
    
    -- Timestamps
    awarded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ,
    
    -- Trigger info
    trigger_tx_id UUID REFERENCES transactions(id),
    promo_code VARCHAR(50)
);

CREATE INDEX idx_bonuses_account_id ON player_bonuses(account_id);
CREATE INDEX idx_bonuses_status ON player_bonuses(status);
CREATE INDEX idx_bonuses_expires_at ON player_bonuses(expires_at);
CREATE INDEX idx_bonuses_rule_account ON player_bonuses(rule_id, account_id);

-- Bonus transactions (links bonuses to wagers)
CREATE TABLE bonus_transactions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    bonus_id UUID NOT NULL REFERENCES player_bonuses(id),
    transaction_id UUID NOT NULL REFERENCES transactions(id),
    wager_contribution BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_bonus_tx_bonus_id ON bonus_transactions(bonus_id);

-- ============================================
-- RISK SERVICE TABLES
-- ============================================

-- Risk scores log
CREATE TABLE risk_scores (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id UUID NOT NULL,
    transaction_id UUID,
    score INTEGER NOT NULL,
    action VARCHAR(20) NOT NULL,
    reason_codes TEXT[], -- array of reason codes
    rule_score INTEGER,
    ml_score REAL,
    response_time_ms INTEGER,
    features JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_risk_scores_account_id ON risk_scores(account_id);
CREATE INDEX idx_risk_scores_created_at ON risk_scores(created_at DESC);
CREATE INDEX idx_risk_scores_score ON risk_scores(score);

-- LTV predictions
CREATE TABLE ltv_predictions (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    account_id UUID NOT NULL,
    predicted_ltv REAL NOT NULL,
    segment VARCHAR(20) NOT NULL,
    churn_risk REAL NOT NULL,
    predicted_days INTEGER,
    confidence REAL,
    next_best_action VARCHAR(50),
    predicted_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ltv_account_id ON ltv_predictions(account_id);
CREATE INDEX idx_ltv_segment ON ltv_predictions(segment);
CREATE INDEX idx_ltv_predicted_at ON ltv_predictions(predicted_at DESC);

-- Blacklists
CREATE TABLE blacklists (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    type VARCHAR(20) NOT NULL, -- 'device', 'ip', 'fingerprint', 'email'
    value VARCHAR(500) NOT NULL,
    reason VARCHAR(500),
    created_by VARCHAR(100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ,
    
    UNIQUE(type, value)
);

CREATE INDEX idx_blacklist_type_value ON blacklists(type, value);

-- ============================================
-- AUDIT & EVENTS
-- ============================================

-- Event outbox (for transactional outbox pattern)
CREATE TABLE event_outbox (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    event_type VARCHAR(100) NOT NULL,
    aggregate_type VARCHAR(50) NOT NULL,
    aggregate_id UUID NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    published_at TIMESTAMPTZ,
    retry_count INTEGER DEFAULT 0
);

CREATE INDEX idx_outbox_unpublished ON event_outbox(created_at) WHERE published_at IS NULL;

-- Audit log
CREATE TABLE audit_log (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    entity_type VARCHAR(50) NOT NULL,
    entity_id UUID NOT NULL,
    action VARCHAR(50) NOT NULL,
    old_values JSONB,
    new_values JSONB,
    performed_by VARCHAR(100),
    ip_address INET,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_audit_entity ON audit_log(entity_type, entity_id);
CREATE INDEX idx_audit_created_at ON audit_log(created_at DESC);

-- ============================================
-- FUNCTIONS & TRIGGERS
-- ============================================

-- Update timestamp trigger
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER accounts_updated_at
    BEFORE UPDATE ON accounts
    FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- Optimistic locking trigger for accounts
CREATE OR REPLACE FUNCTION check_account_version()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.version != NEW.version - 1 THEN
        RAISE EXCEPTION 'Concurrent update detected on account %', OLD.id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER accounts_version_check
    BEFORE UPDATE ON accounts
    FOR EACH ROW EXECUTE FUNCTION check_account_version();

-- ============================================
-- SAMPLE DATA FOR TESTING
-- ============================================

-- Insert test account
INSERT INTO accounts (id, player_id, currency, balance, bonus, status)
VALUES 
    ('00000000-0000-0000-0000-000000000001', 'test-player-1', 'USD', 10000, 500, 'active'),
    ('00000000-0000-0000-0000-000000000002', 'test-player-2', 'EUR', 50000, 0, 'active'),
    ('00000000-0000-0000-0000-000000000003', 'test-vip-player', 'USD', 500000, 10000, 'active');

-- Grant permissions
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO igaming;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO igaming;
