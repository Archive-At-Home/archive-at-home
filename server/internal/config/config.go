package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all application-level settings.
type Config struct {
	// Server
	ServerAddr string

	// Redis
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Cache
	CacheTTL time.Duration // per-user result cache lifetime

	// Task
	TaskLeaseTTL    time.Duration // lease timeout for claimed tasks
	TaskWaitTimeout time.Duration // max time HTTP handler blocks waiting for result

	// PostgreSQL
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	// Telegram
	TelegramBotToken    string // Bot token for Telegram Login Widget verification
	TelegramBotUsername string // Bot username for Telegram Login Widget (e.g., "EhArchive_bot")

	// Checkin
	CheckinMinGP int // Minimum GP reward for daily checkin
	CheckinMaxGP int // Maximum GP reward for daily checkin

	// Node Authentication
	NodeVerifyKey string // ED25519 public key (Base64 encoded) for verifying node signatures

	// Admin Authentication
	AdminToken string // Bearer token for admin API access

	// Feature flags
	EmailAuthEnabled bool // Whether email registration/login is enabled
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		ServerAddr:          envOr("SERVER_ADDR", ":8080"),
		RedisAddr:           envOr("REDIS_ADDR", "localhost:6379"),
		RedisPassword:       envOr("REDIS_PASSWORD", ""),
		RedisDB:             envIntOr("REDIS_DB", 0),
		CacheTTL:            envDurationOr("CACHE_TTL", 7*24*time.Hour),
		TaskLeaseTTL:        envDurationOr("TASK_LEASE_TTL", 2*time.Minute),
		TaskWaitTimeout:     envDurationOr("TASK_WAIT_TIMEOUT", 90*time.Second),
		DBHost:              envOr("DB_HOST", "localhost"),
		DBPort:              envOr("DB_PORT", "5432"),
		DBUser:              envOr("DB_USER", "postgres"),
		DBPassword:          envOr("DB_PASSWORD", "postgres"),
		DBName:              envOr("DB_NAME", "ehentai"),
		DBSSLMode:           envOr("DB_SSLMODE", "disable"),
		TelegramBotToken:    envOr("TELEGRAM_BOT_TOKEN", ""),
		TelegramBotUsername: envOr("TELEGRAM_BOT_USERNAME", ""),
		CheckinMinGP:        envIntOr("CHECKIN_MIN_GP", 10000),
		CheckinMaxGP:        envIntOr("CHECKIN_MAX_GP", 20000),
		NodeVerifyKey:       envOr("NODE_VERIFY_KEY", ""),
		AdminToken:          envOr("ADMIN_TOKEN", ""),
		EmailAuthEnabled:    envBoolOr("EMAIL_AUTH_ENABLED", false),
	}
}

// ─── helpers ───

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func envBoolOr(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
