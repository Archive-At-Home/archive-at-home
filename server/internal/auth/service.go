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
// userService implements UserService
// ─────────────────────────────────────────────

type userService struct {
	db *gorm.DB
}

// NewUserService creates a new UserService backed by the given DB.
func NewUserService(db *gorm.DB) UserService {
	return &userService{db: db}
}

// Register creates a new user with email + password.
func (s *userService) Register(ctx context.Context, email, password, nickname string) (*User, error) {
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
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		return nil, err
	}

	return user, nil
}

// LoginEmail authenticates via email + password.
func (s *userService) LoginEmail(ctx context.Context, email, password string) (*User, error) {
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

	// Update last used
	now := time.Now()
	user.LastUsedAt = &now
	s.db.WithContext(ctx).Model(&user).Update("last_used_at", now)

	return &user, nil
}

// LoginTelegram authenticates via Telegram OAuth.
func (s *userService) LoginTelegram(ctx context.Context, telegramData map[string]interface{}) (*User, error) {
	// Extract telegram ID
	telegramIDFloat, ok := telegramData["id"].(float64)
	if !ok {
		return nil, errors.New("invalid telegram data: missing id")
	}
	telegramID := int64(telegramIDFloat)

	// Try to find existing user
	var user User
	err := s.db.WithContext(ctx).Where("telegram_id = ?", telegramID).First(&user).Error
	if err == nil {
		// User exists, update last used
		now := time.Now()
		user.LastUsedAt = &now
		s.db.WithContext(ctx).Model(&user).Update("last_used_at", now)
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

	// Extract optional fields
	nickname := ""
	if firstName, ok := telegramData["first_name"].(string); ok {
		nickname = firstName
	}
	if lastName, ok := telegramData["last_name"].(string); ok {
		if nickname != "" {
			nickname += " "
		}
		nickname += lastName
	}

	user = User{
		ID:         uuid.NewString(),
		Nickname:   nickname,
		Provider:   "telegram",
		TelegramID: &telegramID,
		APIKey:     apiKey,
		Status:     "active",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if err := s.db.WithContext(ctx).Create(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// GetByAPIKey looks up a user by API key.
func (s *userService) GetByAPIKey(ctx context.Context, apiKey string) (*User, error) {
	var user User
	if err := s.db.WithContext(ctx).Where("api_key = ?", apiKey).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidAPIKey
		}
		return nil, err
	}
	return &user, nil
}

// GetByID retrieves a user by ID.
func (s *userService) GetByID(ctx context.Context, userID string) (*User, error) {
	var user User
	if err := s.db.WithContext(ctx).Where("id = ?", userID).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

// ResetAPIKey regenerates the user's API key.
func (s *userService) ResetAPIKey(ctx context.Context, userID string) (*User, error) {
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
	user.UpdatedAt = time.Now()

	if err := s.db.WithContext(ctx).Save(&user).Error; err != nil {
		return nil, err
	}

	return &user, nil
}

// SetStatus sets user account status.
func (s *userService) SetStatus(ctx context.Context, userID string, status string) error {
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UpdateLastCheckin updates the user's last checkin timestamp.
func (s *userService) UpdateLastCheckin(ctx context.Context, userID string) error {
	now := time.Now()
	result := s.db.WithContext(ctx).Model(&User{}).
		Where("id = ?", userID).
		Updates(map[string]interface{}{
			"last_checkin_at": now,
			"updated_at":      now,
		})
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
