package scheduler

import (
	"context"
	"fmt"

	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/redis/go-redis/v9"
)

type PublishStatus int

const (
	PublishCreated PublishStatus = iota
	PublishCollapsed
	PublishCached
)

// Scheduler manages task lifecycle via Redis.
type Scheduler struct {
	rdb           *redis.Client
	cfg           *config.Config
	publishScript *redis.Script
}

// NewScheduler initialises the scheduler and loads the publish Lua script.
func NewScheduler(rdb *redis.Client, cfg *config.Config) *Scheduler {
	return &Scheduler{
		rdb:           rdb,
		cfg:           cfg,
		publishScript: redis.NewScript(LuaPublishTask),
	}
}

func boolToFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// ─────────────────────────────────────────────
// Public API
// ─────────────────────────────────────────────

// PublishTask creates a new collapsing sentinel or collapses into an existing one.
// Returns status + payload:
//   - PublishCreated: payload is the new traceID
//   - PublishCollapsed: payload is the existing traceID
//   - PublishCached: payload is the archiveURL
func (s *Scheduler) PublishTask(ctx context.Context, traceID, userID, galleryID string, force bool) (PublishStatus, string, error) {
	keys := []string{
		model.CollapsingKey(userID, galleryID),
		model.CacheKey(userID, galleryID),
	}
	args := []any{traceID, boolToFlag(force)}

	vals, err := s.publishScript.Run(ctx, s.rdb, keys, args...).StringSlice()
	if err != nil {
		return PublishCreated, "", fmt.Errorf("publish task lua: %w", err)
	}
	if len(vals) < 2 {
		return PublishCreated, "", fmt.Errorf("publish task lua: invalid response")
	}

	switch vals[0] {
	case "CREATED":
		return PublishCreated, vals[1], nil
	case "COLLAPSED":
		return PublishCollapsed, vals[1], nil
	case "CACHED":
		return PublishCached, vals[1], nil
	default:
		return PublishCreated, "", fmt.Errorf("publish task lua: unexpected status %s", vals[0])
	}
}

// CompleteTask stores the result in cache and removes the collapsing sentinel.
func (s *Scheduler) CompleteTask(ctx context.Context, userID, galleryID, archiveURL string) error {
	collapseKey := model.CollapsingKey(userID, galleryID)
	cacheKey := model.CacheKey(userID, galleryID)
	ttl := s.cfg.CacheTTL

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, cacheKey, archiveURL, ttl)
	pipe.Del(ctx, collapseKey)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	return nil
}

// FailTask removes the collapsing sentinel so a subsequent request
// for the same user+gallery can create a fresh task.
func (s *Scheduler) FailTask(ctx context.Context, userID, galleryID string) error {
	collapseKey := model.CollapsingKey(userID, galleryID)
	if err := s.rdb.Del(ctx, collapseKey).Err(); err != nil {
		return fmt.Errorf("fail task: %w", err)
	}
	return nil
}
