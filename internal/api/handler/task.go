package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/scanner"
	summarysvc "codescan/internal/service/summary"
	"codescan/internal/utils"
)

func GetTasksHandler(c *gin.Context) {
	list, err := loadTasksForSummary()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load task summaries"})
		return
	}
	c.JSON(http.StatusOK, summarysvc.BuildTaskList(list))
}

func GetTaskDetailHandler(c *gin.Context) {
	id := c.Param("id")

	var task model.Task
	if err := database.DB.
		Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at asc") }).
		First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	c.JSON(http.StatusOK, task)
}

func loadTasksForSummary() ([]model.Task, error) {
	var list []model.Task
	err := database.DB.
		Model(&model.Task{}).
		Select("id", "name", "remark", "status", "created_at", "result", "output_json").
		Order("created_at desc").
		Preload("Stages", func(db *gorm.DB) *gorm.DB {
			return db.Select("id", "task_id", "name", "status", "result", "output_json", "meta", "created_at", "updated_at")
		}).
		Find(&list).Error
	return list, err
}

func isSupportedStageName(stageName string) bool {
	return stageName == "init" || summarysvc.StageLabel(stageName) != ""
}

func loadStructuredStage(taskID, stageName string) (*model.TaskStage, []map[string]any, error) {
	var stage model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", taskID, stageName).First(&stage).Error; err != nil {
		return nil, nil, err
	}
	results, ok := summarysvc.ParseJSONArray(stage.OutputJSON, stage.Result)
	if !ok {
		return &stage, nil, nil
	}
	return &stage, results, nil
}

func UploadHandler(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	name := c.PostForm("name")
	remark := c.PostForm("remark")

	if file.Size > utils.MaxFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File exceeds 30MB limit"})
		return
	}

	// Create Task
	id := strings.ReplaceAll(uuid.New().String(), "-", "")
	projectPath := filepath.Join(config.ProjectsDir, id)

	// Save Zip
	zipPath := filepath.Join(config.ProjectsDir, id+".zip")
	if err := c.SaveUploadedFile(file, zipPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
		return
	}

	// Unzip
	if err := utils.Unzip(zipPath, projectPath); err != nil {
		os.Remove(zipPath)
		os.RemoveAll(projectPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unzip failed: " + err.Error()})
		return
	}
	os.Remove(zipPath)

	task := &model.Task{
		ID:         id,
		Name:       name,
		Remark:     remark,
		Status:     "pending",
		CreatedAt:  time.Now(),
		BasePath:   projectPath,
		Logs:       []string{},
		OutputJSON: json.RawMessage([]byte("{}")),
	}

	database.DB.Create(task)
	go scanner.RunAIScan(task, "init")

	c.JSON(http.StatusOK, task)
}

var (
	errTaskDeleteNotFound = errors.New("task not found")
	errTaskDeleteRunning  = errors.New("task is running")

	newTaskDeletionStore = func(db *gorm.DB) taskDeletionStore {
		return gormTaskDeletionStore{db: db}
	}
	removeTaskPath = os.RemoveAll
)

type taskDeletionStore interface {
	DeleteTask(id string) (model.Task, error)
}

type gormTaskDeletionStore struct {
	db *gorm.DB
}

func (s gormTaskDeletionStore) DeleteTask(id string) (model.Task, error) {
	var task model.Task

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&task, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errTaskDeleteNotFound
			}
			return err
		}

		if task.Status == "running" {
			return errTaskDeleteRunning
		}

		if err := tx.Where("task_id = ?", task.ID).Delete(&model.TaskStage{}).Error; err != nil {
			return err
		}

		deleteResult := tx.Delete(&task)
		if deleteResult.Error != nil {
			return deleteResult.Error
		}
		if deleteResult.RowsAffected == 0 {
			return errTaskDeleteNotFound
		}

		return nil
	})

	return task, err
}

