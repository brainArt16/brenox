package middleware

import (
	"net/http"
	"strings"

	"github.com/brainart16/brenox/internal/billing"
	"github.com/gin-gonic/gin"
)

func MaintenanceMiddleware(service *billing.Service) gin.HandlerFunc {
	if service == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if isMaintenanceBypass(c.Request.URL.Path) {
			c.Next()
			return
		}
		active, message, err := service.IsMaintenanceMode(c.Request.Context())
		if err != nil {
			c.Next()
			return
		}
		if active {
			msg := message
			if strings.TrimSpace(msg) == "" {
				msg = "Platform is under maintenance"
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": msg})
			c.Abort()
			return
		}
		c.Next()
	}
}

func isMaintenanceBypass(path string) bool {
	switch {
	case path == "/health", path == "/version", path == "/metrics":
		return true
	case strings.HasPrefix(path, "/webhooks/stripe"):
		return true
	case path == "/api/platform/status", path == "/api/plans":
		return true
	case strings.HasPrefix(path, "/api/admin"):
		return true
	// Admins must be able to authenticate to turn maintenance mode off.
	case path == "/auth/login", path == "/auth/refresh":
		return true
	default:
		return false
	}
}
