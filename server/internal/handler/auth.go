package handler

import (
	"errors"
	"net/http"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	"github.com/Archive-At-Home/archive-at-home/server/web"
	"github.com/gin-gonic/gin"
	widget "github.com/wesleym/telegramwidget"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	userSvc auth.UserService
	cfg     *config.Config
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(userSvc auth.UserService, cfg *config.Config) *AuthHandler {
	return &AuthHandler{userSvc: userSvc, cfg: cfg}
}

// ─────────────────────────────────────────────
// POST /auth/register
// ─────────────────────────────────────────────

type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
	Nickname string `json:"nickname"`
}

type AuthResponse struct {
	User   *auth.User `json:"user"`
	APIKey string     `json:"api_key"`
}

// Register handles user registration via email.
func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := h.userSvc.Register(c.Request.Context(), req.Email, req.Password, req.Nickname)
	if err != nil {
		if errors.Is(err, auth.ErrEmailExists) {
			c.JSON(http.StatusConflict, gin.H{"error": "email already registered"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "registration failed"})
		return
	}

	c.JSON(http.StatusCreated, AuthResponse{
		User:   user,
		APIKey: user.APIKey,
	})
}

// ─────────────────────────────────────────────
// POST /auth/login
// ─────────────────────────────────────────────

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// Login handles user login via email + password.
func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	user, err := h.userSvc.LoginEmail(c.Request.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredential) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "login failed"})
		return
	}

	c.JSON(http.StatusOK, AuthResponse{
		User:   user,
		APIKey: user.APIKey,
	})
}

// ─────────────────────────────────────────────
// GET /auth/telegram/login
// ─────────────────────────────────────────────

// TelegramLoginPage serves the Telegram login intermediate page.
// Query parameters:
// - redirect_url: URL to redirect to after successful login (optional)
// - param_name: parameter name for API key in redirect URL (default: "start")
func (h *AuthHandler) TelegramLoginPage(c *gin.Context) {
	if h.cfg.TelegramBotUsername == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "telegram login not configured"})
		return
	}

	data := map[string]any{
		"BotUsername": h.cfg.TelegramBotUsername,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := web.TelegramLoginTemplate.Execute(c.Writer, data); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to render template"})
		return
	}
}

// ─────────────────────────────────────────────
// POST /auth/telegram/callback
// ─────────────────────────────────────────────

// TelegramCallback handles Telegram OAuth login.
// JSON body contains only Telegram official auth data for signature verification.
// Redirect logic is handled entirely by the frontend JavaScript.
func (h *AuthHandler) TelegramCallback(c *gin.Context) {
	if h.cfg.TelegramBotToken == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "telegram login not configured"})
		return
	}

	telegramUser, err := widget.ConvertAndVerifyJSON(c.Request.Body, h.cfg.TelegramBotToken)
	if err != nil {
		if errors.Is(err, widget.ErrInvalidHash) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid telegram auth data"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "malformed telegram auth data"})
		}
		return
	}

	user, err := h.userSvc.LoginTelegram(c.Request.Context(), telegramUser.ID, telegramUser.FirstName, telegramUser.LastName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "telegram authentication failed"})
		return
	}

	// Return API key for client use (redirect handled by frontend)
	c.JSON(http.StatusOK, AuthResponse{
		User:   user,
		APIKey: user.APIKey,
	})
}

// RegisterRoutes registers auth routes on the Gin engine.
func (h *AuthHandler) RegisterRoutes(r *gin.Engine) {
	authGroup := r.Group("/auth")
	{
		if h.cfg.EmailAuthEnabled {
			authGroup.POST("/register", h.Register)
			authGroup.POST("/login", h.Login)
		}
		authGroup.GET("/telegram/login", h.TelegramLoginPage)
		authGroup.POST("/telegram/callback", h.TelegramCallback)
	}
}
