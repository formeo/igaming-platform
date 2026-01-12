# iGaming Platform Core

A production-ready microservices platform for iGaming industry, demonstrating high-load transaction processing, configurable bonus rules, and real-time fraud detection with ML.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     iGaming Platform Core                        │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────────────┐ │
│  │   Wallet     │   │    Bonus     │   │    Risk & Predict    │ │
│  │   Service    │◄──►│   Engine     │◄──►│      Service         │ │
│  └──────┬───────┘   └──────┬───────┘   └──────────┬───────────┘ │
│         │                  │                      │              │
│         └──────────────────┼──────────────────────┘              │
│                            │                                     │
│                    ┌───────▼───────┐                            │
│                    │  Event Stream │                            │
│                    │  (RabbitMQ)   │                            │
│                    └───────────────┘                            │
└─────────────────────────────────────────────────────────────────┘
```

## Services

### 🏦 Wallet Service
Core financial operations with ACID guarantees:
- Deposit / Withdraw / Bet / Win operations
- Double-entry bookkeeping ledger
- Idempotent transactions
- Optimistic locking for concurrent access
- Event sourcing for audit trail

### 🎁 Bonus Engine  
Configurable promotion system:
- YAML-based bonus rules DSL
- Multiple bonus types (deposit match, free spins, cashback)
- Wagering requirements tracking
- Automatic expiration handling
- Integration with Risk Service for abuse prevention

### 🛡️ Risk & Prediction Service
Real-time fraud detection and player analytics:
- **Fraud Scoring**: Rule-based + ML ensemble (< 50ms latency)
- **Bonus Abuse Detection**: Pattern matching for bonus hunters
- **LTV Prediction**: Player lifetime value forecasting
- Feature store (Redis + ClickHouse)

## Tech Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.22+ |
| API | gRPC + Connect (HTTP) |
| Database | PostgreSQL 15 |
| Cache & Features | Redis 7 |
| Message Queue | RabbitMQ |
| Analytics DB | ClickHouse |
| ML Inference | ONNX Runtime |
| Monitoring | Prometheus + Grafana |
| Containerization | Docker + Docker Compose |

## Quick Start

```bash
# Clone repository
git clone https://github.com/yourusername/igaming-platform.git
cd igaming-platform

# Start infrastructure
make infra-up

# Run migrations
make migrate

# Start all services
make run

# Run tests
make test
```

## API Examples

### Create Account
```bash
grpcurl -plaintext -d '{
  "player_id": "player-123",
  "currency": "USD"
}' localhost:8080 wallet.v1.WalletService/CreateAccount
```

### Deposit
```bash
grpcurl -plaintext -d '{
  "account_id": "acc-uuid",
  "amount": "100.00",
  "idempotency_key": "deposit-001",
  "payment_method": "card"
}' localhost:8080 wallet.v1.WalletService/Deposit
```

### Place Bet (with fraud check)
```bash
grpcurl -plaintext -d '{
  "account_id": "acc-uuid", 
  "amount": "10.00",
  "game_id": "slots-777",
  "round_id": "round-xyz",
  "idempotency_key": "bet-001"
}' localhost:8080 wallet.v1.WalletService/Bet
```

## Project Structure

```
igaming-platform/
├── services/
│   ├── wallet/          # Financial transactions
│   ├── bonus/           # Promotion management  
│   └── risk/            # Fraud detection & ML
├── pkg/                 # Shared libraries
│   ├── events/          # Event definitions
│   ├── money/           # Decimal money handling
│   └── idempotency/     # Idempotency helpers
├── proto/               # Protobuf definitions
├── deploy/              # Docker & K8s configs
└── docs/                # Documentation
```

## Key Features for iGaming

### 1. Financial Integrity
- Double-entry bookkeeping ensures balance consistency
- Idempotent operations prevent duplicate transactions
- Full audit trail with event sourcing

### 2. Regulatory Compliance
- Configurable jurisdiction rules
- Responsible gambling limits
- Complete transaction history

### 3. Scalability
- Horizontal scaling with stateless services
- Event-driven architecture
- Efficient caching strategies

### 4. Fraud Prevention
- Real-time transaction scoring
- Multi-factor risk assessment
- Automatic blocking & alerting

## Configuration

### Bonus Rules Example (YAML DSL)
```yaml
bonus_rules:
  - id: welcome_bonus
    type: deposit_match
    match_percent: 100
    max_bonus: 500
    min_deposit: 20
    wagering_multiplier: 35
    eligible_games:
      - slots
      - table_games
    excluded_games:
      - live_blackjack
    expiry_days: 30
    max_bet_percent: 10
    
  - id: friday_reload  
    type: deposit_match
    match_percent: 50
    max_bonus: 200
    schedule:
      day_of_week: friday
    conditions:
      - min_deposits_lifetime: 3
```

## Monitoring

Grafana dashboards included:
- Transaction throughput & latency
- Fraud score distribution
- Bonus conversion rates
- Player LTV segments

## License

MIT License - feel free to use for learning and portfolio purposes.

## Author

Built with ❤️ as a portfolio project demonstrating iGaming backend expertise.
