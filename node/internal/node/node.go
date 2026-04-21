package node

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/node/internal/dashboard"
	"github.com/Archive-At-Home/archive-at-home/node/internal/ehentai"
	"github.com/Archive-At-Home/archive-at-home/node/internal/model"
	"github.com/Archive-At-Home/archive-at-home/node/internal/ws"
)

const (
	// Reserve 1000 GP to ensure successful download
	GPReserve = 1000

	// Task queue buffer size
	TaskQueueSize = 100

	// Number of concurrent task processors
	WorkerCount = 5

	// Status refresh interval
	StatusRefreshInterval = 5 * time.Minute

	// Daily reset interval
	DailyResetInterval = 24 * time.Hour

	// Delay for non-free quota nodes claiming free tier tasks
	FreeTierClaimDelay = 2 * time.Second
)

// Node represents a worker node
type Node struct {
	nodeID         string
	signature      string
	serverURL      string
	ehClient       *ehentai.Client
	maxGPCost      int
	wsClient       *ws.Client
	taskQueue      chan *model.TaskAssignment
	wg             sync.WaitGroup
	dashboard      *dashboard.Dashboard
	baseBalanceGP  int           // Base balance for delay calculation
	baseClaimDelay time.Duration // Base claim delay for low balance nodes
}

// NewNode creates a new worker node
func NewNode(nodeID, signature, serverURL string, ehClient *ehentai.Client, maxGPCost, baseBalanceGP, baseClaimDelaySec int) *Node {
	return &Node{
		nodeID:         nodeID,
		signature:      signature,
		serverURL:      serverURL,
		ehClient:       ehClient,
		maxGPCost:      maxGPCost,
		taskQueue:      make(chan *model.TaskAssignment, TaskQueueSize),
		dashboard:      dashboard.NewDashboard(nodeID, serverURL, maxGPCost),
		baseBalanceGP:  baseBalanceGP,
		baseClaimDelay: time.Duration(baseClaimDelaySec) * time.Second,
	}
}

