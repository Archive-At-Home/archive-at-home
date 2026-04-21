package model

// MsgType represents the WebSocket message type
type MsgType string

const (
	// Server → Node
	MsgTypeTaskAnnouncement MsgType = "TASK_ANNOUNCEMENT"

	// Node → Server
	MsgTypeFetchTask  MsgType = "FETCH_TASK"
	MsgTypeTaskResult MsgType = "TASK_RESULT"

	// Server → Node (response to FETCH)
	MsgTypeTaskAssigned MsgType = "TASK_ASSIGNED"
	MsgTypeTaskGone     MsgType = "TASK_GONE"
)

// Envelope is the top-level WebSocket frame
type Envelope struct {
	Type    MsgType     `json:"type"`
	Payload interface{} `json:"payload"`
}

// TaskAnnouncement is broadcast by server when a new task is available
type TaskAnnouncement struct {
	TraceID     string `json:"trace_id"`
	FreeTier    bool   `json:"free_tier"`
	EstimatedGP int    `json:"estimated_gp"`
	QueueLen    int    `json:"queue_len"`
}

// FetchTaskRequest is sent by node to claim a task
type FetchTaskRequest struct {
	TraceID string `json:"trace_id"`
	NodeID  string `json:"node_id"`
}

// TaskAssignment is the response when node successfully claims a task
type TaskAssignment struct {
	TraceID    string `json:"trace_id"`
	GalleryID  string `json:"gallery_id"`
	GalleryKey string `json:"gallery_key"`
}

// TaskResult is submitted by node after completing a parse
type TaskResult struct {
	TraceID    string `json:"trace_id"`
	NodeID     string `json:"node_id"`
	Success    bool   `json:"success"`
	ActualGP   int    `json:"actual_gp"`
	ArchiveURL string `json:"archive_url,omitempty"`
	Error      string `json:"error,omitempty"`
}
