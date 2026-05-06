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

// LogTask records a completed task in a single write.
// Only call this for tasks that successfully entered the broadcast/claim flow.
func (s *Store) LogTask(traceID, userID, galleryID, galleryKey string, force, freeTier bool, estimatedGP int, nodeID string, success bool, failureReason string, actualGP int) {
	now := time.Now()
	s.logCh <- func() {
		tl := model.TaskLog{
			TraceID:       traceID,
			UserID:        userID,
			GalleryID:     galleryID,
			GalleryKey:    galleryKey,
			Success:       success,
			FailureReason: failureReason,
			Force:         force,
			FreeTier:      freeTier,
			EstimatedGP:   estimatedGP,
			NodeID:        nodeID,
			ActualGP:      actualGP,
			FinishedAt:    now,
		}
		if err := s.db.Create(&tl).Error; err != nil {
			log.Printf("[store] log task error: %v", err)
		}
	}
}

// TouchLastUsed asynchronously updates a user's last_used_at to now.
// Call this from business code (e.g. after a successful gallery parse), not on every auth check.
func (s *Store) TouchLastUsed(userID string) {
	now := time.Now()
	s.logCh <- func() {
		if err := s.db.Model(&auth.User{}).Where("id = ?", userID).
			UpdateColumn("last_used_at", now).Error; err != nil {
			log.Printf("[store] touch last_used_at error: %v", err)
		}
	}
}
