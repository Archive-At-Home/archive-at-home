package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/node/internal/dashboard"
	"github.com/Archive-At-Home/archive-at-home/node/internal/database"
	"github.com/Archive-At-Home/archive-at-home/node/internal/ehentai"
	"github.com/Archive-At-Home/archive-at-home/node/internal/model"
	"github.com/Archive-At-Home/archive-at-home/node/internal/ws"
)

const (
	// Task queue buffer size
	TaskQueueSize = 10

	// Number of concurrent task processors
	WorkerCount = 5

	// Status refresh interval
	StatusRefreshInterval = 5 * time.Minute
)

// Node represents a worker node
type Node struct {
	nodeID            string
	signature         string
	serverURL         string
	ehClient          *ehentai.Client
	db                *database.DB
	wsClient          *ws.Client
	taskQueue         chan *model.TaskAssignment
	wg                sync.WaitGroup
	dashboard         *dashboard.Dashboard
	gpCostWillingness int           // Contributor-defined willingness to spend GP (1-5)
	FatalCh           chan struct{} // Closed when a fatal error (e.g. igneous revoked) occurs
}

// NewNode creates a new worker node
func NewNode(nodeID, signature, serverURL string, ehClient *ehentai.Client, db *database.DB, maxGPCost, gpCostWillingness int) *Node {
	return &Node{
		nodeID:            nodeID,
		signature:         signature,
		serverURL:         serverURL,
		ehClient:          ehClient,
		db:                db,
		taskQueue:         make(chan *model.TaskAssignment, TaskQueueSize),
		dashboard:         dashboard.NewDashboard(nodeID, serverURL, maxGPCost),
		gpCostWillingness: gpCostWillingness,
		FatalCh:           make(chan struct{}),
	}
}

// Start connects to the server and starts processing tasks
func (n *Node) Start(ctx context.Context, dashboardAddr string) error {
	// Create WebSocket client with the lifecycle context
	n.wsClient = ws.NewClient(ctx, n.serverURL, n.nodeID, n.signature, n)

	// Setup dashboard callbacks
	n.dashboard.Configure(
		n.wsClient.Reconnect,
		func() error { return n.refreshAndLogStatus("manual") },
		n.db.GetAggregateStats,
		n.ehClient.GetStatus,
	)

	// Start dashboard server if enabled
	if dashboardAddr != "" {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			if err := n.dashboard.ServeHTTP(ctx, dashboardAddr); err != nil {
				log.Printf("[node] dashboard server error: %v", err)
			}
		}()
	}

	// Connect to WebSocket server
	if err := n.wsClient.Connect(); err != nil {
		return fmt.Errorf("websocket connect failed: %w", err)
	}

	// Start periodic status refresh (every 5 minutes)
	n.wg.Add(1)
	go n.statusRefreshLoop(ctx)

	// Start task processor workers
	for range WorkerCount {
		n.wg.Add(1)
		go n.taskProcessor(ctx)
	}

	// Initial status refresh
	n.refreshAndLogStatus("initial")

	return nil
}

// Stop gracefully shuts down the node
func (n *Node) Stop() error {
	close(n.taskQueue)
	n.wg.Wait()
	return n.wsClient.Close()
}

// ─────────────────────────────────────────────
// WebSocket Message Handlers (implements ws.MessageHandler)
// ─────────────────────────────────────────────

// OnTaskAssignment handles direct task assignment from server.
func (n *Node) OnTaskAssignment(ctx context.Context, task *model.TaskAssignment) {
	n.logf("task assigned: trace=%s, gallery=%s", task.TraceID, task.GalleryID)

	// Queue the task for async processing
	select {
	case n.taskQueue <- task:
	default:
		n.logf("task queue full, dropping task %s", task.TraceID)
	}
}

// OnConnected handles WebSocket connection established
func (n *Node) OnConnected() {
	n.logf("connected to server")
	n.dashboard.UpdateConnectionStatus(true)
}

// OnDisconnected handles WebSocket disconnection
func (n *Node) OnDisconnected() {
	n.logf("disconnected from server")
	n.dashboard.UpdateConnectionStatus(false)
}

// ─────────────────────────────────────────────
// Task Processing
// ─────────────────────────────────────────────

func (n *Node) taskProcessor(ctx context.Context) {
	defer n.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return

		case task, ok := <-n.taskQueue:
			if !ok {
				return
			}

			n.processTask(task)
		}
	}
}

func (n *Node) processTask(task *model.TaskAssignment) {
	n.logf("processing task %s (gallery=%s)", task.TraceID, task.GalleryID)

	// Download the archive
	archiveURL, actualGP, sizeMiB, err := n.ehClient.GetArchiveURL(task.GalleryID, task.GalleryKey)

	result := &model.TaskResult{
		TraceID:  task.TraceID,
		ActualGP: actualGP,
	}

	fatal := false
	if err != nil {
		n.logf("task %s failed: %v", task.TraceID, err)
		result.Success = false
		result.Error = err.Error()

		if errors.Is(err, ehentai.ErrCopyrightRestriction) {
			result.Retriable = false
		} else {
			result.Retriable = true
		}

		if errors.Is(err, ehentai.ErrIgneousRevoked) {
			fatal = true
		}
	} else {
		n.logf("task %s completed: archiveURL=%s, actualGP=%d, size=%.1fMiB",
			task.TraceID, archiveURL, actualGP, sizeMiB)
		result.Success = true
		result.ArchiveURL = archiveURL
	}

	if logErr := n.db.InsertParseLog(&database.ParseLog{
		ID:            task.TraceID,
		GID:           task.GalleryID,
		Token:         task.GalleryKey,
		ActualGP:      actualGP,
		EstimatedSize: sizeMiB,
		Success:       result.Success,
		ErrorMessage:  result.Error,
		CreatedAt:     time.Now(),
	}); logErr != nil {
		n.logf("failed to record task log: %v", logErr)
	}

	// Send result to server
	if err := n.wsClient.SendTaskResult(result); err != nil {
		n.logf("failed to send task result: %v", err)
	}

	if fatal {
		n.logf("exhentai access revoked, initiating shutdown")
		close(n.FatalCh)
		return
	}

	// Refresh status after task completion
	n.refreshAndLogStatus("updated")
}

// ─────────────────────────────────────────────
// Background Jobs
// ─────────────────────────────────────────────

func (n *Node) statusRefreshLoop(ctx context.Context) {
	defer n.wg.Done()

	ticker := time.NewTicker(StatusRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.refreshAndLogStatus("refreshed")
		}
	}
}

// ─────────────────────────────────────────────
// Helper functions
// ─────────────────────────────────────────────

// logf logs a message with the [node] prefix
func (n *Node) logf(format string, args ...any) {
	log.Printf("[node] "+format, args...)
}

// refreshAndLogStatus refreshes EH status and reports it to the server.
func (n *Node) refreshAndLogStatus(label string) error {
	if err := n.ehClient.RefreshStatus(); err != nil {
		n.logf("%s status refresh failed: %v", label, err)
		return err
	}
	haveFree, gpBalance := n.ehClient.GetStatus()
	n.logf("status %s: haveFreeQuota=%v, gpBalance=%d, willingness=%d", label, haveFree, gpBalance, n.gpCostWillingness)
	if err := n.wsClient.SendNodeStatus(haveFree, gpBalance, n.gpCostWillingness); err != nil {
		n.logf("failed to send node status: %v", err)
	}
	return nil
}
