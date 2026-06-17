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
	rdb *redis.Client
	cfg *config.Config

	// Pre-loaded Lua script SHAs
	fetchScript    *redis.Script
	completeScript *redis.Script
	failScript     *redis.Script
	publishScript  *redis.Script
}

// NewScheduler initialises the scheduler and loads Lua scripts.
func NewScheduler(rdb *redis.Client, cfg *config.Config) *Scheduler {
	return &Scheduler{
		rdb:            rdb,
		cfg:            cfg,
		fetchScript:    redis.NewScript(LuaFetchTask),
		completeScript: redis.NewScript(LuaCompleteTask),
		failScript:     redis.NewScript(LuaFailTask),
		publishScript:  redis.NewScript(LuaPublishTask),
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

// PublishTask creates a new task or collapses into an existing one.
// Returns status + payload:
//   - PublishCreated: payload is created traceID
//   - PublishCollapsed: payload is existing traceID
//   - PublishCached: payload is archiveURL
func (s *Scheduler) PublishTask(ctx context.Context, traceID, userID, galleryID, galleryKey string, force bool) (PublishStatus, string, error) {
	keys := []string{
		model.TaskKey(traceID),
		model.CollapsingKey(userID, galleryID),
		model.CacheKey(userID, galleryID),
	}
	args := []any{traceID, galleryID, boolToFlag(force), galleryKey}

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

// FetchTask lets a worker node attempt to claim a pending task.
// Returns the assignment details or an indication that the task is gone.
func (s *Scheduler) FetchTask(ctx context.Context, traceID, nodeID string) (*model.TaskAssignment, error) {
	keys := []string{model.TaskKey(traceID)}
	args := []any{nodeID}

	vals, err := s.fetchScript.Run(ctx, s.rdb, keys, args...).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("fetch task lua: %w", err)
	}

	if vals[0] == "GONE" {
		return nil, nil // task already claimed
	}

	// vals = ["OK", galleryID, galleryKey]
	return &model.TaskAssignment{
		TraceID:    traceID,
		GalleryID:  vals[1],
		GalleryKey: vals[2],
	}, nil
}

// CompleteTask stores the result in cache and removes task state.
// nodeID must match the node currently assigned to the task.
func (s *Scheduler) CompleteTask(ctx context.Context, traceID, nodeID, archiveURL string) error {
	keys := []string{model.TaskKey(traceID)}
	args := []any{archiveURL, int(s.cfg.CacheTTL.Seconds()), nodeID}

	status, err := s.completeScript.Run(ctx, s.rdb, keys, args...).Text()
	if err != nil {
		return fmt.Errorf("complete task lua: %w", err)
	}
	if status == "NODE_MISMATCH" {
		return fmt.Errorf("task reassigned to another node (stale completion attempt)")
	}
	if status != "OK" {
		return fmt.Errorf("complete task: unexpected status %s", status)
	}
	return nil
}

// FailTask removes a task and clears collapse state.
// For PROCESSING tasks, nodeID must match the currently assigned node.
// For PENDING tasks, pass nodeID as an empty string.
func (s *Scheduler) FailTask(ctx context.Context, traceID, nodeID string) error {
	keys := []string{model.TaskKey(traceID)}
	args := []any{nodeID}

	status, err := s.failScript.Run(ctx, s.rdb, keys, args...).Text()
	if err != nil {
		return fmt.Errorf("fail task lua: %w", err)
	}
	if status == "NODE_MISMATCH" {
		return fmt.Errorf("task reassigned to another node (stale fail attempt)")
	}
	if status == "NEED_NODE" {
		return fmt.Errorf("processing task failure requires node identity")
	}
	if status == "GONE" {
		return fmt.Errorf("task not found")
	}
	if status != "OK" {
		return fmt.Errorf("fail task: unexpected status %s", status)
	}
	return nil
}
