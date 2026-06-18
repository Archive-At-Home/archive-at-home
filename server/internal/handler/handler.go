package handler

import (
	"log"
	"net/http"
	"strings"

	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
	appctx "github.com/Archive-At-Home/archive-at-home/server/internal/context"
	"github.com/Archive-At-Home/archive-at-home/server/internal/model"
	"github.com/Archive-At-Home/archive-at-home/server/internal/node"
	"github.com/Archive-At-Home/archive-at-home/server/internal/service"
	"github.com/Archive-At-Home/archive-at-home/server/internal/ws"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/mod/semver"
)

const minNodeVersion = "v0.6.0"

// Handler holds HTTP/WS endpoint handlers.
type Handler struct {
	svc      *service.GalleryService
	hub      *ws.Hub
	nodeAuth *node.Authenticator
	cfg      *config.Config
	upgrader websocket.Upgrader
}

// NewHandler creates the handler set.
func NewHandler(svc *service.GalleryService, hub *ws.Hub, nodeAuth *node.Authenticator, cfg *config.Config) *Handler {
	return &Handler{
		svc:      svc,
		hub:      hub,
		nodeAuth: nodeAuth,
		cfg:      cfg,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(r *http.Request) bool { return true },
		},
	}
}

// RegisterRoutes registers all routes on the Gin engine.
// apiKeyMiddleware should be nil during early development (no-auth mode);
// when non-nil it protects all business endpoints via API key.
func (h *Handler) RegisterRoutes(r *gin.Engine, apiKeyMiddleware ...gin.HandlerFunc) {
	// ── WebSocket for worker nodes (uses its own node_id auth) ──
	r.GET("/ws", h.WebSocket)

	// ── Protected business endpoints ──
	api := r.Group("/api/v1")
	for _, mw := range apiKeyMiddleware {
		api.Use(mw)
	}
	{
		api.POST("/parse", h.ParseGallery)
	}
}

// ─────────────────────────────────────────────
// POST /api/v1/parse
// ─────────────────────────────────────────────

// ParseGallery handles gallery parse requests.
//
//	@Summary      Request gallery archive parsing
//	@Description  Checks cache (unless force=true), collapses duplicate requests,
//	              dispatches to worker nodes and returns the parsed result.
//	@Param        body  body  model.ParseRequest  true  "Parse request"
//	@Param        X-Client  header  string  false  "Client identifier in format <category>/<app>, e.g. bot/tg-official"
//	@Success      200   {object}  model.ParseResponse
//	@Failure      400
//	@Failure      500
//	@Router       /api/v1/parse [post]
func (h *Handler) ParseGallery(c *gin.Context) {
	var req model.ParseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// UserID comes from the API key middleware, not the request body.
	userID := appctx.GetUserID(c)
	client := resolveClient(c.GetHeader("X-Client"), c.GetHeader("User-Agent"))

	resp, err := h.svc.ParseGallery(c.Request.Context(), userID, client, &req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// resolveClient returns a non-empty category/app string for task_logs.client.
// X-Client (format: category/app) is used when present and valid.
// Otherwise the User-Agent header is used to make a best-effort guess.
func resolveClient(xClient, ua string) string {
	if v := normalizeClientHeader(xClient); v != "" {
		return v
	}
	cat, app := guessFromUA(ua)
	return cat + "/" + app
}

func normalizeClientHeader(raw string) string {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" {
		return ""
	}

	if len(v) > 64 {
		return ""
	}

	parts := strings.Split(v, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}

	isValid := func(s string) bool {
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
				continue
			}
			return false
		}
		return true
	}

	if !isValid(parts[0]) || !isValid(parts[1]) {
		return ""
	}

	return parts[0] + "/" + parts[1]
}

func guessFromUA(ua string) (category string, appName string) {
	v := strings.ToLower(ua)

	if strings.Contains(v, "python") {
		return "script", "python"
	}
	if strings.Contains(v, "curl") {
		return "script", "curl"
	}
	if strings.Contains(v, "mozilla") || strings.Contains(v, "safari") {
		return "script", "unknown"
	}

	return "unknown", "unknown"
}

// ─────────────────────────────────────────────
// GET /ws  (Worker node WebSocket)
// ─────────────────────────────────────────────

// WebSocket upgrades the connection and registers the worker node.
// Header: X-Auth-Token: <NodeID>:<Signature>
// Signature is ED25519 signed NodeID (Base64 encoded).
func (h *Handler) WebSocket(c *gin.Context) {
	// Extract and verify auth token from header
	authToken := c.GetHeader("X-Auth-Token")
	if authToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "X-Auth-Token header required"})
		return
	}

	// Verify signature and extract NodeID
	nodeID, err := h.nodeAuth.VerifyAuthToken(authToken)
	if err != nil {
		log.Printf("[handler] node auth failed: %v", err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid authentication token"})
		return
	}

	// Upgrade to WebSocket
	if v := c.GetHeader("X-Node-Version"); !semver.IsValid(v) || semver.Compare(v, minNodeVersion) < 0 {
		log.Printf("[handler] node %s version rejected: %s (min %s)", nodeID, v, minNodeVersion)
		c.JSON(http.StatusUpgradeRequired, gin.H{
			"error":            "node version too old, please update",
			"min_node_version": minNodeVersion,
		})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("[handler] websocket upgrade error: %v", err)
		return
	}

	// Register client and start listening
	client := ws.NewClient(nodeID, conn, h.hub)
	if err := client.Run(c.Request.Context()); err != nil {
		log.Printf("[handler] client registration failed for node %s: %v", nodeID, err)
		return
	}
}
