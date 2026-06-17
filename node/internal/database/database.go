package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var sizePattern = regexp.MustCompile(`^([\d,.]+)\s*(KiB|MiB|GiB)$`)

// ParseLog represents a parse log entry
type ParseLog struct {
	ID            string
	GID           string
	Token         string
	ActualGP      int
	EstimatedSize float64 // Size in MiB
	Success       bool
	ErrorMessage  string
	CreatedAt     time.Time
}

// DB wraps the SQLite database
type DB struct {
	conn *sql.DB
}

// NewDB creates a new database connection and initializes the schema
func NewDB(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory failed: %w", err)
	}

	// Open database connection with proper parameters
	// Use WAL mode for better concurrency
	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database failed: %w", err)
	}

	// Configure connection pool for thread safety
	// SQLite works best with limited connections
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("init schema failed: %w", err)
	}

	return db, nil
}

// initSchema creates the necessary tables
func (db *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS parse_logs (
		id TEXT PRIMARY KEY,
		gid TEXT NOT NULL,
		token TEXT NOT NULL,
		actual_gp INTEGER NOT NULL,
		estimated_size REAL,
		success INTEGER NOT NULL DEFAULT 1,
		error_message TEXT,
		created_at INTEGER NOT NULL DEFAULT (unixepoch())
	);

	CREATE INDEX IF NOT EXISTS idx_created_at ON parse_logs(created_at);

	-- Drop legacy gid index from older schema versions.
	DROP INDEX IF EXISTS idx_gid;
	`

	_, err := db.conn.Exec(schema)
	return err
}

// InsertParseLog inserts a new parse log entry
func (db *DB) InsertParseLog(log *ParseLog) error {
	if log.ID == "" {
		return fmt.Errorf("parse log id is required")
	}

	success := 0
	if log.Success {
		success = 1
	}
	errorMessage := sql.NullString{String: log.ErrorMessage, Valid: !log.Success}

	_, err := db.conn.Exec(
		`INSERT INTO parse_logs (id, gid, token, actual_gp, estimated_size, success, error_message, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, log.GID, log.Token, log.ActualGP, log.EstimatedSize, success, errorMessage, log.CreatedAt.Unix(),
	)
	return err
}

// ParseSizeToMiB parses size string (e.g., "59.77 MiB", "1.5 GiB", "512.3 KiB") to MiB
func ParseSizeToMiB(sizeStr string) (float64, error) {
	// Match pattern like "59.77 MiB" or "1.5 GiB"
	matches := sizePattern.FindStringSubmatch(sizeStr)
	if len(matches) < 3 {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	valueText := strings.ReplaceAll(matches[1], ",", "")
	value, err := strconv.ParseFloat(valueText, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size value failed: %w", err)
	}

	unit := matches[2]
	switch unit {
	case "KiB":
		return value / 1024, nil
	case "MiB":
		return value, nil
	case "GiB":
		return value * 1024, nil
	default:
		return 0, fmt.Errorf("unknown size unit: %s", unit)
	}
}

// AggregateStats holds aggregate statistics from the database
type AggregateStats struct {
	TotalTasks           int
	TotalGP              int
	TotalSizeMiB         float64
	TotalFailed          int
	TodayGP              int
	TodayTasks           int
	SevenDayFreeQuotaMiB float64
}

// GetAggregateStats returns aggregate statistics from all parse logs.
// TodayGP/TodayTasks cover the rolling 24h window.
func (db *DB) GetAggregateStats() (*AggregateStats, error) {
	stats := &AggregateStats{}
	cutoff24h := time.Now().Add(-24 * time.Hour).Unix()
	cutoff7d := time.Now().Add(-7 * 24 * time.Hour).Unix()
	err := db.conn.QueryRow(`
		SELECT
			COUNT(*) FILTER (WHERE success = 1),
			COALESCE(SUM(actual_gp)      FILTER (WHERE success = 1), 0),
			COALESCE(SUM(estimated_size) FILTER (WHERE success = 1), 0),
			COUNT(*) FILTER (WHERE success = 0),
			COALESCE(SUM(actual_gp) FILTER (WHERE success = 1 AND created_at >= ?), 0),
			COUNT(*)                FILTER (WHERE success = 1 AND created_at >= ?),
			COALESCE(SUM(estimated_size) FILTER (WHERE actual_gp = 0 AND success = 1 AND created_at >= ?), 0)
		FROM parse_logs
	`, cutoff24h, cutoff24h, cutoff7d).Scan(&stats.TotalTasks, &stats.TotalGP, &stats.TotalSizeMiB,
		&stats.TotalFailed, &stats.TodayGP, &stats.TodayTasks, &stats.SevenDayFreeQuotaMiB)
	if err != nil {
		return nil, fmt.Errorf("query aggregate stats: %w", err)
	}
	return stats, nil
}

// GetRecentGPCost returns the total GP cost in the rolling 24h window.
func (db *DB) GetRecentGPCost() (int, error) {
	var recentGP int
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	err := db.conn.QueryRow(`
		SELECT COALESCE(SUM(actual_gp), 0)
		FROM parse_logs
		WHERE success = 1 AND created_at >= ?
	`, cutoff).Scan(&recentGP)
	if err != nil {
		return 0, fmt.Errorf("query recent gp cost: %w", err)
	}
	return recentGP, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.conn.Close()
}
