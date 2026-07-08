package auth

import (
	"context"
	"time"
)

// ─────────────────────────────────────────────
// User represents a registered platform user.
// ─────────────────────────────────────────────

type User struct {
	ID         string     `json:"id" gorm:"primaryKey"`
	Email      *string    `json:"email,omitempty" gorm:"uniqueIndex"`
	Password   string     `json:"-"` // bcrypt hash, never serialised
	Nickname   string     `json:"nickname"`
	Provider   string     `json:"provider" gorm:"default:email"` // "email" | "telegram"
	TelegramID *int64     `json:"telegram_id,omitempty" gorm:"uniqueIndex"`
	APIKey     string     `json:"api_key" gorm:"uniqueIndex"`   // non-expiring key, issued on login/register
	Status     string     `json:"status" gorm:"default:active"` // active | banned | suspended
	Level      int        `json:"level" gorm:"default:0"`       // user level, 0=normal, 1+=premium
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ─────────────────────────────────────────────
// UserService – the single auth interface.
//
// Supports:
//   - Email registration
//   - Telegram OAuth login
//   - API key lookup (used by middleware)
// ─────────────────────────────────────────────

type UserService interface {
	// Register creates a new user via email + password.
	// A unique API key is generated and returned with the User.
	Register(ctx context.Context, email, password, nickname string) (*User, error)

	// LoginEmail authenticates via email + password, returns the user (incl. API key).
	LoginEmail(ctx context.Context, email, password string) (*User, error)

	// LoginTelegram authenticates via Telegram OAuth callback data.
	LoginTelegram(ctx context.Context, telegramID int64, firstName, lastName string) (*User, error)

	// GetByAPIKey looks up a user by their API key.
	// This is the main method used by the auth middleware on every request.
	GetByAPIKey(ctx context.Context, apiKey string) (*User, error)

	// GetByID retrieves a user by their internal ID.
	GetByID(ctx context.Context, userID string) (*User, error)

	// ResetAPIKey regenerates the user's API key (invalidates old one).
	ResetAPIKey(ctx context.Context, userID string) (*User, error)

	// SetStatus sets user account status (active / banned / suspended).
	SetStatus(ctx context.Context, userID string, status string) error

	// SetLevel sets user level (0=normal, 1+=premium).
	SetLevel(ctx context.Context, userID string, level int) error
}
