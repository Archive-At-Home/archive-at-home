package auth

import (
	"context"
	"time"
)

// ─────────────────────────────────────────────
// User represents a registered platform user.
// ─────────────────────────────────────────────

type User struct {
	ID            string     `json:"id" gorm:"primaryKey"`
	Email         *string    `json:"email,omitempty" gorm:"uniqueIndex"`
	Password      string     `json:"-"` // bcrypt hash, never serialised
	Nickname      string     `json:"nickname"`
	Provider      string     `json:"provider" gorm:"default:email"` // "email" | "telegram"
	TelegramID    *int64     `json:"telegram_id,omitempty" gorm:"uniqueIndex"`
	APIKey        string     `json:"api_key" gorm:"uniqueIndex"`   // non-expiring key, issued on login/register
	Status        string     `json:"status" gorm:"default:active"` // active | banned | suspended
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	LastCheckinAt *time.Time `json:"last_checkin_at,omitempty"` // last daily checkin time
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
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
	// Concrete parameter type TBD — depends on Telegram Bot API docs at implementation time.
	LoginTelegram(ctx context.Context, telegramData map[string]interface{}) (*User, error)

	// GetByAPIKey looks up a user by their API key.
	// This is the main method used by the auth middleware on every request.
	GetByAPIKey(ctx context.Context, apiKey string) (*User, error)

	// GetByID retrieves a user by their internal ID.
	GetByID(ctx context.Context, userID string) (*User, error)

	// ResetAPIKey regenerates the user's API key (invalidates old one).
	ResetAPIKey(ctx context.Context, userID string) (*User, error)

	// SetStatus sets user account status (active / banned / suspended).
	SetStatus(ctx context.Context, userID string, status string) error
	// UpdateLastCheckin updates the user's last checkin timestamp.
	UpdateLastCheckin(ctx context.Context, userID string) error
}
