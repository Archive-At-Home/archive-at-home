package handler

import (
	"math/rand"
	"net/http"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/Archive-At-Home/archive-at-home/server/internal/balance"
	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	appctx "github.com/Archive-At-Home/archive-at-home/server/internal/context"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/gin-gonic/gin"
)

// UserHandler handles user-related endpoints.
type UserHandler struct {
	userSvc    auth.UserService
	balanceSvc balance.BalanceService
	cfg        *config.Config
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(userSvc auth.UserService, balanceSvc balance.BalanceService, cfg *config.Config) *UserHandler {
	return &UserHandler{
		userSvc:    userSvc,
		balanceSvc: balanceSvc,
		cfg:        cfg,
	}
}

// ─────────────────────────────────────────────
// POST /api/v1/me/reset-key
// ─────────────────────────────────────────────

type ResetKeyResponse struct {
	APIKey string `json:"api_key"`
}

// ResetAPIKey regenerates the user's API key.
func (h *UserHandler) ResetAPIKey(c *gin.Context) {
	user := appctx.MustGetUser(c)

	updatedUser, err := h.userSvc.ResetAPIKey(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reset api key"})
		return
	}

	c.JSON(http.StatusOK, ResetKeyResponse{
		APIKey: updatedUser.APIKey,
	})
}

// ─────────────────────────────────────────────
// GET /api/v1/me/balance
// ─────────────────────────────────────────────

type BalanceResponse struct {
	Balance int64 `json:"balance"`
}

// MyBalance returns the user's current available GP balance.
func (h *UserHandler) MyBalance(c *gin.Context) {
	user := appctx.MustGetUser(c)

	acc, err := h.balanceSvc.GetAccount(c.Request.Context(), user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get balance"})
		return
	}

	c.JSON(http.StatusOK, BalanceResponse{
		Balance: acc.Available(),
	})
}

// RegisterRoutes registers user routes on the api group.
func (h *UserHandler) RegisterRoutes(api *gin.RouterGroup) {
	api.GET("/me", h.Me)
	api.POST("/me/reset-key", h.ResetAPIKey)
	api.GET("/me/balance", h.MyBalance)
	api.POST("/me/checkin", h.Checkin)
}

// ─────────────────────────────────────────────
// GET /api/v1/me
// ─────────────────────────────────────────────

// Me returns the authenticated user's profile with available balance.
func (h *UserHandler) Me(c *gin.Context) {
	user := appctx.MustGetUser(c)
	ctx := c.Request.Context()

	// Get balance
	var balance int64
	acc, err := h.balanceSvc.GetAccount(ctx, user.ID)
	if err == nil {
		balance = acc.Available()
	}

	c.JSON(http.StatusOK, model.UserProfile{
		User:    user,
		Balance: balance,
	})
}

// ─────────────────────────────────────────────
// POST /api/v1/me/checkin
// ─────────────────────────────────────────────

type CheckinResponse struct {
	Success bool   `json:"success"`
	Reward  int64  `json:"reward"`
	Balance int64  `json:"balance"`
	Message string `json:"message,omitempty"`
}

// Checkin handles daily checkin.
func (h *UserHandler) Checkin(c *gin.Context) {
	user := appctx.MustGetUser(c)
	ctx := c.Request.Context()

	// Check if already checked in today
	if user.LastCheckinAt != nil {
		now := time.Now()
		last := *user.LastCheckinAt
		// Compare by date (same year, month, day)
		if now.Year() == last.Year() && now.YearDay() == last.YearDay() {
			c.JSON(http.StatusOK, CheckinResponse{
				Success: false,
				Message: "今日已签到",
			})
			return
		}
	}

	// Generate random reward in range [min, max]
	minGP := h.cfg.CheckinMinGP
	maxGP := h.cfg.CheckinMaxGP
	if minGP > maxGP {
		minGP, maxGP = maxGP, minGP
	}
	reward := int64(minGP)
	if maxGP > minGP {
		reward = int64(minGP + rand.Intn(maxGP-minGP+1))
	}

	// Deposit the reward
	acc, err := h.balanceSvc.Deposit(ctx, user.ID, reward, "每日签到")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to add reward"})
		return
	}

	// Update last checkin time
	if err := h.userSvc.UpdateLastCheckin(ctx, user.ID); err != nil {
		// Non-fatal, reward already added
	}

	c.JSON(http.StatusOK, CheckinResponse{
		Success: true,
		Reward:  reward,
		Balance: acc.Balance,
		Message: "签到成功",
	})
}
