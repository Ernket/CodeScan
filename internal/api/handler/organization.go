package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/database"
	orgsvc "codescan/internal/service/organization"
)

type organizationRequest struct {
	Name     string `json:"name"`
	ParentID *uint  `json:"parent_id"`
}

type updateOrganizationRequest struct {
	Name     *string `json:"name"`
	ParentID *uint   `json:"parent_id"`
}

func GetAccessibleOrganizationsHandler(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	tree, err := orgsvc.AccessibleTree(database.DB, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load organizations"})
		return
	}
	c.JSON(http.StatusOK, tree)
}

func ListOrganizationsHandler(c *gin.Context) {
	tree, err := orgsvc.AllTree(database.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load organizations"})
		return
	}
	c.JSON(http.StatusOK, tree)
}

func CreateOrganizationHandler(c *gin.Context) {
	var req organizationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	org, err := orgsvc.Create(database.DB, req.Name, req.ParentID)
	switch {
	case errors.Is(err, orgsvc.ErrInvalidName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Organization name is required"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Parent organization not found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create organization"})
	default:
		c.JSON(http.StatusCreated, org)
	}
}

func UpdateOrganizationHandler(c *gin.Context) {
	id, ok := parseOrganizationID(c)
	if !ok {
		return
	}

	var req updateOrganizationRequest
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)

	_, parentChanged := raw["parent_id"]
	org, err := orgsvc.Update(database.DB, id, req.Name, req.ParentID, parentChanged)
	switch {
	case errors.Is(err, orgsvc.ErrInvalidName):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Organization name is required"})
	case errors.Is(err, orgsvc.ErrInvalidMove):
		c.JSON(http.StatusConflict, gin.H{"error": "Organization cannot be moved under itself or a descendant"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Organization not found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update organization"})
	default:
		c.JSON(http.StatusOK, org)
	}
}

func DeleteOrganizationHandler(c *gin.Context) {
	id, ok := parseOrganizationID(c)
	if !ok {
		return
	}

	err := orgsvc.Delete(database.DB, id)
	switch {
	case errors.Is(err, orgsvc.ErrOrganizationHasChildren):
		c.JSON(http.StatusConflict, gin.H{"error": "Organization has child organizations"})
	case errors.Is(err, orgsvc.ErrOrganizationHasTasks):
		c.JSON(http.StatusConflict, gin.H{"error": "Organization has projects"})
	case errors.Is(err, orgsvc.ErrOrganizationHasMembers):
		c.JSON(http.StatusConflict, gin.H{"error": "Organization has members"})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Organization not found"})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete organization"})
	default:
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	}
}

func parseOrganizationID(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid organization id"})
		return 0, false
	}
	return uint(id), true
}
