package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// ─────────────────────────────────────────────
// Errors
// ─────────────────────────────────────────────

var (
	ErrUserNotFound      = errors.New("user not found")
	ErrEmailExists       = errors.New("email already registered")
	ErrInvalidCredential = errors.New("invalid email or password")
	ErrInvalidAPIKey     = errors.New("invalid api key")
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
// UserService
// ─────────────────────────────────────────────

// UserService provides user auth CRUD backed by GORM.
// Supports email registration, Telegram OAuth login,
// and API-key-based authentication (used by middleware).
type UserService struct {
	db *gorm.DB
}

// NewUserService creates a new UserService backed by the given DB.
func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db}
}

// Register creates a new user via email + password.
// A unique API key is generated and returned with the User.
func (s *UserService) Register(ctx context.Context, email, password, nickname string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	// Check if email exists
	var existing User
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&existing).Error; err == nil {
		return nil, ErrEmailExists
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	// Generate API key
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, err
	}

	user := &User{
		ID:        uuid.NewString(),
		Email:     &email,
		Password:  string(hash),
		Nickname:  nickname,
		Provider:  "email",
		APIKey:    apiKey,
		CreatedAt: time.Now(),
	}

	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		return nil, err
	}

	return user, nil
}

// LoginEmail authenticates via email + password, returns the user (incl. API key).
func (s *UserService) LoginEmail(ctx context.Context, email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	var user User
	if err := s.db.WithContext(ctx).Where("email = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidCredential
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return nil, ErrInvalidCredential
	}

	return &user, nil
}

// LoginTelegram authenticates via Telegram OAuth callback data.
func (s *UserService) LoginTelegram(ctx context.Context, telegramID int64, firstName, lastName string) (*User, error) {

	// Try to find existing user
	var user User
	err := s.db.WithContext(ctx).Where("telegram_id = ?", telegramID).First(&user).Error
	if err == nil {
		// User exists, return directly
		return &user, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Create new user from Telegram
	apiKey, err := generateAPIKey()
	if err != nil {
		return nil, err
	}

	nickname := strings.TrimSpace(firstName)
	trimmedLastName := strings.TrimSpace(lastName)
	if trimmedLastName != "" {
		if nickname != "" {
			nickname += " "
		}
		nickname += trimmedLastName
	}

	user = User{
		ID:         uuid.NewString(),
		Nickname:   nickname,
		Provider:   "telegram",
		TelegramID: &telegramID,
		APIKey:     apiKey,
		CreatedAt:  time.Now(),
	}

	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// GetByAPIKey looks up a user by their API key.
// This is the main method used by the auth middleware on every request.
func (s *UserService) GetByAPIKey(ctx context.Context, apiKey string) (*User, error) {
	var user User
	if err := s.db.WithContext(ctx).Where("api_key = ?", apiKey).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidAPIKey
		}
		return nil, err
	}
	return &user, nil
}

// GetByID retrieves a user by their internal ID.
func (s *UserService) GetByID(ctx context.Context, userID string) (*User, error) {
	var user User
	if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

// ResetAPIKey regenerates the user's API key (invalidates old one).
func (s *UserService) ResetAPIKey(ctx context.Context, userID string) (*User, error) {
	var user User
	if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	newKey, err := generateAPIKey()
	if err != nil {
		return nil, err
	}

	user.APIKey = newKey

	if err := s.db.WithContext(ctx).Model(&user).UpdateColumn("api_key", newKey).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// SetStatus sets user account status (active / banned / suspended).
func (s *UserService) SetStatus(ctx context.Context, userID, status string) error {
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", userID).
		UpdateColumn("status", status)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetLevel sets user level (0=normal, 1+=premium).
func (s *UserService) SetLevel(ctx context.Context, userID string, level int) error {
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", userID).
		UpdateColumn("level", level)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

// generateAPIKey creates a new API key with "sk-" prefix.
func generateAPIKey() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "sk-" + hex.EncodeToString(bytes), nil
}
