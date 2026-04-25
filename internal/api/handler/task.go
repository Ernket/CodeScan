package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/orchestration"
	"codescan/internal/service/scanner"
	summarysvc "codescan/internal/service/summary"
	"codescan/internal/utils"
)

type taskDetailResponse struct {
	model.Task
	Orchestration *orchestration.TaskSummary `json:"orchestration,omitempty"`
}

var (
	launchTaskOrchestration = func(taskID string) error {
		_, err := orchestration.DefaultManager().Start(taskID)
		return err
	}
	launchLegacyInitScan = func(task *model.Task) {
		scanner.RunAIScan(task, "init")
	}
	loadTaskStatus = func(taskID string) (string, error) {
		var task model.Task
		if err := database.DB.Select("status").First(&task, "id = ?", taskID).Error; err != nil {
			return "", err
		}
		return task.Status, nil
	}
	persistTask = func(task *model.Task) error {
		return database.DB.Save(task).Error
	}
	persistTaskStatus = func(taskID, status string) error {
		return database.DB.Model(&model.Task{}).Where("id = ?", taskID).Update("status", status).Error
	}
	loadTaskWithStagesForControl = func(taskID string) (model.Task, error) {
		var task model.Task
		err := database.DB.Preload("Stages").First(&task, "id = ?", taskID).Error
		return task, err
	}
	loadTaskForControl = func(taskID string) (model.Task, error) {
		var task model.Task
		err := database.DB.First(&task, "id = ?", taskID).Error
		return task, err
	}
	saveTaskForControl = func(task *model.Task) error {
		return database.DB.Save(task).Error
	}
	saveTaskStageForControl = func(stage *model.TaskStage) error {
		return database.DB.Save(stage).Error
	}
	markTaskOrchestrationPaused = func(taskID string) error {
		return orchestration.DefaultManager().MarkPaused(taskID)
	}
	loadTaskOrchestrationSummary = func(taskID string) (*orchestration.TaskSummary, error) {
		return orchestration.DefaultManager().Summary(taskID)
	}
	resumeTaskOrchestration = func(taskID string) (*orchestration.Snapshot, error) {
		return orchestration.DefaultManager().Resume(taskID)
	}
	resumeLegacyTaskScan = func(task *model.Task) (string, error) {
		return scanner.ResumeAIScan(task)
	}
	taskLogClock = time.Now
	newTaskID    = utils.NewOpaqueID
)

func formatTaskLogEntry(message string) string {
	return fmt.Sprintf("[%s] %s", taskLogClock().Format("15:04:05"), message)
}

func autoStartUploadedTask(task *model.Task) error {
	if !config.Orchestration.Enabled {
		task.Status = "running"
		if err := persistTaskStatus(task.ID, "running"); err != nil {
			return err
		}
		launchLegacyInitScan(task)
		return nil
	}

	if err := launchTaskOrchestration(task.ID); err == nil {
		task.Status = "running"
		return nil
	} else {
		status, statusErr := loadTaskStatus(task.ID)
		if statusErr == nil && strings.EqualFold(status, "running") {
			task.Status = "running"
			return nil
		}

		message := fmt.Sprintf("Automatic orchestration failed to start: %v", err)
		log.Printf("warning: task %s auto-start failed: %v", task.ID, err)

		task.Status = "failed"
		task.Result = message
		task.Logs = append(task.Logs, formatTaskLogEntry(message))
		return persistTask(task)
	}
}

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

	summary, err := orchestration.DefaultManager().Summary(task.ID)
	if err != nil {
		log.Printf("warning: failed to build orchestration summary for task %s: %v", task.ID, err)
	}

	c.JSON(http.StatusOK, taskDetailResponse{
		Task:          task,
		Orchestration: summary,
	})
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
	id := newTaskID()
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

	if err := database.DB.Create(task).Error; err != nil {
		os.RemoveAll(projectPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}
	if err := autoStartUploadedTask(task); err != nil {
		log.Printf("warning: task %s created but failed to initialize execution: %v", task.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize task"})
		return
	}

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

var taskScopedDeletionTables = []string{
	"task_findings",
	"task_routes",
	"task_events",
	"task_agent_runs",
	"task_subtasks",
	"task_runs",
	"task_stages",
}

type gormTaskDeletionStore struct {
	db *gorm.DB
}

type taskScopedDeletionExecutor interface {
	DeleteByTaskID(table, taskID string) error
	DeleteTaskRecord(task *model.Task) (int64, error)
}

type gormTaskDeletionExecutor struct {
	tx *gorm.DB
}

func (e gormTaskDeletionExecutor) DeleteByTaskID(table, taskID string) error {
	switch table {
	case "task_findings":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskFinding{}).Error
	case "task_routes":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskRoute{}).Error
	case "task_events":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskEvent{}).Error
	case "task_agent_runs":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskAgentRun{}).Error
	case "task_subtasks":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskSubtask{}).Error
	case "task_runs":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskRun{}).Error
	case "task_stages":
		return e.tx.Where("task_id = ?", taskID).Delete(&model.TaskStage{}).Error
	default:
		return fmt.Errorf("unsupported deletion table %q", table)
	}
}

