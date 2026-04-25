package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/orchestration"
	"codescan/internal/service/scanner"
)

var (
	taskExistsForOrchestration = func(taskID string) (bool, error) {
		var count int64
		err := database.DB.Model(&model.Task{}).Where("id = ?", taskID).Count(&count).Error
		return count > 0, err
	}
	loadOrchestrationSnapshot = func(taskID string) (*orchestration.Snapshot, error) {
		return orchestration.DefaultManager().Snapshot(taskID)
	}
	loadOrchestrationSubtasks = func(taskID string) ([]model.TaskSubtask, error) {
		return orchestration.DefaultManager().ListSubtasks(taskID)
	}
	loadOrchestrationAgents = func(taskID string) ([]model.TaskAgentRun, error) {
		return orchestration.DefaultManager().ListAgents(taskID)
	}
	loadOrchestrationEvents = func(taskID string, after uint64, limit int) ([]model.TaskEvent, error) {
		return orchestration.DefaultManager().ListEvents(taskID, after, limit)
	}
	subscribeOrchestrationEvents = func(taskID string) (<-chan model.TaskEvent, func()) {
		return orchestration.DefaultManager().Subscribe(taskID)
	}
)

func ensureOrchestrationTaskExists(c *gin.Context, taskID string) bool {
	exists, err := taskExistsForOrchestration(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect task"})
		return false
	}
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return false
	}
	return true
}

func StartOrchestrationHandler(c *gin.Context) {
	taskID := c.Param("id")

	if !config.Orchestration.Enabled {
		var task model.Task
		if err := database.DB.First(&task, "id = ?", taskID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if task.Status == "running" {
			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("task %s is already running", taskID)})
			return
		}
		if err := database.DB.Model(&model.Task{}).Where("id = ?", taskID).Update("status", "running").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		task.Status = "running"
		task.BasePath = task.GetBasePath()
		scanner.RunAIScan(&task, "init")

		snapshot, err := orchestration.DefaultManager().Snapshot(taskID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, snapshot)
		return
	}

	snapshot, err := orchestration.DefaultManager().Start(taskID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
	case err != nil:
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusOK, snapshot)
	}
}

func GetOrchestrationSnapshotHandler(c *gin.Context) {
	taskID := c.Param("id")
	if !ensureOrchestrationTaskExists(c, taskID) {
		return
	}

	snapshot, err := loadOrchestrationSnapshot(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, snapshot)
}

func GetOrchestrationSubtasksHandler(c *gin.Context) {
	taskID := c.Param("id")
	if !ensureOrchestrationTaskExists(c, taskID) {
		return
	}

	subtasks, err := loadOrchestrationSubtasks(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, subtasks)
}

func GetOrchestrationAgentsHandler(c *gin.Context) {
	taskID := c.Param("id")
	if !ensureOrchestrationTaskExists(c, taskID) {
		return
	}

	agents, err := loadOrchestrationAgents(taskID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, agents)
}

func OrchestrationEventsHandler(c *gin.Context) {
	taskID := c.Param("id")
	after, _ := strconv.ParseUint(c.Query("after"), 10, 64)

	if !ensureOrchestrationTaskExists(c, taskID) {
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	c.Writer.Flush()

	eventCh, cancel := subscribeOrchestrationEvents(taskID)
	defer cancel()

	history, err := loadOrchestrationEvents(taskID, after, 500)
	if err == nil {
		for _, event := range history {
			if writeSSE(c, event) != nil {
				return
			}
		}
	}

	heartbeatSeconds := config.Orchestration.SSEHeartbeatSeconds
	if heartbeatSeconds <= 0 {
		heartbeatSeconds = 15
	}

	heartbeat := time.NewTicker(time.Duration(heartbeatSeconds) * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}
			if event.Sequence <= after {
				continue
			}
			if writeSSE(c, event) != nil {
				return
			}
			after = event.Sequence
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeSSE(c *gin.Context, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
