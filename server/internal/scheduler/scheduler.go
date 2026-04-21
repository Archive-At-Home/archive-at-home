package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

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
	reclaimScript  *redis.Script
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
		reclaimScript:  redis.NewScript(LuaReclaimTask),
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
	leaseTTL := int(s.cfg.TaskLeaseTTL.Seconds())

	keys := []string{
		model.TaskKey(traceID),
		model.CollapsingKey(userID, galleryID),
		model.CacheKey(userID, galleryID),
	}
	args := []interface{}{traceID, galleryID, boolToFlag(force), leaseTTL, galleryKey}

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
	leaseTTL := int(s.cfg.TaskLeaseTTL.Seconds())

	keys := []string{model.TaskKey(traceID)}
	args := []interface{}{nodeID, leaseTTL}

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

// CompleteTask stores the result and updates caches.
// nodeID must match the node currently assigned to the task.
func (s *Scheduler) CompleteTask(ctx context.Context, traceID, nodeID, archiveURL string) error {
	keys := []string{model.TaskKey(traceID)}
	args := []interface{}{archiveURL, int(s.cfg.CacheTTL.Seconds()), nodeID, traceID}

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

// FailTask marks a task as failed and removes collapse/pending state.
// For PROCESSING tasks, nodeID must match the currently assigned node.
// For PENDING tasks, pass nodeID as an empty string.
func (s *Scheduler) FailTask(ctx context.Context, traceID, nodeID string) error {
	return s.finalizeTask(ctx, traceID, nodeID, "FAIL")
}

// RejectTask removes a task entirely (used for initialization/pre-flight rejections).
func (s *Scheduler) RejectTask(ctx context.Context, traceID string) error {
	return s.finalizeTask(ctx, traceID, "", "REJECT")
}

func (s *Scheduler) finalizeTask(ctx context.Context, traceID, nodeID, mode string) error {
	keys := []string{model.TaskKey(traceID)}
	args := []interface{}{nodeID, traceID, mode}

	status, err := s.failScript.Run(ctx, s.rdb, keys, args...).Text()
	if err != nil {
		return fmt.Errorf("finalize task lua: %w", err)
	}
	if status == "NODE_MISMATCH" {
		return fmt.Errorf("task reassigned to another node (stale failure attempt)")
	}
	if status == "NEED_NODE" {
		return fmt.Errorf("processing task failure requires node identity")
	}
	if status == "GONE" {
		return fmt.Errorf("task not found")
	}
	if status != "OK" {
		return fmt.Errorf("finalize task: unexpected status %s", status)
	}
	return nil
}

// UpdateTaskCost sets task metadata used for node claim strategy and billing.
func (s *Scheduler) UpdateTaskCost(ctx context.Context, traceID string, freeTier bool, estimatedGP int) error {
	err := s.rdb.HSet(ctx, model.TaskKey(traceID), map[string]interface{}{
		"free_tier":    boolToFlag(freeTier),
		"estimated_gp": estimatedGP,
	}).Err()
	if err != nil {
		return fmt.Errorf("update task cost: %w", err)
	}
	return nil
}

// PendingQueueLen returns the current length of the pending queue.
func (s *Scheduler) PendingQueueLen(ctx context.Context) (int64, error) {
	return s.rdb.LLen(ctx, model.PendingQueueKey).Result()
}

// ─────────────────────────────────────────────
// Lease Watchdog (background goroutine)
// ─────────────────────────────────────────────

// StartLeaseWatchdog periodically scans for expired task keys
// whose lease TTL has passed and re-enqueues them.
// It runs until ctx is cancelled.
func (s *Scheduler) StartLeaseWatchdog(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	log.Println("[scheduler] lease watchdog started")
	for {
		select {
		case <-ctx.Done():
			log.Println("[scheduler] lease watchdog stopped")
			return
		case <-ticker.C:
			s.reclaimExpiredTasks(ctx)
		}
	}
}

// reclaimExpiredTasks scans for stuck PROCESSING tasks and:
// 1. Checks if task is still in PROCESSING state with low TTL
// 2. Calls LuaReclaimTask to reset, clear collapseKey, and re-enqueue
// 3. Removes truly expired tasks from queue
func (s *Scheduler) reclaimExpiredTasks(ctx context.Context) {
	queueLen, err := s.rdb.LLen(ctx, model.PendingQueueKey).Result()
	if err != nil || queueLen == 0 {
		return
	}

	// Scan up to 100 entries
	limit := queueLen
	if limit > 100 {
		limit = 100
	}

	// Collect all traceIDs first, then process.
	// This avoids index shifting when LREM modifies the list during iteration.
	traceIDs := make([]string, 0, limit)
	for i := int64(0); i < limit; i++ {
		traceID, err := s.rdb.LIndex(ctx, model.PendingQueueKey, i).Result()
		if err != nil {
			break
		}
		traceIDs = append(traceIDs, traceID)
	}

	leaseTTL := int(s.cfg.TaskLeaseTTL.Seconds())
	reclaimThreshold := leaseTTL / 2 // Reclaim if TTL < 50% of lease

	for _, traceID := range traceIDs {
		taskKey := model.TaskKey(traceID)

		// Check if task exists
		pipe := s.rdb.Pipeline()
		ttlCmd := pipe.TTL(ctx, taskKey)
		statusCmd := pipe.HGet(ctx, taskKey, "status")
		_, err = pipe.Exec(ctx)
		if err != nil {
			// Task doesn't exist – remove from queue
			s.rdb.LRem(ctx, model.PendingQueueKey, 1, traceID)
			log.Printf("[scheduler] removed expired task %s from queue", traceID)
			continue
		}

		ttl := ttlCmd.Val().Seconds()
		status := statusCmd.Val()

		// If task is PROCESSING and TTL is low, reclaim it
		if status == "PROCESSING" && ttl > 0 && ttl < float64(reclaimThreshold) {
			keys := []string{taskKey}
			args := []interface{}{leaseTTL, traceID}

			result, err := s.reclaimScript.Run(ctx, s.rdb, keys, args...).Text()
			if err != nil {
				log.Printf("[scheduler] reclaim task %s error: %v", traceID, err)
				continue
			}
			if result == "RECLAIMED" {
				log.Printf("[scheduler] reclaimed stuck task %s (TTL was %.0fs)", traceID, ttl)
			}
		}
	}
}
