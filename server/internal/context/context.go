package context

import (
	"github.com/Archive-At-Home/archive-at-home/server/internal/auth"
	"github.com/gin-gonic/gin"
)

// Context key for the authenticated user.
const CtxKeyUser = "auth_user"

// MustGetUser extracts the authenticated user from the Gin context.
// Panics if not present (should only be called after APIKeyAuth middleware).
func MustGetUser(c *gin.Context) *auth.User {
	v, exists := c.Get(CtxKeyUser)
	if !exists {
		panic("MustGetUser called without APIKeyAuth middleware")
	}
	return v.(*auth.User)
}

// GetUserID is a shorthand that returns the user ID string.
func GetUserID(c *gin.Context) string {
	u := MustGetUser(c)
	return u.ID
}
