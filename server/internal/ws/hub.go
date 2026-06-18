package ws

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
)

// ─────────────────────────────────────────────
// Result Waiter: async WS → sync HTTP bridge
// ─────────────────────────────────────────────

// ResultWaiter maps TraceID → channels and delivers task results to HTTP waiters.
type ResultWaiter struct {
	mu      sync.Mutex
	waiters map[string][]chan *model.TaskResult
}

// NewResultWaiter creates a new ResultWaiter.
func NewResultWaiter() *ResultWaiter {
	return &ResultWaiter{
		waiters: make(map[string][]chan *model.TaskResult),
	}
}

// Register creates a channel for the given traceID and returns it.
func (w *ResultWaiter) Register(traceID string) <-chan *model.TaskResult {
	ch := make(chan *model.TaskResult, 1)
	w.mu.Lock()
	w.waiters[traceID] = append(w.waiters[traceID], ch)
	w.mu.Unlock()
	return ch
}

// Unregister removes a specific channel from the waiters for the given traceID.
func (w *ResultWaiter) Unregister(traceID string, ch <-chan *model.TaskResult) {
	w.mu.Lock()
	defer w.mu.Unlock()

	chs := w.waiters[traceID]
	for i, c := range chs {
		if c == ch {
			w.waiters[traceID] = append(chs[:i], chs[i+1:]...)
			if len(w.waiters[traceID]) == 0 {
				delete(w.waiters, traceID)
			}
			break
		}
	}
}

// Notify delivers a result to all waiters for the given traceID.
// The channel is kept alive so a single Register call can receive
// multiple notifications (e.g. retriable failures from different nodes).
func (w *ResultWaiter) Notify(traceID string, result *model.TaskResult) {
	w.mu.Lock()
	chs := w.waiters[traceID]
	w.mu.Unlock()

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

// NodeInfo holds the last reported status for a connected node.
type NodeInfo struct {
	model.NodeStatus
	ReportedAt time.Time `json:"reported_at"`
}

// Hub maintains the set of active WebSocket clients and
// assigns tasks to specific nodes.
type Hub struct {
	mu           sync.RWMutex
	clients      map[string]*Client // nodeID → Client
	resultWaiter *ResultWaiter
}

// NewHub creates a new Hub.
func NewHub(resultWaiter *ResultWaiter) *Hub {
	return &Hub{
		clients:      make(map[string]*Client),
		resultWaiter: resultWaiter,
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

// NodeInfoSnapshot returns the last reported status for all connected nodes.
func (h *Hub) NodeInfoSnapshot() map[string]NodeInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	snap := make(map[string]NodeInfo, len(h.clients))
	for id, c := range h.clients {
		s, at := c.StatusSnapshot()
		snap[id] = NodeInfo{NodeStatus: s, ReportedAt: at}
	}
	return snap
}

// nodeSnapshot is a lightweight snapshot used for candidate selection.
// The rnd field enables efficient in-place randomization during sort:
// nodes with the same GPCostWillingness are randomly ordered based on rnd values.
type nodeSnapshot struct {
	NodeID            string
	GPCostWillingness int
	rnd               int // random value for tie-breaking (randomizes nodes with same willingness)
}

// SelectCandidates returns an ordered list of candidate node IDs for task assignment.
//
// For free-tier tasks: nodes with free quota come first (shuffled), followed by
// GP-capable nodes sorted by willingness (descending).
//
// For GP tasks: only GP-capable nodes, sorted by willingness (descending).
// Nodes with the same willingness are shuffled within their tier.
func (h *Hub) SelectCandidates(freeTier bool, estimatedGP int) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var freeNodes []string
	var gpCandidates []nodeSnapshot

	for _, c := range h.clients {
		s, _ := c.StatusSnapshot()

		if freeTier && s.HaveFreeQuota {
			freeNodes = append(freeNodes, c.NodeID)
			continue
		}

		if s.GPBalance >= estimatedGP {
			gpCandidates = append(gpCandidates, nodeSnapshot{
				NodeID:            c.NodeID,
				GPCostWillingness: s.GPCostWillingness,
				rnd:               rand.Int(),
			})
		}
	}

	slices.SortFunc(gpCandidates, func(a, b nodeSnapshot) int {
		if c := cmp.Compare(b.GPCostWillingness, a.GPCostWillingness); c != 0 {
			return c
		}
		return cmp.Compare(a.rnd, b.rnd)
	})

	gpNodes := make([]string, len(gpCandidates))
	for i, c := range gpCandidates {
		gpNodes[i] = c.NodeID
	}

	rand.Shuffle(len(freeNodes), func(i, j int) {
		freeNodes[i], freeNodes[j] = freeNodes[j], freeNodes[i]
	})

	return append(freeNodes, gpNodes...)
}

// AssignTask sends a TASK_ASSIGNMENT message to a specific node.
func (h *Hub) AssignTask(nodeID string, assignment *model.TaskAssignment) error {
	env := model.Envelope{
		Type:    model.MsgTypeTaskAssignment,
		Payload: assignment,
	}
	data, err := json.Marshal(env)
	if err != nil {
		log.Printf("[hub] marshal assignment error: %v", err)
		return fmt.Errorf("marshal assignment: %w", err)
	}

	h.mu.RLock()
	c, ok := h.clients[nodeID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("node %s not connected", nodeID)
	}

	select {
	case c.send <- data:
		log.Printf("[hub] assigned task trace=%s to node=%s", assignment.TraceID, nodeID)
		return nil
	default:
		log.Printf("[hub] send buffer full for node %s, dropping assignment", nodeID)
		return fmt.Errorf("node %s send buffer full", nodeID)
	}
}

// HandleTaskResult delivers a TASK_RESULT from a worker node to the waiting HTTP handler.
func (h *Hub) HandleTaskResult(ctx context.Context, result *model.TaskResult) {
	log.Printf("[hub] received result for trace=%s from node=%s success=%v",
		result.TraceID, result.NodeID, result.Success)
	h.resultWaiter.Notify(result.TraceID, result)
}
