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
	mu            sync.RWMutex
	stats         Stats
	reconnectFunc func() error
	refreshFunc   func() error
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

// SetReconnectFunc sets the function to call when reconnect is requested
func (d *Dashboard) SetReconnectFunc(f func() error) {
	d.reconnectFunc = f
}

// SetRefreshFunc sets the function to call when status refresh is requested
func (d *Dashboard) SetRefreshFunc(f func() error) {
	d.refreshFunc = f
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

// RecordTaskCompleted records a completed task
func (d *Dashboard) RecordTaskCompleted(gpCost int, sizeMiB float64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.TasksCompleted++
	d.stats.TodayTasksCompleted++
	d.stats.TotalGPCost += gpCost
	d.stats.TotalSizeMiB += sizeMiB

	if d.stats.TasksCompleted > 0 {
		d.stats.AvgGPPerTask = float64(d.stats.TotalGPCost) / float64(d.stats.TasksCompleted)
		d.stats.AvgSizeMiB = d.stats.TotalSizeMiB / float64(d.stats.TasksCompleted)
	}
}

// RecordTaskFailed records a failed task
func (d *Dashboard) RecordTaskFailed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stats.TasksFailed++
}

// UpdateGPStatus updates GP balance and quota status
func (d *Dashboard) UpdateGPStatus(haveFreeQuota bool, gpBalance, todayGPCost int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.HaveFreeQuota = haveFreeQuota
	d.stats.GPBalance = gpBalance
	d.stats.TodayGPCost = todayGPCost
}

// LoadHistoricalStats initializes stats with historical data from the database
func (d *Dashboard) LoadHistoricalStats(totalTasks, totalGP int, totalSizeMiB float64, todayTasks int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.stats.TasksCompleted = totalTasks
	d.stats.TotalGPCost = totalGP
	d.stats.TotalSizeMiB = totalSizeMiB
	d.stats.TodayTasksCompleted = todayTasks

	if totalTasks > 0 {
		d.stats.AvgGPPerTask = float64(totalGP) / float64(totalTasks)
		d.stats.AvgSizeMiB = totalSizeMiB / float64(totalTasks)
	}
}

// GetStats returns a copy of the current stats
func (d *Dashboard) GetStats() Stats {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.stats
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
