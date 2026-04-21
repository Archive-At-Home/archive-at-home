package middleware

import (
	"net/http"
	"strings"

	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	appctx "github.com/Archive-At-Home/archive-at-home/server/internal/context"
	"github.com/gin-gonic/gin"
)

// APIKeyAuth returns a Gin middleware that validates the API key
// from the Authorization header (format: "Bearer sk-xxx") and
// injects the authenticated User into the context.
//
// Lookup is delegated to auth.UserService.GetByAPIKey.
func APIKeyAuth(userSvc auth.UserService) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractBearerToken(c)
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing or malformed Authorization header (expected: Bearer <api-key>)",
			})
			return
		}

		user, err := userSvc.GetByAPIKey(c.Request.Context(), raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid api key",
			})
			return
		}

		if user.Status != "active" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "account is " + user.Status,
			})
			return
		}

		c.Set(appctx.CtxKeyUser, user)
		c.Next()
	}
}

// extractBearerToken gets the token from "Authorization: Bearer <token>".
func extractBearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// AdminTokenAuth returns a Gin middleware that validates the admin token
// from the Authorization header (format: "Bearer <admin-token>").
// This provides simple admin authentication without user database lookup.
func AdminTokenAuth(adminToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if adminToken == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "admin authentication not configured",
			})
			return
		}

		token := extractBearerToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "missing or malformed Authorization header (expected: Bearer <admin-token>)",
			})
			return
		}

		if token != adminToken {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "invalid admin token",
			})
			return
		}

		c.Next()
	}
}
