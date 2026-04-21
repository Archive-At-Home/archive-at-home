package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/scheduler"
)

// ─────────────────────────────────────────────
// Result Waiter: async WS → sync HTTP bridge
// ─────────────────────────────────────────────

// ResultWaiter maps TraceID → channel, allowing HTTP handlers
// to block until a WebSocket result arrives.
type ResultWaiter struct {
	mu      sync.Mutex
	waiters map[string][]chan *model.TaskResult
}

func NewResultWaiter() *ResultWaiter {
	return &ResultWaiter{
		waiters: make(map[string][]chan *model.TaskResult),
	}
}

// Register creates a channel for the given traceID and returns it.
func (rw *ResultWaiter) Register(traceID string) <-chan *model.TaskResult {
	ch := make(chan *model.TaskResult, 1)
	rw.mu.Lock()
	rw.waiters[traceID] = append(rw.waiters[traceID], ch)
	rw.mu.Unlock()
	return ch
}

// Unregister removes a specific channel from the waiters for the given traceID.
// This prevents memory leaks when requests timeout or are cancelled.
func (rw *ResultWaiter) Unregister(traceID string, ch <-chan *model.TaskResult) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	chs := rw.waiters[traceID]
	for i, c := range chs {
		if c == ch {
			// Remove this channel from the slice
			rw.waiters[traceID] = append(chs[:i], chs[i+1:]...)
			// If no more waiters, clean up the map entry
			if len(rw.waiters[traceID]) == 0 {
				delete(rw.waiters, traceID)
			}
			break
		}
	}
}

// Notify delivers a result to all waiters for the given traceID.
func (rw *ResultWaiter) Notify(traceID string, result *model.TaskResult) {
	rw.mu.Lock()
	chs := rw.waiters[traceID]
	delete(rw.waiters, traceID)
	rw.mu.Unlock()

	for _, ch := range chs {
		select {
		case ch <- result:
		default:
		}
	}
}

// ─────────────────────────────────────────────
// Hub: manages all connected worker nodes
// ─────────────────────────────────────────────

// Hub maintains the set of active WebSocket clients and
// broadcasts task announcements to all of them.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client // nodeID → Client
	sched   *scheduler.Scheduler
	waiter  *ResultWaiter
}

// NewHub creates a new Hub.
func NewHub(sched *scheduler.Scheduler, waiter *ResultWaiter) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		sched:   sched,
		waiter:  waiter,
	}
}

// Register adds a client to the hub. Returns an error if the node is already connected.
func (h *Hub) Register(c *Client) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[c.NodeID]; ok {
		return fmt.Errorf("node %s already connected", c.NodeID)
	}
	h.clients[c.NodeID] = c
	log.Printf("[hub] node %s connected (total: %d)", c.NodeID, len(h.clients))
	return nil
}

// Unregister removes a client from the hub.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c.NodeID)
	log.Printf("[hub] node %s disconnected (total: %d)", c.NodeID, len(h.clients))
}

// NodeCount returns the number of connected nodes.
func (h *Hub) NodeCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// NodeIDs returns the IDs of all currently connected nodes.
func (h *Hub) NodeIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	return ids
}

// BroadcastTaskAnnouncement sends a task announcement to all connected nodes.
func (h *Hub) BroadcastTaskAnnouncement(ctx context.Context, ann *model.TaskAnnouncement) error {
	env := model.Envelope{
		Type:    model.MsgTypeTaskAnnouncement,
		Payload: ann,
	}
	data, err := json.Marshal(env)
	if err != nil {
		log.Printf("[hub] marshal announcement error: %v", err)
		return fmt.Errorf("marshal announcement: %w", err)
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	var sent int
	for _, c := range h.clients {
		select {
		case c.send <- data:
			sent++
		default:
			log.Printf("[hub] send buffer full for node %s, dropping", c.NodeID)
		}
	}

	if sent == 0 {
		log.Printf("[hub] no nodes received announcement trace=%s (online=%d)", ann.TraceID, len(h.clients))
		return fmt.Errorf("no nodes available")
	}
	log.Printf("[hub] broadcast TASK_ANNOUNCEMENT trace=%s to %d/%d nodes", ann.TraceID, sent, len(h.clients))
	return nil
}

// HandleFetchTask processes a FETCH_TASK request from a worker node.
func (h *Hub) HandleFetchTask(ctx context.Context, c *Client, req *model.FetchTaskRequest) {
	assignment, err := h.sched.FetchTask(ctx, req.TraceID, c.NodeID)
	if err != nil {
		log.Printf("[hub] fetch task error: %v", err)
		return
	}

	var env model.Envelope
	if assignment == nil {
		// Task already claimed
		env = model.Envelope{
			Type:    model.MsgTypeTaskGone,
			Payload: map[string]string{"trace_id": req.TraceID},
		}
	} else {
		env = model.Envelope{
			Type:    model.MsgTypeTaskAssigned,
			Payload: assignment,
		}
	}

	data, err := json.Marshal(env)
	if err != nil {
		log.Printf("[hub] marshal response error: %v", err)
		return
	}
	select {
	case c.send <- data:
	default:
		log.Printf("[hub] send buffer full for node %s", c.NodeID)
	}
}

// HandleTaskResult processes a TASK_RESULT submission from a worker node.
func (h *Hub) HandleTaskResult(ctx context.Context, c *Client, result *model.TaskResult) {
	log.Printf("[hub] received result for trace=%s from node=%s success=%v",
		result.TraceID, c.NodeID, result.Success)

	if result.Success && result.ArchiveURL == "" {
		result.Success = false
		if result.Error == "" {
			result.Error = "task reported success but archive_url is empty"
		}
	}

	if result.Success {
		if err := h.sched.CompleteTask(ctx, result.TraceID, c.NodeID, result.ArchiveURL); err != nil {
			log.Printf("[hub] complete task error: %v", err)
			result.Success = false
			if result.Error == "" {
				result.Error = "failed to finalize successful task"
			}
			if failErr := h.sched.FailTask(ctx, result.TraceID, c.NodeID); failErr != nil {
				log.Printf("[hub] fail task after complete error: %v", failErr)
			}
		}
	} else {
		if err := h.sched.FailTask(ctx, result.TraceID, c.NodeID); err != nil {
			log.Printf("[hub] fail task error: %v", err)
		}
	}

	// Notify HTTP waiters regardless of success
	h.waiter.Notify(result.TraceID, result)
}
