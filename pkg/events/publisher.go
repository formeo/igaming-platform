// Package events implements event publishing to RabbitMQ
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Event types
const (
	EventAccountCreated       = "account.created"
	EventTransactionCompleted = "transaction.completed"
	EventTransactionFailed    = "transaction.failed"
	EventDepositReceived      = "deposit.received"
	EventWithdrawalRequested  = "withdrawal.requested"
	EventWithdrawalCompleted  = "withdrawal.completed"
	EventBetPlaced            = "bet.placed"
	EventWinPaid              = "win.paid"
	EventBonusAwarded         = "bonus.awarded"
	EventBonusCompleted       = "bonus.completed"
	EventBonusExpired         = "bonus.expired"
	EventRiskScoreHigh        = "risk.score.high"
	EventRiskBlocked          = "risk.blocked"
	EventFraudDetected        = "fraud.detected"
)

// Exchange and queue names
const (
	ExchangeWallet  = "wallet.events"
	ExchangeBonus   = "bonus.events"
	ExchangeRisk    = "risk.events"
	
	QueueRiskScoring    = "risk.scoring"
	QueueBonusProcessor = "bonus.processor"
	QueueAnalytics      = "analytics.events"
	QueueNotifications  = "notifications.events"
)

// Event represents a domain event
type Event struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Source      string                 `json:"source"`
	AggregateID string                 `json:"aggregate_id"`
	Timestamp   time.Time              `json:"timestamp"`
	Version     int                    `json:"version"`
	Data        map[string]interface{} `json:"data"`
	Metadata    map[string]string      `json:"metadata"`
}

// NewEvent creates a new event
func NewEvent(eventType, source, aggregateID string, data map[string]interface{}) *Event {
	return &Event{
		ID:          uuid.New().String(),
		Type:        eventType,
		Source:      source,
		AggregateID: aggregateID,
		Timestamp:   time.Now().UTC(),
		Version:     1,
		Data:        data,
		Metadata:    make(map[string]string),
	}
}

// Publisher interface for event publishing
type Publisher interface {
	Publish(ctx context.Context, exchange string, event *Event) error
	PublishWithRouting(ctx context.Context, exchange, routingKey string, event *Event) error
	Close() error
}

// RabbitMQPublisher implements Publisher for RabbitMQ
type RabbitMQPublisher struct {
	conn       *amqp.Connection
	channel    *amqp.Channel
	logger     *slog.Logger
	
	mu         sync.RWMutex
	closed     bool
	confirms   chan amqp.Confirmation
}

// PublisherConfig for RabbitMQ connection
type PublisherConfig struct {
	URL               string
	ReconnectDelay    time.Duration
	MaxReconnects     int
	PublishTimeout    time.Duration
	EnableConfirms    bool
}

// DefaultPublisherConfig returns sensible defaults
func DefaultPublisherConfig(url string) PublisherConfig {
	return PublisherConfig{
		URL:            url,
		ReconnectDelay: 5 * time.Second,
		MaxReconnects:  10,
		PublishTimeout: 5 * time.Second,
		EnableConfirms: true,
	}
}

// NewRabbitMQPublisher creates new RabbitMQ publisher
func NewRabbitMQPublisher(cfg PublisherConfig, logger *slog.Logger) (*RabbitMQPublisher, error) {
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	
	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create channel: %w", err)
	}
	
	// Declare exchanges
	exchanges := []string{ExchangeWallet, ExchangeBonus, ExchangeRisk}
	for _, exchange := range exchanges {
		err = channel.ExchangeDeclare(
			exchange,
			"topic",     // type
			true,        // durable
			false,       // auto-deleted
			false,       // internal
			false,       // no-wait
			nil,         // arguments
		)
		if err != nil {
			return nil, fmt.Errorf("failed to declare exchange %s: %w", exchange, err)
		}
	}
	
	p := &RabbitMQPublisher{
		conn:    conn,
		channel: channel,
		logger:  logger,
	}
	
	// Enable publisher confirms
	if cfg.EnableConfirms {
		if err := channel.Confirm(false); err != nil {
			return nil, fmt.Errorf("failed to enable confirms: %w", err)
		}
		p.confirms = channel.NotifyPublish(make(chan amqp.Confirmation, 100))
	}
	
	logger.Info("RabbitMQ publisher initialized")
	
	return p, nil
}

// Publish sends event to exchange with event type as routing key
func (p *RabbitMQPublisher) Publish(ctx context.Context, exchange string, event *Event) error {
	return p.PublishWithRouting(ctx, exchange, event.Type, event)
}

