package handler

import (
	"errors"
	"net/http"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/tokenbucket"
	"github.com/Archive-At-Home/archive-at-home/server/internal/ws"
	"github.com/gin-gonic/gin"
)

// AdminHandler handles admin-only endpoints.
type AdminHandler struct {
	userSvc  auth.UserService
	tokenSvc *tokenbucket.TokenBucket
	hub      *ws.Hub
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(userSvc auth.UserService, tokenSvc *tokenbucket.TokenBucket, hub *ws.Hub) *AdminHandler {
	return &AdminHandler{
		userSvc:  userSvc,
		tokenSvc: tokenSvc,
		hub:      hub,
	}
}

// RegisterRoutes registers admin routes on the Gin engine.
func (h *AdminHandler) RegisterRoutes(r *gin.Engine, adminMw gin.HandlerFunc) {
	admin := r.Group("/api/v1/admin", adminMw)
	{
		admin.GET("/health", h.Health)
		admin.GET("/users/:id", h.GetUser)
		admin.PUT("/users/:id/status", h.SetUserStatus)
		admin.PUT("/users/:id/level", h.SetUserLevel)
	}
}

// ─────────────────────────────────────────────
// GET /api/v1/admin/health
// ─────────────────────────────────────────────

// Health returns basic server health info (admin-only).
func (h *AdminHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"node_count": h.hub.NodeCount(),
		"nodes":      h.hub.NodeInfoSnapshot(),
	})
}

// ─────────────────────────────────────────────
// GET /api/v1/admin/users/:id
// ─────────────────────────────────────────────

// GetUser retrieves a user's information by ID (admin-only).
// Returns the same format as /api/v1/me.
func (h *AdminHandler) GetUser(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	ctx := c.Request.Context()

	// Get user by ID
	user, err := h.userSvc.GetByID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Get token count
	tokens, err := h.tokenSvc.GetTokens(ctx, userID, user.Level)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get balance"})
		return
	}

	c.JSON(http.StatusOK, model.UserProfile{
		User:    user,
		Balance: tokens,
	})
}

// ─────────────────────────────────────────────
// PUT /api/v1/admin/users/:id/status
// ─────────────────────────────────────────────

type SetUserStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=active banned suspended"`
}

// SetUserStatus updates a user's account status (admin-only).
// Valid statuses: active, banned, suspended.
func (h *AdminHandler) SetUserStatus(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	var req SetUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.userSvc.SetStatus(c.Request.Context(), userID, req.Status); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update status"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "status updated to " + req.Status,
	})
}

// ─────────────────────────────────────────────
// PUT /api/v1/admin/users/:id/level
// ─────────────────────────────────────────────

type SetUserLevelRequest struct {
	Level int `json:"level" binding:"min=0"`
}

// SetUserLevel updates a user's level (admin-only).
// Level 0 = normal, 1+ = premium (higher token rate and capacity).
func (h *AdminHandler) SetUserLevel(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	var req SetUserLevelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.userSvc.SetLevel(c.Request.Context(), userID, req.Level); err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update level"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "level updated",
	})
}
