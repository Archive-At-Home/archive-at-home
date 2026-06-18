package model

// MsgType represents the WebSocket message type
type MsgType string

const (
	// Server → Node
	MsgTypeTaskAssignment MsgType = "TASK_ASSIGNMENT"

	// Node → Server
	MsgTypeTaskResult MsgType = "TASK_RESULT"
	MsgTypeNodeStatus MsgType = "NODE_STATUS"
)

// Envelope is the top-level WebSocket frame
type Envelope struct {
	Type    MsgType `json:"type"`
	Payload any     `json:"payload"`
}

// TaskAssignment is sent by the server to assign a task to this node.
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
	Retriable  bool   `json:"retriable,omitempty"` // true = server may retry with another node
	ActualGP   int    `json:"actual_gp"`
	ArchiveURL string `json:"archive_url,omitempty"`
	Error      string `json:"error,omitempty"`
}

// NodeStatus is periodically reported by a node to the server.
type NodeStatus struct {
	HaveFreeQuota     bool `json:"have_free_quota"`
	GPBalance         int  `json:"gp_balance"`
	GPCostWillingness int  `json:"gp_cost_willingness"` // 1-5, only used for GP task ordering
}