func (e gormTaskDeletionExecutor) DeleteTaskRecord(task *model.Task) (int64, error) {
	result := e.tx.Delete(task)
	return result.RowsAffected, result.Error
}

func executeTaskScopedDeletion(executor taskScopedDeletionExecutor, task *model.Task) error {
	for _, table := range taskScopedDeletionTables {
		if err := executor.DeleteByTaskID(table, task.ID); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
	}

	rowsAffected, err := executor.DeleteTaskRecord(task)
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errTaskDeleteNotFound
	}
	return nil
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

		return executeTaskScopedDeletion(gormTaskDeletionExecutor{tx: tx}, &task)
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
	task, err := loadTaskWithStagesForControl(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	switch strings.ToLower(strings.TrimSpace(task.Status)) {
	case "running":
	case "paused":
		c.JSON(http.StatusConflict, gin.H{"error": "Task is already paused"})
		return
	default:
		c.JSON(http.StatusConflict, gin.H{"error": "Only running tasks can be paused"})
		return
	}

	task.Status = "paused"
	if err := saveTaskForControl(&task); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to pause task"})
		return
	}
	for i := range task.Stages {
		stage := &task.Stages[i]
		if stage.Status == "running" {
			stage.Status = "paused"
			if err := saveTaskStageForControl(stage); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to pause task"})
				return
			}
		}
	}
	if err := markTaskOrchestrationPaused(id); err != nil {
		log.Printf("warning: failed to pause orchestration for task %s: %v", id, err)
	}
	c.JSON(http.StatusOK, task)
}

func ResumeTaskHandler(c *gin.Context) {
	id := c.Param("id")
	task, err := loadTaskForControl(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}

	switch strings.ToLower(strings.TrimSpace(task.Status)) {
	case "paused":
	case "running":
		c.JSON(http.StatusConflict, gin.H{"error": "Task is already running"})
		return
	default:
		c.JSON(http.StatusConflict, gin.H{"error": "Only paused tasks can be resumed"})
		return
	}

	summary, err := loadTaskOrchestrationSummary(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect task runtime"})
		return
	}
	if summary != nil && summary.LastRunStatus == "paused" {
		snapshot, resumeErr := resumeTaskOrchestration(id)
		if resumeErr != nil {
			c.JSON(http.StatusConflict, gin.H{"error": resumeErr.Error()})
			return
		}
		task.Status = "running"
		if err := saveTaskForControl(&task); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resume task"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "resumed", "mode": "orchestration", "task": task, "orchestration": snapshot})
		return
	}

	task.BasePath = task.GetBasePath()
	stage, err := resumeLegacyTaskScan(&task)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	task.Status = "running"
	if err := saveTaskForControl(&task); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resume task"})
		return
	}
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
