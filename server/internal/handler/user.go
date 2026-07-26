package handler

import (
	"net/http"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	appctx "github.com/Archive-At-Home/archive-at-home/server/internal/context"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/tokenbucket"
	"github.com/gin-gonic/gin"
)

// UserHandler handles user-related endpoints.
type UserHandler struct {
	userSvc  *auth.UserService
	tokenSvc *tokenbucket.TokenBucket
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(userSvc *auth.UserService, tokenSvc *tokenbucket.TokenBucket) *UserHandler {
	return &UserHandler{
		userSvc:  userSvc,
		tokenSvc: tokenSvc,
	}
}

// ─────────────────────────────────────────────
// POST /api/v1/me/reset-key
// ─────────────────────────────────────────────

// ResetAPIKey regenerates the user's API key.
func (h *UserHandler) ResetAPIKey(c *gin.Context) {
	user := appctx.MustGetUser(c)

	updatedUser, err := h.userSvc.ResetAPIKey(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset api key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"api_key": updatedUser.APIKey})
}

// ─────────────────────────────────────────────
// GET /api/v1/me/balance
// ─────────────────────────────────────────────

// MyBalance returns the user's current available token count.
func (h *UserHandler) MyBalance(c *gin.Context) {
	user := appctx.MustGetUser(c)

	tokens, err := h.tokenSvc.GetTokens(c.Request.Context(), user.ID, user.Level)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get balance"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"balance": tokens})
}

// ─────────────────────────────────────────────
// GET /api/v1/me
// ─────────────────────────────────────────────

// Me returns the authenticated user's profile with token count.
func (h *UserHandler) Me(c *gin.Context) {
	user := appctx.MustGetUser(c)
	ctx := c.Request.Context()

	// Get token count
	tokens, err := h.tokenSvc.GetTokens(ctx, user.ID, user.Level)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get balance"})
		return
	}

	c.JSON(http.StatusOK, model.UserProfile{
		User:    user,
		Balance: tokens,
	})
}

// RegisterRoutes registers user routes on the Gin engine.
func (h *UserHandler) RegisterRoutes(r *gin.Engine, apiKeyMw gin.HandlerFunc) {
	api := r.Group("/api/v1/me", apiKeyMw)
	{
		api.GET("", h.Me)
		api.POST("/reset-key", h.ResetAPIKey)
		api.GET("/balance", h.MyBalance)
	}
}
