# iGaming Platform Makefile
# ======================================

.PHONY: all build test lint clean proto docker infra help

# Variables
GO := go
GOFLAGS := -v
DOCKER_COMPOSE := docker compose -f deploy/docker-compose.yml

# Service binaries
WALLET_BIN := bin/wallet
BONUS_BIN := bin/bonus
RISK_BIN := bin/risk

# Proto paths
PROTO_DIR := proto
PROTO_OUT := gen/proto

# Default target
all: build

# ======================================
# Build targets
# ======================================

build: build-wallet build-bonus build-risk ## Build all services

build-wallet: ## Build wallet service
	@echo "Building wallet service..."
	$(GO) build $(GOFLAGS) -o $(WALLET_BIN) ./services/wallet/cmd/

build-bonus: ## Build bonus service
	@echo "Building bonus service..."
	$(GO) build $(GOFLAGS) -o $(BONUS_BIN) ./services/bonus/cmd/

build-risk: ## Build risk service
	@echo "Building risk service..."
	$(GO) build $(GOFLAGS) -o $(RISK_BIN) ./services/risk/cmd/

# ======================================
# Development targets
# ======================================

run-wallet: build-wallet ## Run wallet service locally
	@echo "Starting wallet service..."
	DATABASE_URL=postgres://igaming:igaming_secret@localhost:5432/igaming?sslmode=disable \
	REDIS_URL=redis://localhost:6379 \
	$(WALLET_BIN)

run-bonus: build-bonus ## Run bonus service locally
	@echo "Starting bonus service..."
	DATABASE_URL=postgres://igaming:igaming_secret@localhost:5432/igaming?sslmode=disable \
	REDIS_URL=redis://localhost:6379 \
	CONFIG_PATH=./services/bonus/configs/bonus_rules.yaml \
	$(BONUS_BIN)

run-risk: build-risk ## Run risk service locally
	@echo "Starting risk service..."
	REDIS_URL=redis://localhost:6379 \
	$(RISK_BIN)

run: ## Run all services (requires infra)
	@make -j3 run-wallet run-bonus run-risk

dev: infra-up ## Start development environment
	@echo "Development environment ready!"
	@echo "  - PostgreSQL: localhost:5432"
	@echo "  - Redis: localhost:6379"
	@echo "  - RabbitMQ: localhost:5672 (UI: localhost:15672)"
	@echo "  - ClickHouse: localhost:8123"
	@echo "  - Grafana: localhost:3000 (admin/admin)"

# ======================================
# Test targets
# ======================================

test: ## Run all tests
	@echo "Running tests..."
	$(GO) test -v -race -cover ./...

test-wallet: ## Run wallet service tests
	$(GO) test -v -race -cover ./services/wallet/...

test-bonus: ## Run bonus service tests
	$(GO) test -v -race -cover ./services/bonus/...

test-risk: ## Run risk service tests
	$(GO) test -v -race -cover ./services/risk/...

test-integration: ## Run integration tests (requires infra)
	@echo "Running integration tests..."
	$(GO) test -v -tags=integration ./tests/...

test-coverage: ## Generate coverage report
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

bench: ## Run benchmarks
	$(GO) test -bench=. -benchmem ./...

# ======================================
# Code quality targets
# ======================================

lint: ## Run linters
	@echo "Running linters..."
	golangci-lint run ./...

fmt: ## Format code
	@echo "Formatting code..."
	$(GO) fmt ./...
	gofumpt -l -w .

vet: ## Run go vet
	$(GO) vet ./...

tidy: ## Tidy go modules
	$(GO) mod tidy

check: fmt vet lint test ## Run all checks

# ======================================
# Proto targets
# ======================================

