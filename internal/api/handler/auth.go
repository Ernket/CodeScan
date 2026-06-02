package handler

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

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	ID                      uint                                 `json:"id"`
	Username                string                               `json:"username"`
	Role                    string                               `json:"role"`
	Enabled                 *bool                                `json:"enabled,omitempty"`
	CreatedAt               string                               `json:"created_at,omitempty"`
	UpdatedAt               string                               `json:"updated_at,omitempty"`
	OrganizationAssignments []userOrganizationAssignmentResponse `json:"organization_assignments,omitempty"`
}

type userOrganizationAssignmentResponse struct {
	OrganizationID uint                `json:"organization_id"`
	Role           string              `json:"role"`
	Organization   *model.Organization `json:"organization,omitempty"`
}

var authClock = time.Now

func newUserResponse(user model.User, includeAdminFields bool) userResponse {
	response := userResponse{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
	}
	if includeAdminFields {
		enabled := user.Enabled
		response.Enabled = &enabled
		response.CreatedAt = user.CreatedAt.Format(time.RFC3339)
		response.UpdatedAt = user.UpdatedAt.Format(time.RFC3339)
		if len(user.OrganizationMemberships) > 0 {
			response.OrganizationAssignments = make([]userOrganizationAssignmentResponse, 0, len(user.OrganizationMemberships))
			for _, membership := range user.OrganizationMemberships {
				assignment := userOrganizationAssignmentResponse{
					OrganizationID: membership.OrganizationID,
					Role:           membership.Role,
				}
				if membership.Organization.ID != 0 {
					org := membership.Organization
					assignment.Organization = &org
				}
				response.OrganizationAssignments = append(response.OrganizationAssignments, assignment)
			}
		}
	}
	return response
}

func LoginHandler(authKey string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req LoginRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		username := strings.TrimSpace(req.Username)
		password := req.Password
		if username == "" || password == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Username and password are required"})
			return
		}

		var user model.User
		if err := database.DB.Where("username = ?", username).First(&user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
			return
		}
		if !user.Enabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "User is disabled"})
			return
		}
		if !authsvc.CheckPassword(user.PasswordHash, password) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid username or password"})
			return
		}
		if _, err := authsvc.NormalizeRole(user.Role); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Invalid user role"})
			return
		}

		token, err := authsvc.GenerateToken(authKey, user, authClock(), authsvc.DefaultTokenTTL)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to issue token"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"token": token,
			"user":  newUserResponse(user, false),
		})
	}
}
