package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/orchestration"
	orgsvc "codescan/internal/service/organization"
	"codescan/internal/service/scanner"
	summarysvc "codescan/internal/service/summary"
	"codescan/internal/utils"
)

type taskDetailResponse struct {
	model.Task
	Orchestration *orchestration.TaskSummary `json:"orchestration,omitempty"`
}

var (
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
	repairTaskJSON = scanner.RepairJSON
	newTaskID      = utils.NewOpaqueID
)

func GetTasksHandler(c *gin.Context) {
	list, err := loadTasksForSummary(c)
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load task summaries"})
		return
	}
	c.JSON(http.StatusOK, summarysvc.BuildTaskList(list))
}

func GetTaskDetailHandler(c *gin.Context) {
	id := c.Param("id")

	task, ok := loadReadableTask(c, id, func(db *gorm.DB) *gorm.DB {
		return db.
			Preload("Organization").
			Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at asc") })
	})
	if !ok {
		return
	}

	summary, err := loadTaskOrchestrationSummary(task.ID)
	if err != nil {
		log.Printf("warning: failed to build orchestration summary for task %s: %v", task.ID, err)
	}

	c.JSON(http.StatusOK, taskDetailResponse{
		Task:          task,
		Orchestration: summary,
	})
}

func loadTasksForSummary(c *gin.Context) ([]model.Task, error) {
	query, ok := scopedReadableTasksQuery(c, database.DB.Model(&model.Task{}))
	if !ok {
		return nil, errResponseWritten
	}

	var list []model.Task
	err := query.
		Select("id", "name", "remark", "status", "organization_id", "created_at", "result", "output_json").
		Order("created_at desc").
		Preload("Organization").
		Preload("Stages", func(db *gorm.DB) *gorm.DB { return db.Order("created_at asc") }).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	if !attachTaskPermissions(c, list) {
		return nil, errResponseWritten
	}
	return list, nil
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
	user, ok := currentUser(c)
	if !ok {
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	name := c.PostForm("name")
	remark := c.PostForm("remark")

	if file.Size > utils.MaxUploadFileSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("File exceeds %dMB limit", utils.MaxUploadFileSize/(1024*1024))})
		return
	}

	organizationID, err := strconv.ParseUint(strings.TrimSpace(c.PostForm("organization_id")), 10, 64)
	if err != nil || organizationID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "organization_id is required"})
		return
	}
	canWriteOrg, err := orgsvc.CanWriteOrganization(database.DB, user, uint(organizationID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to inspect organization permissions"})
		return
	}
	if !canWriteOrg {
		c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
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
		ID:             id,
		Name:           name,
		Remark:         remark,
		Status:         "pending",
		OrganizationID: uintPtr(uint(organizationID)),
		CreatedAt:      time.Now(),
		BasePath:       projectPath,
		Logs:           []string{},
		OutputJSON:     json.RawMessage([]byte("{}")),
	}

	if err := database.DB.Create(task).Error; err != nil {
		os.RemoveAll(projectPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}

	c.JSON(http.StatusOK, task)
}

func uintPtr(value uint) *uint {
	return &value
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
	if !requireTaskWrite(c, &task) {
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
	if !requireTaskWrite(c, &task) {
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
	task, ok := loadWritableTask(c, id, nil)
	if !ok {
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

	task, ok := loadWritableTask(c, id, nil)
	if !ok {
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

	task, ok := loadWritableTask(c, id, nil)
	if !ok {
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
	stageName, ok := scanner.NormalizeRepairStage(c.Query("stage"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Unsupported repair stage: %s", c.Query("stage"))})
		return
	}

	task, ok := loadWritableTask(c, id, nil)
	if !ok {
		return
	}

	var repairSource string
	var oldOutput json.RawMessage
	var target interface{}

	if stageName == "init" {
		oldOutput = task.OutputJSON
		target = &task
	} else {
		var stage model.TaskStage
		if err := database.DB.Where("task_id = ? AND name = ?", id, stageName).First(&stage).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Stage not found"})
			return
		}
		oldOutput = stage.OutputJSON
		target = &stage
	}

	repairSource = repairableRawResult(target)
	if repairSource == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No result to repair. Please re-run the scan."})
		return
	}

	repaired, err := repairTaskJSON(repairSource, stageName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":       "Failed to repair JSON: " + err.Error(),
			"stage":       stageName,
			"output_json": oldOutput,
		})
		return
	}
	items, repairedJSON, err := scanner.ParseValidatedRepairJSON(repaired, stageName)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":       "Failed to repair JSON: " + err.Error(),
			"stage":       stageName,
			"output_json": oldOutput,
		})
		return
	}
	repaired = string(repairedJSON)

	switch t := target.(type) {
	case *model.Task:
		t.OutputJSON = json.RawMessage(repaired)
		if err := database.DB.Save(t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save repaired JSON"})
			return
		}
	case *model.TaskStage:
		t.OutputJSON = json.RawMessage(repaired)
		if err := database.DB.Save(t).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save repaired JSON"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "repaired", "stage": stageName, "output_json": json.RawMessage(repaired), "count": len(items)})
}

func repairableRawResult(target interface{}) string {
	var result string
	var logs []string
	switch t := target.(type) {
	case *model.Task:
		result = t.Result
		logs = t.Logs
	case *model.TaskStage:
		result = t.Result
		logs = t.Logs
	default:
		return ""
	}

	if canUseStoredResultForRepair(result) {
		return strings.TrimSpace(result)
	}
	return latestAIResultFromLogs(logs)
}

func canUseStoredResultForRepair(result string) bool {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "Result processing error:") {
		return false
	}
	return strings.Contains(trimmed, "[") || strings.Contains(trimmed, "{") || strings.Contains(trimmed, "```")
}

func latestAIResultFromLogs(logs []string) string {
	for i := len(logs) - 1; i >= 0; i-- {
		logEntry := logs[i]
		if idx := strings.Index(logEntry, "] AI: "); idx != -1 {
			return strings.TrimSpace(logEntry[idx+6:])
		}
	}
	return ""
}
