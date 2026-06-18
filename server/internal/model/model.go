package model

import "time"

// ─────────────────────────────────────────────
// Redis Key Builders
// ─────────────────────────────────────────────

// CacheKey builds the per-user cache key: "cache:{UserID}:{GalleryID}"
func CacheKey(userID, galleryID string) string {
	return "cache:" + userID + ":" + galleryID
}

// CollapsingKey builds the request collapsing key: "inflight:{UserID}:{GalleryID}"
func CollapsingKey(userID, galleryID string) string {
	return "inflight:" + userID + ":" + galleryID
}

// ─────────────────────────────────────────────
// WebSocket Protocol Messages
// ─────────────────────────────────────────────

type MsgType string

const (
	// Server → Node
	MsgTypeTaskAssignment MsgType = "TASK_ASSIGNMENT"

	// Node → Server
	MsgTypeTaskResult MsgType = "TASK_RESULT"
	MsgTypeNodeStatus MsgType = "NODE_STATUS"
)

// Envelope is the top-level WebSocket frame.
type Envelope struct {
	Type    MsgType `json:"type"`
	Payload any     `json:"payload"`
}

// TaskAssignment is sent by the server to assign a task to a specific node.
type TaskAssignment struct {
	TraceID    string `json:"trace_id"`
	GalleryID  string `json:"gallery_id"`
	GalleryKey string `json:"gallery_key"`
}

// TaskResult is submitted by a node after completing a parse.
type TaskResult struct {
	TraceID    string `json:"trace_id"`
	NodeID     string `json:"node_id"`
	Success    bool   `json:"success"`
	Retriable  bool   `json:"retriable,omitempty"` // true = intermediate failure, retry in progress
	ActualGP   int    `json:"actual_gp"`           // actual GP consumed during parsing
	ArchiveURL string `json:"archive_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

// NodeStatus is periodically reported by a node to the server.
type NodeStatus struct {
	HaveFreeQuota     bool `json:"have_free_quota"`
	GPBalance         int  `json:"gp_balance"`
	GPCostWillingness int  `json:"gp_cost_willingness"` // 1-5, only used for GP task ordering
}

// ─────────────────────────────────────────────
// SQL Persistence Models (async write)
// ─────────────────────────────────────────────

// TaskLog records every task lifecycle event (one record per task).
type TaskLog struct {
	TraceID       string    `gorm:"primaryKey" json:"trace_id"`
	UserID        string    `gorm:"index" json:"user_id"`
	Client        string    `json:"client"`
	GalleryID     string    `json:"gallery_id"`
	GalleryKey    string    `json:"gallery_key"`
	NodeID        string    `json:"node_id"`
	Success       bool      `json:"success"`
	FailureReason string    `json:"failure_reason"`
	Force         bool      `json:"force"`
	FreeTier      bool      `json:"free_tier"`
	EstimatedGP   int       `json:"estimated_gp"`
	ActualGP      int       `json:"actual_gp"`
	FinishedAt    time.Time `json:"finished_at"`
}

// ─────────────────────────────────────────────
// HTTP Request / Response
// ─────────────────────────────────────────────

// ParseRequest is the inbound API request.
// UserID is NOT included here – it is extracted from the API key in the middleware.
type ParseRequest struct {
	GalleryID  string `json:"gallery_id" binding:"required"`
	GalleryKey string `json:"gallery_key" binding:"required"` // e-hentai gallery token/key
	Force      bool   `json:"force"`
}

// ParseResponse is the outbound API response.
type ParseResponse struct {
	Cached     bool   `json:"cached,omitempty"`
	GPCost     int    `json:"gp_cost,omitempty"` // GP cost (from EstimatedGP)
	ArchiveURL string `json:"archive_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

// UserProfile represents user profile with balance information.
// Used by both /api/v1/me and /api/v1/admin/users/:id endpoints.
type UserProfile struct {
	User    any   `json:"user"`    // *auth.User
	Balance int64 `json:"balance"` // Available balance (balance - frozen)
}
