package tokenbucket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/redis/go-redis/v9"
)

// ErrInsufficientTokens is returned when a Consume call cannot
// be satisfied because the bucket does not have enough tokens.
var ErrInsufficientTokens = errors.New("insufficient tokens")

// ─────────────────────────────────────────────
// TokenBucket
// ─────────────────────────────────────────────

type TokenBucket struct {
	rdb           *redis.Client
	rate          int64
	maxCapacity   int64
	vipRate       int64
	vipCapacity   int64
	consumeScript *redis.Script
	refundScript  *redis.Script
}

// NewTokenBucket creates a new TokenBucket backed by Redis.
func NewTokenBucket(rdb *redis.Client, cfg *config.Config) *TokenBucket {
	return &TokenBucket{
		rdb:           rdb,
		rate:          int64(cfg.TokenRate),
		maxCapacity:   int64(cfg.TokenMaxCapacity),
		vipRate:       int64(cfg.TokenVIPRate),
		vipCapacity:   int64(cfg.TokenVIPCapacity),
		consumeScript: redis.NewScript(LuaConsumeTokens),
		refundScript:  redis.NewScript(LuaRefundTokens),
	}
}

func (s *TokenBucket) paramsForLevel(level int) (rate, capacity, ttl int64) {
	if level >= 1 {
		return s.vipRate, s.vipCapacity, s.vipCapacity / s.vipRate
	}
	return s.rate, s.maxCapacity, s.maxCapacity / s.rate
}

// ─────────────────────────────────────────────
// GetTokens
// ─────────────────────────────────────────────

// GetTokens returns the user's current available token count.
// This is a read-only calculation — it does not mutate state,
// so a concurrent Consume may race.
func (s *TokenBucket) GetTokens(ctx context.Context, userID string, level int) (int64, error) {
	rate, capacity, _ := s.paramsForLevel(level)
	nowSec := time.Now().Unix()

	var bucket struct {
		Tokens    int64 `redis:"tokens"`
		UpdatedAt int64 `redis:"updated_at"`
	}
	// Missing key → zero values → refill from 0 over huge elapsed → capped at capacity. Correct.
	if err := s.rdb.HGetAll(ctx, model.TokenBucketKey(userID)).Scan(&bucket); err != nil {
		return 0, err
	}

	elapsed := nowSec - bucket.UpdatedAt
	return min(bucket.Tokens+elapsed*rate, capacity), nil
}

// ─────────────────────────────────────────────
// Consume
// ─────────────────────────────────────────────

// Consume atomically deducts tokens from the user's bucket.
func (s *TokenBucket) Consume(ctx context.Context, userID string, level int, amount int64) error {
	rate, capacity, ttl := s.paramsForLevel(level)
	key := model.TokenBucketKey(userID)
	nowSec := time.Now().Unix()

	vals, err := s.consumeScript.Run(ctx, s.rdb, []string{key},
		amount, rate, capacity, nowSec, ttl,
	).Text()
	if err != nil {
		return err
	}

	switch vals {
	case "OK":
		return nil
	case "INSUFFICIENT":
		return ErrInsufficientTokens
	default:
		return fmt.Errorf("consume tokens: unexpected script status: %s", vals)
	}
}

// ─────────────────────────────────────────────
// Refund
// ─────────────────────────────────────────────

// Refund atomically returns tokens to the bucket, capped at maxCapacity.
func (s *TokenBucket) Refund(ctx context.Context, userID string, level int, amount int64) error {
	rate, capacity, ttl := s.paramsForLevel(level)
	key := model.TokenBucketKey(userID)
	nowSec := time.Now().Unix()

	_, err := s.refundScript.Run(ctx, s.rdb, []string{key},
		amount, rate, capacity, nowSec, ttl,
	).Result()
	return err
}
