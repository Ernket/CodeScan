package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/orchestration"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestGetTaskDetailHandlerIncludesExpandedOrchestrationSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "task-detail.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite database: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}, &model.TaskStage{}); err != nil {
		t.Fatalf("auto-migrate sqlite schema: %v", err)
	}
	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		_ = sqlDB.Close()
	})

	now := time.Date(2026, 5, 20, 8, 30, 0, 0, time.UTC)
	task := model.Task{
		ID:        "task-1",
		Name:      "Demo Task",
		Remark:    "detail test",
		Status:    "running",
		CreatedAt: now,
		Logs:      []string{"[08:30:00] created"},
	}
	stage := model.TaskStage{
		TaskID:    task.ID,
		Name:      "auth",
		Status:    "completed",
		Result:    "ok",
		CreatedAt: now,
		UpdatedAt: now.Add(2 * time.Minute),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	progressAt := now.Add(5 * time.Minute)
	eventAt := now.Add(6 * time.Minute)
	oldLoadSummary := loadTaskOrchestrationSummary
	loadTaskOrchestrationSummary = func(taskID string) (*orchestration.TaskSummary, error) {
		if taskID != "task-1" {
			t.Fatalf("expected summary load for task-1, got %s", taskID)
		}
		return &orchestration.TaskSummary{
			ActiveRunID:        "run-1",
			PlannerRevision:    4,
			ActiveSubtaskCount: 2,
			LastRunStatus:      "running",
			LastReplanReason:   "integrator_completed",
			FocusStatus:        "blocked",
			CurrentStage:       "auth",
			LastProgressAt:     &progressAt,
			LatestEventAt:      &eventAt,
			LatestEventMessage: "validator waiting for upstream confirmation",
		}, nil
	}
	t.Cleanup(func() {
		loadTaskOrchestrationSummary = oldLoadSummary
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTestCurrentUser(c)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/tasks/task-1", nil)
	c.Params = gin.Params{{Key: "id", Value: "task-1"}}

	GetTaskDetailHandler(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload taskDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.ID != "task-1" {
		t.Fatalf("expected task id task-1, got %s", payload.ID)
	}
	if len(payload.Stages) != 1 || payload.Stages[0].Name != "auth" {
		t.Fatalf("expected auth stage in response, got %+v", payload.Stages)
	}
	if payload.Orchestration == nil {
		t.Fatal("expected orchestration summary in response")
	}
	if payload.Orchestration.FocusStatus != "blocked" {
		t.Fatalf("expected focus status blocked, got %q", payload.Orchestration.FocusStatus)
	}
	if payload.Orchestration.CurrentStage != "auth" {
		t.Fatalf("expected current stage auth, got %q", payload.Orchestration.CurrentStage)
	}
	if payload.Orchestration.ActiveSubtaskCount != 2 {
		t.Fatalf("expected active subtask count 2, got %d", payload.Orchestration.ActiveSubtaskCount)
	}
	if payload.Orchestration.LastProgressAt == nil || !payload.Orchestration.LastProgressAt.Equal(progressAt) {
		t.Fatalf("expected last progress at %v, got %v", progressAt, payload.Orchestration.LastProgressAt)
	}
	if payload.Orchestration.LatestEventAt == nil || !payload.Orchestration.LatestEventAt.Equal(eventAt) {
		t.Fatalf("expected latest event at %v, got %v", eventAt, payload.Orchestration.LatestEventAt)
	}
	if payload.Orchestration.LatestEventMessage != "validator waiting for upstream confirmation" {
		t.Fatalf("unexpected latest event message %q", payload.Orchestration.LatestEventMessage)
	}
}
