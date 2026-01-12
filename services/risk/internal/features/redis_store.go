// Package features implements feature store using Redis and ClickHouse
package features

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RedisFeatureStore implements real-time feature storage
type RedisFeatureStore struct {
	client *redis.Client
}

// NewRedisFeatureStore creates Redis-backed feature store
func NewRedisFeatureStore(client *redis.Client) *RedisFeatureStore {
	return &RedisFeatureStore{client: client}
}

// Keys patterns
const (
	keyTxCount1Min      = "features:%s:tx_count:1m"      // sliding window
	keyTxCount5Min      = "features:%s:tx_count:5m"
	keyTxCount1Hour     = "features:%s:tx_count:1h"
	keyTxSum1Hour       = "features:%s:tx_sum:1h"
	keyUniqueDevices    = "features:%s:devices:24h"      // HyperLogLog
	keyUniqueIPs        = "features:%s:ips:24h"          // HyperLogLog
	keyLastTxTimestamp  = "features:%s:last_tx"
	keySessionStart     = "features:%s:session_start"
	keyTxHistory        = "features:%s:tx_history"       // sorted set for velocity
)

// RealTimeFeatures from Redis
type RealTimeFeatures struct {
	TxCount1Min      int   
	TxCount5Min      int   
	TxCount1Hour     int   
	TxSum1Hour       int64 
	UniqueDevices24h int   
	UniqueIPs24h     int   
	LastTxTimestamp  int64 
	SessionStart     int64 
}

// TransactionEvent for updating features
type TransactionEvent struct {
	AccountID  uuid.UUID
	Amount     int64
	TxType     string
	IP         string
	DeviceID   string
	Timestamp  time.Time
}

// GetRealTimeFeatures retrieves all real-time features for an account
func (s *RedisFeatureStore) GetRealTimeFeatures(ctx context.Context, accountID uuid.UUID) (*RealTimeFeatures, error) {
	id := accountID.String()
	now := time.Now()
	
	pipe := s.client.Pipeline()
	
	// Get transaction counts from sorted set (sliding window)
	min1 := now.Add(-1 * time.Minute).Unix()
	min5 := now.Add(-5 * time.Minute).Unix()
	hour := now.Add(-1 * time.Hour).Unix()
	
	txHistoryKey := fmt.Sprintf(keyTxHistory, id)
	count1MinCmd := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(min1, 10), "+inf")
	count5MinCmd := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(min5, 10), "+inf")
	count1HourCmd := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(hour, 10), "+inf")
	
	// Get sum from hash
	sumCmd := pipe.Get(ctx, fmt.Sprintf(keyTxSum1Hour, id))
	
	// Get unique devices and IPs (HyperLogLog)
	devicesCmd := pipe.PFCount(ctx, fmt.Sprintf(keyUniqueDevices, id))
	ipsCmd := pipe.PFCount(ctx, fmt.Sprintf(keyUniqueIPs, id))
	
	// Get timestamps
	lastTxCmd := pipe.Get(ctx, fmt.Sprintf(keyLastTxTimestamp, id))
	sessionCmd := pipe.Get(ctx, fmt.Sprintf(keySessionStart, id))
	
	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("redis pipeline failed: %w", err)
	}
	
	features := &RealTimeFeatures{
		TxCount1Min:  int(count1MinCmd.Val()),
		TxCount5Min:  int(count5MinCmd.Val()),
		TxCount1Hour: int(count1HourCmd.Val()),
	}
	
	// Parse optional values
	if sum, err := sumCmd.Int64(); err == nil {
		features.TxSum1Hour = sum
	}
	if devices, err := devicesCmd.Result(); err == nil {
		features.UniqueDevices24h = int(devices)
	}
	if ips, err := ipsCmd.Result(); err == nil {
		features.UniqueIPs24h = int(ips)
	}
	if ts, err := lastTxCmd.Int64(); err == nil {
		features.LastTxTimestamp = ts
	}
	if ts, err := sessionCmd.Int64(); err == nil {
		features.SessionStart = ts
	}
	
	return features, nil
}

// UpdateRealTimeFeatures updates features after a transaction
func (s *RedisFeatureStore) UpdateRealTimeFeatures(ctx context.Context, accountID uuid.UUID, event *TransactionEvent) error {
	id := accountID.String()
	now := event.Timestamp.Unix()
	
	pipe := s.client.Pipeline()
	
	// Add to transaction history (sorted set with timestamp as score)
	txHistoryKey := fmt.Sprintf(keyTxHistory, id)
	pipe.ZAdd(ctx, txHistoryKey, redis.Z{
		Score:  float64(now),
		Member: fmt.Sprintf("%d:%d", now, event.Amount),
	})
	// Cleanup old entries (older than 1 hour)
	pipe.ZRemRangeByScore(ctx, txHistoryKey, "-inf", strconv.FormatInt(now-3600, 10))
	pipe.Expire(ctx, txHistoryKey, 2*time.Hour)
	
	// Update sum (with TTL)
	sumKey := fmt.Sprintf(keyTxSum1Hour, id)
	pipe.IncrBy(ctx, sumKey, event.Amount)
	pipe.Expire(ctx, sumKey, time.Hour)
	
	// Update unique devices (HyperLogLog)
	if event.DeviceID != "" {
		devicesKey := fmt.Sprintf(keyUniqueDevices, id)
		pipe.PFAdd(ctx, devicesKey, event.DeviceID)
		pipe.Expire(ctx, devicesKey, 24*time.Hour)
	}
	
	// Update unique IPs
	if event.IP != "" {
		ipsKey := fmt.Sprintf(keyUniqueIPs, id)
		pipe.PFAdd(ctx, ipsKey, event.IP)
		pipe.Expire(ctx, ipsKey, 24*time.Hour)
	}
	
	// Update last transaction timestamp
	pipe.Set(ctx, fmt.Sprintf(keyLastTxTimestamp, id), now, 7*24*time.Hour)
	
	// Update session start if not set
	sessionKey := fmt.Sprintf(keySessionStart, id)
	pipe.SetNX(ctx, sessionKey, now, 30*time.Minute)
	pipe.Expire(ctx, sessionKey, 30*time.Minute) // extend session
	
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to update features: %w", err)
	}
	
	return nil
}

