package store

import (
	"log"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/balance"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store provides SQL persistence via GORM (async writes).
type Store struct {
	db    *gorm.DB
	logCh chan func() // buffered channel for async writes
}

// NewStore opens the database, auto-migrates schemas, and
// starts background write workers.
func NewStore(dsn string) (*Store, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, err
	}

	// Configure connection pool for PostgreSQL
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}

	// PostgreSQL works well with multiple connections
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Auto-migrate
	if err := db.AutoMigrate(
		&model.TaskLog{},
		&auth.User{},
		&balance.Account{},
		&balance.Transaction{},
	); err != nil {
		return nil, err
	}

	s := &Store{
		db:    db,
		logCh: make(chan func(), 1024),
	}

	// Start async write workers
	go s.writeWorker()

	return s, nil
}

func (s *Store) writeWorker() {
	for fn := range s.logCh {
		fn()
	}
}

// DB returns the underlying GORM database instance.
func (s *Store) DB() *gorm.DB {
	return s.db
}

// ─────────────────────────────────────────────
// Async write helpers
// ─────────────────────────────────────────────

// LogTaskCreated records a new task event.
func (s *Store) LogTaskCreated(traceID, userID, galleryID, galleryKey string, force, freeTier bool, estimatedGP int) {
	s.logCh <- func() {
		tl := model.TaskLog{
			TraceID:     traceID,
			UserID:      userID,
			GalleryID:   galleryID,
			GalleryKey:  galleryKey,
			Status:      model.TaskStatusPending,
			Force:       force,
			FreeTier:    freeTier,
			EstimatedGP: estimatedGP,
			CreatedAt:   time.Now(),
		}
		if err := s.db.Create(&tl).Error; err != nil {
			log.Printf("[store] log task created error: %v", err)
		}
	}
}

// LogTaskCompleted updates the task log.
func (s *Store) LogTaskCompleted(traceID, nodeID string, success bool, actualGP int) {
	s.logCh <- func() {
		now := time.Now()
		status := model.TaskStatusFailed
		if success {
			status = model.TaskStatusCompleted
		}
		// Update task log
		s.db.Model(&model.TaskLog{}).
			Where("trace_id = ?", traceID).
			Updates(map[string]interface{}{
				"status":      status,
				"node_id":     nodeID,
				"actual_gp":   actualGP,
				"finished_at": &now,
			})
	}
}