func DeleteTaskHandler(c *gin.Context) {
	id := c.Param("id")

	task, err := newTaskDeletionStore(database.DB).DeleteTask(id)
	switch {
	case errors.Is(err, errTaskDeleteNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	case errors.Is(err, errTaskDeleteRunning):
		c.JSON(http.StatusConflict, gin.H{"error": "Task is running. Pause it before deleting."})
		return
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete task"})
		return
	}

	taskPath := task.GetBasePath()
	if err := removeTaskPath(taskPath); err != nil {
		log.Printf("warning: failed to remove task data for %s at %s: %v", task.ID, taskPath, err)
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func PauseTaskHandler(c *gin.Context) {
	id := c.Param("id")
	var task model.Task
	if err := database.DB.Preload("Stages").First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	task.Status = "paused"
	database.DB.Save(&task)
	for _, stage := range task.Stages {
		if stage.Status == "running" {
			stage.Status = "paused"
			database.DB.Save(&stage)
		}
	}
	c.JSON(http.StatusOK, task)
}

func ResumeTaskHandler(c *gin.Context) {
	id := c.Param("id")
	var task model.Task
	if err := database.DB.First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	task.BasePath = task.GetBasePath()
	stage, err := scanner.ResumeAIScan(&task)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	task.Status = "running"
	database.DB.Save(&task)
	c.JSON(http.StatusOK, gin.H{"status": "resumed", "stage": stage, "task": task})
}

func RunStageHandler(c *gin.Context) {
	id := c.Param("id")
	stageName := c.Param("stage_name")
	if !isSupportedStageName(stageName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported stage"})
		return
	}
	var task model.Task
	if err := database.DB.First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.Status == "running" {
		c.JSON(http.StatusConflict, gin.H{"error": "Task is already running"})
		return
	}

	task.BasePath = task.GetBasePath()
	task.Status = "running"
	database.DB.Model(&task).Update("status", "running")

	go scanner.RunAIScan(&task, stageName)
	c.JSON(http.StatusOK, gin.H{"status": "stage started", "stage": stageName})
}

func GapCheckStageHandler(c *gin.Context) {
	id := c.Param("id")
	stageName := c.Param("stage_name")
	if !isSupportedStageName(stageName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported stage"})
		return
	}

	var task model.Task
	if err := database.DB.First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.Status == "running" {
		c.JSON(http.StatusConflict, gin.H{"error": "Task is already running"})
		return
	}

	if stageName == "init" {
		if _, ok := summarysvc.ParseJSONArray(task.OutputJSON, task.Result); !ok {
			c.JSON(http.StatusConflict, gin.H{"error": "Route inventory is not available as structured JSON yet. Run the scan or repair JSON first."})
			return
		}
	} else {
		stage, findings, err := loadStructuredStage(id, stageName)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Stage not found"})
			return
		}
		if !strings.EqualFold(stage.Status, "completed") {
			c.JSON(http.StatusConflict, gin.H{"error": "Stage must complete once before gap check can run"})
			return
		}
		if findings == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Stage output is not structured JSON yet. Repair JSON first."})
			return
		}
	}

	task.BasePath = task.GetBasePath()
	task.Status = "running"
	database.DB.Model(&task).Update("status", "running")
	go scanner.RunGapCheck(&task, stageName)
	c.JSON(http.StatusOK, gin.H{"status": "gap check started", "stage": stageName})
}

func RevalidateStageHandler(c *gin.Context) {
	id := c.Param("id")
	stageName := c.Param("stage_name")
	if stageName == "init" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Route inventory does not support revalidation"})
		return
	}
	if !isSupportedStageName(stageName) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported stage"})
		return
	}

	var task model.Task
	if err := database.DB.First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.Status == "running" {
		c.JSON(http.StatusConflict, gin.H{"error": "Task is already running"})
		return
	}

	stage, findings, err := loadStructuredStage(id, stageName)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Stage not found"})
		return
	}
	if !strings.EqualFold(stage.Status, "completed") {
		c.JSON(http.StatusConflict, gin.H{"error": "Stage must complete once before revalidation can run"})
		return
	}
	if findings == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Stage output is not structured JSON yet. Repair JSON first."})
		return
	}
	if len(findings) == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "No findings are available to revalidate"})
		return
	}

	task.BasePath = task.GetBasePath()
	task.Status = "running"
	database.DB.Model(&task).Update("status", "running")
	go scanner.RunRevalidate(&task, stageName)
	c.JSON(http.StatusOK, gin.H{"status": "revalidation started", "stage": stageName})
}

func RepairJSONHandler(c *gin.Context) {
	id := c.Param("id")
	stageName := c.Query("stage")

	var task model.Task
	if err := database.DB.First(&task, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	var rawResult string
	var target interface{}

	if stageName == "" || stageName == "init" {
		rawResult = task.Result
		target = &task
	} else {
		var stage model.TaskStage
		if err := database.DB.Where("task_id = ? AND name = ?", id, stageName).First(&stage).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Stage not found"})
			return
		}
		rawResult = stage.Result
		target = &stage
	}

	if rawResult == "" {
		var logs []string
		switch t := target.(type) {
		case *model.Task:
			logs = t.Logs
		case *model.TaskStage:
			logs = t.Logs
		}

		for i := len(logs) - 1; i >= 0; i-- {
			logEntry := logs[i]
			if idx := strings.Index(logEntry, "] AI: "); idx != -1 {
				rawResult = logEntry[idx+6:]
				break
			}
		}
	}

	if rawResult == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No result to repair. Please re-run the scan."})
		return
	}

	repaired, err := scanner.RepairJSON(rawResult, stageName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to repair JSON: " + err.Error()})
		return
	}

	switch t := target.(type) {
	case *model.Task:
		t.OutputJSON = json.RawMessage(repaired)
		database.DB.Save(t)
	case *model.TaskStage:
		t.OutputJSON = json.RawMessage(repaired)
		database.DB.Save(t)
	}

	c.JSON(http.StatusOK, gin.H{"status": "repaired", "output_json": json.RawMessage(repaired)})
}
