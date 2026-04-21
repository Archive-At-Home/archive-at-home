package handler

import (
	"errors"
	"net/http"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/balance"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/ws"
	"github.com/gin-gonic/gin"
)

// AdminHandler handles admin-only endpoints.
type AdminHandler struct {
	userSvc    auth.UserService
	balanceSvc balance.BalanceService
	hub        *ws.Hub
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(userSvc auth.UserService, balanceSvc balance.BalanceService, hub *ws.Hub) *AdminHandler {
	return &AdminHandler{
		userSvc:    userSvc,
		balanceSvc: balanceSvc,
		hub:        hub,
	}
}

// RegisterRoutes registers admin routes on the admin group.
func (h *AdminHandler) RegisterRoutes(admin *gin.RouterGroup) {
	admin.GET("/health", h.Health)
	admin.GET("/users/:id", h.GetUser)
	admin.PUT("/users/:id/status", h.SetUserStatus)
	admin.POST("/users/:id/credits", h.AddCredits)
}

// ─────────────────────────────────────────────
// GET /api/v1/admin/health
// ─────────────────────────────────────────────

// Health returns basic server health info (admin-only).
func (h *AdminHandler) Health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"node_count": h.hub.NodeCount(),
		"node_ids":   h.hub.NodeIDs(),
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

	// Get balance
	var balance int64
	acc, err := h.balanceSvc.GetAccount(ctx, userID)
	if err == nil {
		balance = acc.Available()
	}

	c.JSON(http.StatusOK, model.UserProfile{
		User:    user,
		Balance: balance,
	})
}

// ─────────────────────────────────────────────
// PUT /api/v1/admin/users/:id/status
// ─────────────────────────────────────────────

type SetUserStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=active banned suspended"`
}

type SetUserStatusResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
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

	c.JSON(http.StatusOK, SetUserStatusResponse{
		Success: true,
		Message: "status updated to " + req.Status,
	})
}

// ─────────────────────────────────────────────
// POST /api/v1/admin/users/:id/credits
// ─────────────────────────────────────────────

type AddCreditsRequest struct {
	Amount int64  `json:"amount" binding:"required,min=1"`
	Remark string `json:"remark"` // optional
}

type AddCreditsResponse struct {
	Success bool   `json:"success"`
	Balance int64  `json:"balance"`
	Message string `json:"message"`
}

// AddCredits adds GP credits to a user's balance (admin-only).
func (h *AdminHandler) AddCredits(c *gin.Context) {
	userID := c.Param("id")
	if userID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "user id required"})
		return
	}

	var req AddCreditsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Check if user exists
	_, err := h.userSvc.GetByID(ctx, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}

	// Add credits
	remark := req.Remark
	if remark == "" {
		remark = "管理员充值"
	}
	acc, err := h.balanceSvc.Deposit(ctx, userID, req.Amount, remark)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add credits"})
		return
	}

	c.JSON(http.StatusOK, AddCreditsResponse{
		Success: true,
		Balance: acc.Balance,
		Message: "credits added successfully",
	})
}
