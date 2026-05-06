package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/node/internal/database"
)

//go:embed templates/*
var templates embed.FS

// Stats holds the node statistics (pure data, no mutex)
type Stats struct {
	// Connection status
	Connected      bool      `json:"connected"`
	ConnectedSince time.Time `json:"connectedSince,omitempty"`
	LastDisconnect time.Time `json:"lastDisconnect,omitempty"`

	// Task statistics
	TodayTasksCompleted int `json:"todayTasksCompleted"`
	TasksCompleted      int `json:"tasksCompleted"`
	TasksFailed         int `json:"tasksFailed"`

	// GP statistics
	GPBalance     int     `json:"gpBalance"`
	HaveFreeQuota bool    `json:"haveFreeQuota"`
	TodayGPCost   int     `json:"todayGPCost"`
	TotalGPCost   int     `json:"totalGPCost"`
	MaxGPCost     int     `json:"maxGPCost"`
	TotalSizeMiB  float64 `json:"totalSizeMiB"`
	AvgGPPerTask  float64 `json:"avgGPPerTask"`
	AvgSizeMiB    float64 `json:"avgSizeMiB"`

	// Session info
	NodeID    string    `json:"nodeId"`
	StartTime time.Time `json:"startTime"`
	ServerURL string    `json:"serverUrl"`
}

// Dashboard manages the node dashboard
type Dashboard struct {
	mu                     sync.RWMutex
	stats                  Stats
	aggregateStatsProvider func() (*database.AggregateStats, error)
	nodeStatusProvider     func() (bool, int)
	reconnectFunc          func() error
	refreshFunc            func() error
}

// NewDashboard creates a new dashboard instance
func NewDashboard(nodeID, serverURL string, maxGPCost int) *Dashboard {
	return &Dashboard{
		stats: Stats{
			NodeID:    nodeID,
			ServerURL: serverURL,
			StartTime: time.Now(),
			MaxGPCost: maxGPCost,
		},
	}
}

// Configure wires the dashboard callbacks and data providers.
func (d *Dashboard) Configure(
	reconnectFunc func() error,
	refreshFunc func() error,
	aggregateStatsProvider func() (*database.AggregateStats, error),
	nodeStatusProvider func() (bool, int),
) {
	d.reconnectFunc = reconnectFunc
	d.refreshFunc = refreshFunc
	d.aggregateStatsProvider = aggregateStatsProvider
	d.nodeStatusProvider = nodeStatusProvider
}

// UpdateConnectionStatus updates the connection status
func (d *Dashboard) UpdateConnectionStatus(connected bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.Connected = connected
	if connected {
		d.stats.ConnectedSince = time.Now()
	} else {
		d.stats.LastDisconnect = time.Now()
	}
}

// GetStats returns a copy of the current stats
func (d *Dashboard) GetStats() Stats {
	d.mu.RLock()
	stats := d.stats
	aggregateStatsProvider := d.aggregateStatsProvider
	nodeStatusProvider := d.nodeStatusProvider
	d.mu.RUnlock()

	if nodeStatusProvider != nil {
		haveFree, gpBalance := nodeStatusProvider()
		stats.HaveFreeQuota = haveFree
		stats.GPBalance = gpBalance
	}

	if aggregateStatsProvider != nil {
		agg, err := aggregateStatsProvider()
		if err != nil {
			log.Printf("[dashboard] Failed to load aggregate stats: %v", err)
		} else {
			stats.TodayTasksCompleted = agg.TodayTasks
			stats.TasksCompleted = agg.TotalTasks
			stats.TasksFailed = agg.TotalFailed
			stats.TodayGPCost = agg.TodayGP
			stats.TotalGPCost = agg.TotalGP
			stats.TotalSizeMiB = agg.TotalSizeMiB

			if agg.TotalTasks > 0 {
				stats.AvgGPPerTask = float64(agg.TotalGP) / float64(agg.TotalTasks)
				stats.AvgSizeMiB = agg.TotalSizeMiB / float64(agg.TotalTasks)
			} else {
				stats.AvgGPPerTask = 0
				stats.AvgSizeMiB = 0
			}
		}
	}

	return stats
}

// ServeHTTP starts the HTTP dashboard server. It shuts down gracefully when ctx is cancelled.
func (d *Dashboard) ServeHTTP(ctx context.Context, addr string) error {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/stats", d.handleStats)
	mux.HandleFunc("/api/reconnect", d.handleReconnect)
	mux.HandleFunc("/api/refresh", d.handleRefresh)

	// Static files (CSS, JS)
	staticFS, err := fs.Sub(templates, "templates")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Index page
	mux.HandleFunc("/", d.handleIndex)

	server := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Printf("[dashboard] Starting dashboard server on %s", addr)
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// handleStats returns the current statistics as JSON
func (d *Dashboard) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stats := d.GetStats()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if err := json.NewEncoder(w).Encode(stats); err != nil {
		log.Printf("[dashboard] Failed to encode stats: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
}

// handleReconnect triggers a reconnection
func (d *Dashboard) handleReconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if d.reconnectFunc == nil {
		http.Error(w, "Reconnect function not configured", http.StatusInternalServerError)
		return
	}

	log.Printf("[dashboard] Manual reconnect requested")
	if err := d.reconnectFunc(); err != nil {
		log.Printf("[dashboard] Reconnect failed: %v", err)
		http.Error(w, fmt.Sprintf("Reconnect failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "重连成功"})
}

// handleRefresh triggers a status refresh
func (d *Dashboard) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if d.refreshFunc == nil {
		http.Error(w, "Refresh function not configured", http.StatusInternalServerError)
		return
	}

	log.Printf("[dashboard] Manual status refresh requested")
	if err := d.refreshFunc(); err != nil {
		log.Printf("[dashboard] Status refresh failed: %v", err)
		http.Error(w, fmt.Sprintf("Status refresh failed: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": "刷新成功"})
}

// handleIndex serves the dashboard HTML page
func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := templates.ReadFile("templates/index.html")
	if err != nil {
		log.Printf("[dashboard] Failed to read index.html: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}
