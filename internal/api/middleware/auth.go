package middleware

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
)

func AuthMiddleware(authKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if token == "" {
			token = c.Query("token")
		}
		claims, err := authsvc.ParseToken(token, authKey, time.Now())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}

		var user model.User
		if err := database.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
			status := http.StatusUnauthorized
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				status = http.StatusInternalServerError
			}
			c.AbortWithStatusJSON(status, gin.H{"error": "Invalid token"})
			return
		}
		if !user.Enabled {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "User is disabled"})
			return
		}
		if user.TokenVersion != claims.TokenVersion {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Token has been revoked"})
			return
		}
		normalizedRole, err := authsvc.NormalizeRole(user.Role)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Invalid user role"})
			return
		}
		user.Role = normalizedRole
		c.Set("current_user", user)
		c.Set("current_user_id", user.ID)
		c.Set("current_user_role", normalizedRole)
		c.Next()
	}
}

func RequireWrite() gin.HandlerFunc {
	return requirePermission(authsvc.CanWrite)
}

func RequireDelete() gin.HandlerFunc {
	return requirePermission(authsvc.CanDelete)
}

func RequireUserManagement() gin.HandlerFunc {
	return requirePermission(authsvc.CanManageUsers)
}

func requirePermission(allowed func(string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		role := CurrentUserRole(c)
		if !allowed(role) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
			return
		}
		c.Next()
	}
}

func CurrentUser(c *gin.Context) (model.User, bool) {
	value, ok := c.Get("current_user")
	if !ok {
		return model.User{}, false
	}
	user, ok := value.(model.User)
	return user, ok
}

func CurrentUserRole(c *gin.Context) string {
	if role, ok := c.Get("current_user_role"); ok {
		if value, ok := role.(string); ok {
			return value
		}
	}
	if user, ok := CurrentUser(c); ok {
		return user.Role
	}
	return ""
}

func CorsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, PATCH, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}
