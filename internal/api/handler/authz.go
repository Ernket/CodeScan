package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/api/middleware"
	"codescan/internal/database"
	"codescan/internal/model"
	orgsvc "codescan/internal/service/organization"
)

var errResponseWritten = errors.New("response already written")

func currentUser(c *gin.Context) (model.User, bool) {
	user, ok := middleware.CurrentUser(c)
	if !ok || user.ID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return model.User{}, false
	}
	return user, true
}

func requireTaskRead(c *gin.Context, task *model.Task) bool {
	user, ok := currentUser(c)
	if !ok {
		return false
	}
	permissions, err := orgsvc.TaskPermissions(database.DB, user, *task)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect task permissions"})
		return false
	}
	if !permissions.CanRead {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return false
	}
	task.Permissions = permissions
	return true
}

func requireTaskWrite(c *gin.Context, task *model.Task) bool {
	user, ok := currentUser(c)
	if !ok {
		return false
	}
	permissions, err := orgsvc.TaskPermissions(database.DB, user, *task)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect task permissions"})
		return false
	}
	if !permissions.CanRead {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return false
	}
	if !permissions.CanWrite {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
		return false
	}
	task.Permissions = permissions
	return true
}

func loadReadableTask(c *gin.Context, taskID string, query func(*gorm.DB) *gorm.DB) (model.Task, bool) {
	var task model.Task
	db := database.DB
	if query != nil {
		db = query(db)
	}
	if err := db.First(&task, "id = ?", taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
			return model.Task{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load task"})
		return model.Task{}, false
	}
	if !requireTaskRead(c, &task) {
		return model.Task{}, false
	}
	return task, true
}

func loadWritableTask(c *gin.Context, taskID string, query func(*gorm.DB) *gorm.DB) (model.Task, bool) {
	var task model.Task
	db := database.DB
	if query != nil {
		db = query(db)
	}
	if err := db.First(&task, "id = ?", taskID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
			return model.Task{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load task"})
		return model.Task{}, false
	}
	if !requireTaskWrite(c, &task) {
		return model.Task{}, false
	}
	return task, true
}

func scopedReadableTasksQuery(c *gin.Context, db *gorm.DB) (*gorm.DB, bool) {
	user, ok := currentUser(c)
	if !ok {
		return nil, false
	}
	if orgsvc.IsSuperAdmin(user) {
		return db, true
	}
	ids, err := orgsvc.ReadableOrganizationIDs(database.DB, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect organization permissions"})
		return nil, false
	}
	if len(ids) == 0 {
		return db.Where("1 = 0"), true
	}
	return db.Where("organization_id IN ?", ids), true
}

func attachTaskPermissions(c *gin.Context, tasks []model.Task) bool {
	user, ok := currentUser(c)
	if !ok {
		return false
	}
	if err := orgsvc.AttachTaskPermissions(database.DB, user, tasks); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect task permissions"})
		return false
	}
	return true
}