proto: ## Generate protobuf code
	@echo "Generating protobuf code..."
	@mkdir -p $(PROTO_OUT)
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=$(PROTO_OUT) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_OUT) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/wallet/v1/*.proto \
		$(PROTO_DIR)/risk/v1/*.proto

proto-clean: ## Clean generated proto files
	rm -rf $(PROTO_OUT)

# ======================================
# Database targets
# ======================================

migrate: ## Run database migrations
	@echo "Running migrations..."
	migrate -path db/migrations -database "$(DATABASE_URL)" up

migrate-down: ## Rollback last migration
	migrate -path db/migrations -database "$(DATABASE_URL)" down 1

migrate-create: ## Create new migration (usage: make migrate-create name=add_users)
	migrate create -ext sql -dir db/migrations -seq $(name)

db-reset: ## Reset database
	@echo "Resetting database..."
	psql "$(DATABASE_URL)" -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
	@make migrate

db-seed: ## Seed database with test data
	@echo "Seeding database..."
	psql "$(DATABASE_URL)" -f deploy/init-db.sql

# ======================================
# Infrastructure targets
# ======================================

infra-up: ## Start infrastructure (PostgreSQL, Redis, RabbitMQ, etc.)
	@echo "Starting infrastructure..."
	$(DOCKER_COMPOSE) up -d postgres redis rabbitmq clickhouse prometheus grafana
	@echo "Waiting for services to be ready..."
	@sleep 5
	@$(DOCKER_COMPOSE) ps

infra-down: ## Stop infrastructure
	@echo "Stopping infrastructure..."
	$(DOCKER_COMPOSE) down

infra-logs: ## Show infrastructure logs
	$(DOCKER_COMPOSE) logs -f

infra-clean: ## Remove infrastructure volumes
	$(DOCKER_COMPOSE) down -v

infra-status: ## Show infrastructure status
	$(DOCKER_COMPOSE) ps

# ======================================
# Docker targets
# ======================================

docker-build: ## Build Docker images
	@echo "Building Docker images..."
	docker build -t igaming/wallet:latest -f services/wallet/Dockerfile .
	docker build -t igaming/bonus:latest -f services/bonus/Dockerfile .
	docker build -t igaming/risk:latest -f services/risk/Dockerfile .

docker-push: ## Push Docker images
	docker push igaming/wallet:latest
	docker push igaming/bonus:latest
	docker push igaming/risk:latest

docker-up: ## Start all services in Docker
	$(DOCKER_COMPOSE) up -d

docker-down: ## Stop all services
	$(DOCKER_COMPOSE) down

docker-logs: ## Show service logs
	$(DOCKER_COMPOSE) logs -f wallet bonus risk

# ======================================
# ML Model targets
# ======================================

model-train: ## Train fraud detection model
	@echo "Training fraud model..."
	cd services/risk/training && python train_fraud_model.py

model-export: ## Export model to ONNX
	@echo "Exporting model to ONNX..."
	cd services/risk/training && python export_onnx.py

model-validate: ## Validate model performance
	@echo "Validating model..."
	cd services/risk/training && python validate_model.py

# ======================================
# API Testing targets
# ======================================

api-test: ## Run API tests with grpcurl
	@echo "Testing Wallet API..."
	grpcurl -plaintext localhost:9080 list
	@echo "Testing Risk API..."
	grpcurl -plaintext localhost:9082 list

api-health: ## Check service health
	@echo "Checking wallet service..."
	curl -s localhost:8080/health | jq
	@echo "Checking risk service..."
	curl -s localhost:8082/health | jq

# ======================================
# Utility targets
# ======================================

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -rf bin/
	rm -rf coverage.out coverage.html
	rm -rf $(PROTO_OUT)

deps: ## Download dependencies
	$(GO) mod download

tools: ## Install development tools
	@echo "Installing development tools..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install mvdan.cc/gofumpt@latest
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

env: ## Show environment info
	@echo "Go version: $(shell $(GO) version)"
	@echo "Docker version: $(shell docker --version)"
	@echo "Docker Compose version: $(shell docker compose version)"

# ======================================
# Help
# ======================================

help: ## Show this help
	@echo "iGaming Platform - Available targets:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make dev          # Start development environment"
	@echo "  make build        # Build all services"
	@echo "  make test         # Run all tests"
	@echo "  make docker-up    # Start all services in Docker"