// Start connects to the server and starts processing tasks
func (n *Node) Start(ctx context.Context, dashboardAddr string) error {
	// Create WebSocket client with the lifecycle context
	n.wsClient = ws.NewClient(ctx, n.serverURL, n.nodeID, n.signature, n)

	// Load historical stats from database
	if stats, err := n.ehClient.GetHistoricalStats(); err != nil {
		log.Printf("[node] failed to load historical stats: %v", err)
	} else {
		n.dashboard.LoadHistoricalStats(stats.TotalTasks, stats.TotalGP, stats.TotalSizeMiB, stats.TodayTasks)
		log.Printf("[node] loaded historical stats: %d tasks, %d GP, %.1f MiB",
			stats.TotalTasks, stats.TotalGP, stats.TotalSizeMiB)
	}

	// Setup dashboard callbacks
	n.dashboard.SetReconnectFunc(func() error {
		return n.wsClient.Reconnect()
	})
	n.dashboard.SetRefreshFunc(func() error {
		return n.ehClient.RefreshStatus()
	})

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

	// Start daily GP cost reset (every 24 hours)
	n.wg.Add(1)
	go n.dailyResetLoop(ctx)

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

// OnTaskAnnouncement handles incoming task announcements
func (n *Node) OnTaskAnnouncement(ctx context.Context, ann *model.TaskAnnouncement) {
	n.logf("received task announcement: trace=%s, freeTier=%v, estimatedGP=%d",
		ann.TraceID, ann.FreeTier, ann.EstimatedGP)

	// Check if we should claim this task
	shouldClaim, delay := n.shouldClaimTask(ann)

	if !shouldClaim {
		n.logf("skipping task %s (insufficient balance)", ann.TraceID)
		return
	}

	// Schedule the fetch request with delay
	go func() {
		if delay > 0 {
			n.logf("waiting %v before claiming task %s", delay, ann.TraceID)
			time.Sleep(delay)
		}

		n.logf("attempting to claim task %s", ann.TraceID)
		if err := n.wsClient.SendFetchTask(ann.TraceID); err != nil {
			n.logf("failed to send fetch task: %v", err)
		}
	}()
}

// OnTaskAssigned handles task assignment from server
func (n *Node) OnTaskAssigned(ctx context.Context, task *model.TaskAssignment) {
	n.logf("task assigned: trace=%s, gallery=%s", task.TraceID, task.GalleryID)

	// Queue the task for async processing
	select {
	case n.taskQueue <- task:
	default:
		n.logf("task queue full, dropping task %s", task.TraceID)
	}
}

// OnTaskGone handles task gone notification (already claimed by another node)
func (n *Node) OnTaskGone(ctx context.Context, traceID string) {
	n.logf("task gone: trace=%s", traceID)
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
// Task Claiming Strategy
// ─────────────────────────────────────────────

func (n *Node) shouldClaimTask(ann *model.TaskAnnouncement) (bool, time.Duration) {
	haveFree, gpBalance := n.ehClient.GetStatus()

	// 情况 A：有免费额度的免费任务
	if ann.FreeTier && haveFree {
		return true, 0
	}

	// 情况 B：余额充足（满足预留金）
	if gpBalance >= ann.EstimatedGP+GPReserve {
		if ann.FreeTier {
			// 免费任务但没有免费额度，固定长延迟以减少资源浪费
			return true, FreeTierClaimDelay
		}
		// 付费任务，使用基于余额的动态延迟
		return true, n.calculateBalanceBasedDelay(gpBalance)
	}

	return false, 0
}

// calculateBalanceBasedDelay 根据余额计算动态延迟
// 余额越高，延迟越小；余额越低（但仍满足阈值），延迟越大
// 使用平方函数实现非线性延迟：高余额时延迟增长缓慢，低余额时延迟快速增大
func (n *Node) calculateBalanceBasedDelay(currentBalance int) time.Duration {
	// 余额高于基准值，不延迟
	if currentBalance >= n.baseBalanceGP {
		return 0
	}

	// 余额在预留金和基准值之间，使用平方函数计算延迟
	// delayRatio = ((baseBalanceGP - currentBalance) / (baseBalanceGP - GPReserve))^2
	linearRatio := float64(n.baseBalanceGP-currentBalance) / float64(n.baseBalanceGP-GPReserve)
	delayRatio := linearRatio * linearRatio // 平方函数
	delay := time.Duration(float64(n.baseClaimDelay) * delayRatio)

	return delay
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

	if err != nil {
		n.logf("task %s failed: %v", task.TraceID, err)
		result.Success = false
		result.Error = err.Error()
		n.dashboard.RecordTaskFailed()
	} else {
		n.logf("task %s completed: archiveURL=%s, actualGP=%d, size=%.1fMiB",
			task.TraceID, archiveURL, actualGP, sizeMiB)
		result.Success = true
		result.ArchiveURL = archiveURL
		n.dashboard.RecordTaskCompleted(actualGP, sizeMiB)
	}

	// Send result to server
	if err := n.wsClient.SendTaskResult(result); err != nil {
		n.logf("failed to send task result: %v", err)
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

func (n *Node) dailyResetLoop(ctx context.Context) {
	defer n.wg.Done()

	ticker := time.NewTicker(DailyResetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.ehClient.ResetDailyGPCost()
			n.logf("daily GP cost reset")
		}
	}
}

// ─────────────────────────────────────────────
// Helper functions
// ─────────────────────────────────────────────

// logf logs a message with the [node] prefix
func (n *Node) logf(format string, args ...interface{}) {
	log.Printf("[node] "+format, args...)
}

// refreshAndLogStatus refreshes status and logs the result
func (n *Node) refreshAndLogStatus(context string) {
	if err := n.ehClient.RefreshStatus(); err != nil {
		n.logf("%s status refresh failed: %v", context, err)
	} else {
		haveFree, gpBalance := n.ehClient.GetStatus()
		todayGPCost := n.ehClient.GetTodayGPCost()
		n.logf("status %s: haveFreeQuota=%v, gpBalance=%d", context, haveFree, gpBalance)

		// Update dashboard
		n.dashboard.UpdateGPStatus(haveFree, gpBalance, todayGPCost)
	}
}
