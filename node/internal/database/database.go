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

// ParseLog represents a parse log entry
type ParseLog struct {
	ID            int64
	GID           string
	Token         string
	ActualGP      int
	EstimatedSize float64 // Size in MiB
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
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		gid TEXT NOT NULL,
		token TEXT NOT NULL,
		actual_gp INTEGER NOT NULL,
		estimated_size REAL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_gid ON parse_logs(gid);
	CREATE INDEX IF NOT EXISTS idx_created_at ON parse_logs(created_at);
	`

	_, err := db.conn.Exec(schema)
	return err
}

// InsertParseLog inserts a new parse log entry
func (db *DB) InsertParseLog(log *ParseLog) error {
	query := `
		INSERT INTO parse_logs (gid, token, actual_gp, estimated_size, created_at)
		VALUES (?, ?, ?, ?, ?)
	`

	result, err := db.conn.Exec(query, log.GID, log.Token, log.ActualGP, log.EstimatedSize, log.CreatedAt)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}

	log.ID = id
	return nil
}

// ParseSizeToMiB parses size string (e.g., "59.77 MiB", "1.5 GiB", "512.3 KiB") to MiB
func ParseSizeToMiB(sizeStr string) (float64, error) {
	// Match pattern like "59.77 MiB" or "1.5 GiB"
	re := regexp.MustCompile(`^([\d,.]+)\s*(KiB|MiB|GiB)$`)
	matches := re.FindStringSubmatch(sizeStr)
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
	TotalTasks   int
	TotalGP      int
	TotalSizeMiB float64
	TodayGP      int
	TodayTasks   int
}

// GetAggregateStats returns aggregate statistics from all parse logs
func (db *DB) GetAggregateStats() (*AggregateStats, error) {
	stats := &AggregateStats{}

	err := db.conn.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(actual_gp), 0), COALESCE(SUM(estimated_size), 0)
		FROM parse_logs
	`).Scan(&stats.TotalTasks, &stats.TotalGP, &stats.TotalSizeMiB)
	if err != nil {
		return nil, fmt.Errorf("query total stats: %w", err)
	}

	today := time.Now().Format("2006-01-02")
	err = db.conn.QueryRow(`
		SELECT COALESCE(SUM(actual_gp), 0), COUNT(*)
		FROM parse_logs
		WHERE DATE(created_at) = ?
	`, today).Scan(&stats.TodayGP, &stats.TodayTasks)
	if err != nil {
		return nil, fmt.Errorf("query today stats: %w", err)
	}

	return stats, nil
}

// Close closes the database connection
func (db *DB) Close() error {
	return db.conn.Close()
}
