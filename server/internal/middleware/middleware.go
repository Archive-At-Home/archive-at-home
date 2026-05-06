package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

// Logger returns a Gin middleware that logs request details.
func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		gin.DefaultWriter.Write([]byte(
			time.Now().Format("2006/01/02 15:04:05") +
				" | " + c.Request.Method +
				" | " + path +
				" | " + c.ClientIP() +
				" | " + latency.String() +
				" | " + statusText(status) + "\n",
		))
	}
}

func statusText(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}

// CORS returns a permissive CORS middleware.
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
