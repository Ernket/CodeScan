package handler

import (
	"github.com/gin-gonic/gin"

	"codescan/internal/model"
)

func setTestCurrentUser(c *gin.Context) {
	user := model.User{ID: 1, Username: "super", Role: model.RoleSuperAdmin, Enabled: true, TokenVersion: 1}
	c.Set("current_user", user)
	c.Set("current_user_id", user.ID)
	c.Set("current_user_role", user.Role)
}
