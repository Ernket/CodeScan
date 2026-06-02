package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/database"
	"codescan/internal/model"
	authsvc "codescan/internal/service/auth"
	orgsvc "codescan/internal/service/organization"
)

type createUserRequest struct {
	Username                string              `json:"username"`
	Password                string              `json:"password"`
	Role                    string              `json:"role"`
	OrganizationAssignments []orgsvc.Assignment `json:"organization_assignments"`
}

type updateUserStatusRequest struct {
	Enabled *bool `json:"enabled"`
}

type resetUserPasswordRequest struct {
	Password string `json:"password"`
}

type replaceUserOrganizationsRequest struct {
	Assignments []orgsvc.Assignment `json:"assignments"`
}

func ListUsersHandler(c *gin.Context) {
	var users []model.User
	if err := database.DB.
		Preload("OrganizationMemberships.Organization").
		Order("created_at asc").
		Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list users"})
		return
	}

	response := make([]userResponse, 0, len(users))
	for _, user := range users {
		response = append(response, newUserResponse(user, true))
	}
	c.JSON(http.StatusOK, response)
}

func CreateUserHandler(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	username := strings.TrimSpace(req.Username)
	password := req.Password
	role, err := authsvc.NormalizeRole(req.Role)
	if err != nil || role == model.RoleSuperAdmin {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Role must be admin or observer"})
		return
	}
	if username == "" || password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
		return
	}

	hash, err := authsvc.HashPassword(password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	user := model.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		Enabled:      true,
		TokenVersion: 1,
	}
	userCreateFailed := false
	membershipUpdateFailed := false
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			userCreateFailed = true
			return err
		}
		if len(req.OrganizationAssignments) == 0 {
			return nil
		}
		if err := orgsvc.ReplaceUserMemberships(tx, user.ID, req.OrganizationAssignments); err != nil {
			membershipUpdateFailed = true
			return err
		}
		return nil
	}); err != nil {
		if membershipUpdateFailed {
			handleMembershipUpdateError(c, err)
			return
		}
		if userCreateFailed {
			c.JSON(http.StatusConflict, gin.H{"error": "Username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
		return
	}
	if err := database.DB.Preload("OrganizationMemberships.Organization").First(&user, "id = ?", user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload user"})
		return
	}
	c.JSON(http.StatusCreated, newUserResponse(user, true))
}

func UpdateUserStatusHandler(c *gin.Context) {
	id, ok := parseUserID(c)
	if !ok {
		return
	}

	var req updateUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}

	var user model.User
	if err := database.DB.First(&user, "id = ?", id).Error; err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": "User not found"})
		return
	}

	if !*req.Enabled && user.Role == model.RoleSuperAdmin {
		var enabledSuperAdmins int64
		if err := database.DB.Model(&model.User{}).
			Where("role = ? AND enabled = ?", model.RoleSuperAdmin, true).
			Count(&enabledSuperAdmins).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect super admin accounts"})
			return
		}
		if enabledSuperAdmins <= 1 && user.Enabled {
			c.JSON(http.StatusConflict, gin.H{"error": "Cannot disable the last enabled super admin"})
			return
		}
	}

	updates := map[string]any{"enabled": *req.Enabled}
	if user.Enabled != *req.Enabled {
		updates["token_version"] = gorm.Expr("token_version + ?", 1)
	}
	if err := database.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user"})
		return
	}
	if err := database.DB.First(&user, "id = ?", user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload user"})
		return
	}
	c.JSON(http.StatusOK, newUserResponse(user, true))
}

func ResetUserPasswordHandler(c *gin.Context) {
	id, ok := parseUserID(c)
	if !ok {
		return
	}

	var req resetUserPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password is required"})
		return
	}

	hash, err := authsvc.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	result := database.DB.Model(&model.User{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"password_hash": hash,
			"token_version": gorm.Expr("token_version + ?", 1),
		})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset password"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	var user model.User
	if err := database.DB.First(&user, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload user"})
		return
	}
	c.JSON(http.StatusOK, newUserResponse(user, true))
}

func ReplaceUserOrganizationsHandler(c *gin.Context) {
	id, ok := parseUserID(c)
	if !ok {
		return
	}

	var req replaceUserOrganizationsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := orgsvc.ReplaceUserMemberships(database.DB, id, req.Assignments); err != nil {
		handleMembershipUpdateError(c, err)
		return
	}

	var user model.User
	if err := database.DB.Preload("OrganizationMemberships.Organization").First(&user, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload user"})
		return
	}
	c.JSON(http.StatusOK, newUserResponse(user, true))
}

func handleMembershipUpdateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, orgsvc.ErrInvalidRole):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Organization role must be member or admin"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "User or organization not found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update organization assignments"})
	}
}

func parseUserID(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user id"})
		return 0, false
	}
	return uint(id), true
}