// PublishWithRouting sends event with custom routing key
func (p *RabbitMQPublisher) PublishWithRouting(ctx context.Context, exchange, routingKey string, event *Event) error {
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return fmt.Errorf("publisher is closed")
	}
	p.mu.RUnlock()
	
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}
	
	msg := amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Timestamp:    event.Timestamp,
		ContentType:  "application/json",
		MessageId:    event.ID,
		Type:         event.Type,
		Body:         body,
	}
	
	err = p.channel.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		msg,
	)
	if err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}
	
	// Wait for confirm if enabled
	if p.confirms != nil {
		select {
		case confirm := <-p.confirms:
			if !confirm.Ack {
				return fmt.Errorf("message not acknowledged")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	
	p.logger.Debug("event published",
		"event_id", event.ID,
		"type", event.Type,
		"exchange", exchange,
	)
	
	return nil
}

// Close closes the connection
func (p *RabbitMQPublisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	
	p.closed = true
	
	if p.channel != nil {
		p.channel.Close()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// Consumer interface for event consumption
type Consumer interface {
	Subscribe(queue string, handler EventHandler) error
	Start(ctx context.Context) error
	Stop() error
}

// EventHandler processes events
type EventHandler func(ctx context.Context, event *Event) error

// RabbitMQConsumer implements Consumer for RabbitMQ
type RabbitMQConsumer struct {
	conn     *amqp.Connection
	channel  *amqp.Channel
	logger   *slog.Logger
	handlers map[string]EventHandler
	
	mu       sync.RWMutex
	running  bool
}

// ConsumerConfig for consumer
type ConsumerConfig struct {
	URL           string
	PrefetchCount int
	RetryCount    int
	RetryDelay    time.Duration
}

// NewRabbitMQConsumer creates new consumer
func NewRabbitMQConsumer(cfg ConsumerConfig, logger *slog.Logger) (*RabbitMQConsumer, error) {
	conn, err := amqp.Dial(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	
	channel, err := conn.Channel()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to create channel: %w", err)
	}
	
	// Set prefetch
	if cfg.PrefetchCount > 0 {
		err = channel.Qos(cfg.PrefetchCount, 0, false)
		if err != nil {
			return nil, fmt.Errorf("failed to set QoS: %w", err)
		}
	}
	
	return &RabbitMQConsumer{
		conn:     conn,
		channel:  channel,
		logger:   logger,
		handlers: make(map[string]EventHandler),
	}, nil
}

// Subscribe registers handler for queue
func (c *RabbitMQConsumer) Subscribe(queue string, handler EventHandler) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// Declare queue
	_, err := c.channel.QueueDeclare(
		queue,
		true,  // durable
		false, // auto-delete
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare queue: %w", err)
	}
	
	c.handlers[queue] = handler
	return nil
}

// Start begins consuming messages
func (c *RabbitMQConsumer) Start(ctx context.Context) error {
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	
	for queue, handler := range c.handlers {
		msgs, err := c.channel.Consume(
			queue,
			"",    // consumer tag
			false, // auto-ack
			false, // exclusive
			false, // no-local
			false, // no-wait
			nil,   // args
		)
		if err != nil {
			return fmt.Errorf("failed to consume from %s: %w", queue, err)
		}
		
		go c.processMessages(ctx, queue, handler, msgs)
	}
	
	return nil
}

func (c *RabbitMQConsumer) processMessages(ctx context.Context, queue string, handler EventHandler, msgs <-chan amqp.Delivery) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-msgs:
			if !ok {
				c.logger.Warn("channel closed", "queue", queue)
				return
			}
			
			var event Event
			if err := json.Unmarshal(msg.Body, &event); err != nil {
				c.logger.Error("failed to unmarshal event",
					"queue", queue,
					"error", err,
				)
				msg.Reject(false) // don't requeue malformed messages
				continue
			}
			
			if err := handler(ctx, &event); err != nil {
				c.logger.Error("failed to process event",
					"queue", queue,
					"event_id", event.ID,
					"error", err,
				)
				msg.Nack(false, true) // requeue
				continue
			}
			
			msg.Ack(false)
		}
	}
}

// Stop stops the consumer
func (c *RabbitMQConsumer) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	c.running = false
	
	if c.channel != nil {
		c.channel.Close()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Convenience functions for creating common events

// NewTransactionEvent creates transaction event
func NewTransactionEvent(eventType string, tx TransactionData) *Event {
	return NewEvent(eventType, "wallet-service", tx.AccountID, map[string]interface{}{
		"transaction_id":   tx.ID,
		"account_id":       tx.AccountID,
		"type":             tx.Type,
		"amount":           tx.Amount,
		"balance_before":   tx.BalanceBefore,
		"balance_after":    tx.BalanceAfter,
		"status":           tx.Status,
		"game_id":          tx.GameID,
		"round_id":         tx.RoundID,
		"risk_score":       tx.RiskScore,
	})
}

// TransactionData for transaction events
type TransactionData struct {
	ID            string
	AccountID     string
	Type          string
	Amount        int64
	BalanceBefore int64
	BalanceAfter  int64
	Status        string
	GameID        string
	RoundID       string
	RiskScore     int
}

// NewBonusEvent creates bonus event
func NewBonusEvent(eventType string, bonus BonusData) *Event {
	return NewEvent(eventType, "bonus-service", bonus.AccountID, map[string]interface{}{
		"bonus_id":           bonus.ID,
		"account_id":         bonus.AccountID,
		"rule_id":            bonus.RuleID,
		"type":               bonus.Type,
		"amount":             bonus.Amount,
		"wagering_required":  bonus.WageringRequired,
		"wagering_progress":  bonus.WageringProgress,
	})
}

// BonusData for bonus events
type BonusData struct {
	ID               string
	AccountID        string
	RuleID           string
	Type             string
	Amount           int64
	WageringRequired int64
	WageringProgress int64
}

// NewRiskEvent creates risk event
func NewRiskEvent(eventType string, risk RiskData) *Event {
	return NewEvent(eventType, "risk-service", risk.AccountID, map[string]interface{}{
		"account_id":     risk.AccountID,
		"transaction_id": risk.TransactionID,
		"score":          risk.Score,
		"action":         risk.Action,
		"reason_codes":   risk.ReasonCodes,
	})
}

// RiskData for risk events
type RiskData struct {
	AccountID     string
	TransactionID string
	Score         int
	Action        string
	ReasonCodes   []string
}