// GetVelocity returns transaction velocity metrics
func (s *RedisFeatureStore) GetVelocity(ctx context.Context, accountID uuid.UUID) (count1min, count5min, count1hour int, err error) {
	id := accountID.String()
	now := time.Now()
	
	txHistoryKey := fmt.Sprintf(keyTxHistory, id)
	
	pipe := s.client.Pipeline()
	
	min1 := now.Add(-1 * time.Minute).Unix()
	min5 := now.Add(-5 * time.Minute).Unix()
	hour := now.Add(-1 * time.Hour).Unix()
	
	c1 := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(min1, 10), "+inf")
	c5 := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(min5, 10), "+inf")
	ch := pipe.ZCount(ctx, txHistoryKey, strconv.FormatInt(hour, 10), "+inf")
	
	_, err = pipe.Exec(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	
	return int(c1.Val()), int(c5.Val()), int(ch.Val()), nil
}

// CheckRateLimit checks if account exceeds rate limits
func (s *RedisFeatureStore) CheckRateLimit(ctx context.Context, accountID uuid.UUID, maxPerMin, maxPerHour int) (bool, error) {
	count1min, _, count1hour, err := s.GetVelocity(ctx, accountID)
	if err != nil {
		return false, err
	}
	
	return count1min >= maxPerMin || count1hour >= maxPerHour, nil
}

// IncrementCounter increments a named counter with TTL
func (s *RedisFeatureStore) IncrementCounter(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := s.client.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

// SetFeature sets a single feature value
func (s *RedisFeatureStore) SetFeature(ctx context.Context, accountID uuid.UUID, feature string, value interface{}, ttl time.Duration) error {
	key := fmt.Sprintf("features:%s:%s", accountID.String(), feature)
	return s.client.Set(ctx, key, value, ttl).Err()
}

// GetFeature gets a single feature value
func (s *RedisFeatureStore) GetFeature(ctx context.Context, accountID uuid.UUID, feature string) (string, error) {
	key := fmt.Sprintf("features:%s:%s", accountID.String(), feature)
	return s.client.Get(ctx, key).Result()
}

// DeleteAccountFeatures removes all features for an account
func (s *RedisFeatureStore) DeleteAccountFeatures(ctx context.Context, accountID uuid.UUID) error {
	pattern := fmt.Sprintf("features:%s:*", accountID.String())
	keys, err := s.client.Keys(ctx, pattern).Result()
	if err != nil {
		return err
	}
	if len(keys) > 0 {
		return s.client.Del(ctx, keys...).Err()
	}
	return nil
}

// Blacklist operations

const (
	keyBlacklistDevices     = "blacklist:devices"
	keyBlacklistIPs         = "blacklist:ips"
	keyBlacklistFingerprints = "blacklist:fingerprints"
)

// AddToBlacklist adds an identifier to blacklist
func (s *RedisFeatureStore) AddToBlacklist(ctx context.Context, listType, value string) error {
	var key string
	switch listType {
	case "device":
		key = keyBlacklistDevices
	case "ip":
		key = keyBlacklistIPs
	case "fingerprint":
		key = keyBlacklistFingerprints
	default:
		return fmt.Errorf("unknown blacklist type: %s", listType)
	}
	return s.client.SAdd(ctx, key, value).Err()
}

// CheckBlacklist checks if any identifier is blacklisted
func (s *RedisFeatureStore) CheckBlacklist(ctx context.Context, deviceID, fingerprint, ip string) (bool, error) {
	pipe := s.client.Pipeline()
	
	var cmds []*redis.BoolCmd
	if deviceID != "" {
		cmds = append(cmds, pipe.SIsMember(ctx, keyBlacklistDevices, deviceID))
	}
	if fingerprint != "" {
		cmds = append(cmds, pipe.SIsMember(ctx, keyBlacklistFingerprints, fingerprint))
	}
	if ip != "" {
		cmds = append(cmds, pipe.SIsMember(ctx, keyBlacklistIPs, ip))
	}
	
	_, err := pipe.Exec(ctx)
	if err != nil {
		return false, err
	}
	
	for _, cmd := range cmds {
		if cmd.Val() {
			return true, nil
		}
	}
	
	return false, nil
}
