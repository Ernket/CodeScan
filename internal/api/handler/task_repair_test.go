package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codescan/internal/database"
	"codescan/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRepairJSONHandlerRepairsInitStage(t *testing.T) {
	setupRepairHandlerDB(t)
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		if stage != "init" {
			t.Fatalf("expected init repair stage, got %q", stage)
		}
		return `[{"method":"GET","path":"/health","source":"api.go","description":"health"}]`, nil
	})()

	task := model.Task{
		ID:         "task-init",
		Status:     "completed",
		Result:     "raw route text ```json\n[{bad]\n```",
		OutputJSON: json.RawMessage(`[{"method":"OLD","path":"/old","source":"old.go","description":"old"}]`),
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	w := performRepairRequest("task-init", "")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if string(payload["stage"]) != `"init"` {
		t.Fatalf("expected response stage init, got %s", string(payload["stage"]))
	}
	if !strings.Contains(string(payload["output_json"]), `"/health"`) {
		t.Fatalf("expected repaired output in response, got %s", string(payload["output_json"]))
	}

	var saved model.Task
	if err := database.DB.First(&saved, "id = ?", "task-init").Error; err != nil {
		t.Fatalf("load saved task: %v", err)
	}
	if !strings.Contains(string(saved.OutputJSON), `"/health"`) {
		t.Fatalf("expected repaired task output_json to persist, got %s", string(saved.OutputJSON))
	}
}

func TestRepairJSONHandlerRepairsAuditStage(t *testing.T) {
	setupRepairHandlerDB(t)
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		if stage != "auth" {
			t.Fatalf("expected auth repair stage, got %q", stage)
		}
		return `[{"type":"Authentication","affected_endpoints":[]}]`, nil
	})()

	task := model.Task{ID: "task-auth", Status: "completed", CreatedAt: time.Now()}
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		Result:     "raw auth text ```json\n[{bad]\n```",
		OutputJSON: json.RawMessage(`[{"type":"Authentication","affected_endpoints":["OLD /old"]}]`),
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	w := performRepairRequest("task-auth", "auth")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if strings.Contains(string(saved.OutputJSON), "OLD /old") || !strings.Contains(string(saved.OutputJSON), `"Authentication"`) {
		t.Fatalf("expected repaired auth output_json to persist, got %s", string(saved.OutputJSON))
	}
}

func TestRepairJSONHandlerRejectsUnknownStage(t *testing.T) {
	setupRepairHandlerDB(t)

	task := model.Task{ID: "task-unknown", Status: "completed", CreatedAt: time.Now()}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	w := performRepairRequest("task-unknown", "bogus")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unsupported repair stage: bogus") {
		t.Fatalf("expected unsupported stage error, got %s", w.Body.String())
	}
}

func TestRepairJSONHandlerPreservesOldOutputOnRepairFailure(t *testing.T) {
	setupRepairHandlerDB(t)
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		return "", errors.New("schema mismatch")
	})()

	oldOutput := json.RawMessage(`[{"type":"Authentication","affected_endpoints":["OLD /old"]}]`)
	task := model.Task{ID: "task-fail", Status: "completed", CreatedAt: time.Now()}
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		Result:     "raw auth text ```json\n[{bad]\n```",
		OutputJSON: oldOutput,
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	w := performRepairRequest("task-fail", "auth")

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnprocessableEntity, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "OLD /old") {
		t.Fatalf("expected old output_json in 422 response, got %s", w.Body.String())
	}

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if string(saved.OutputJSON) != string(oldOutput) {
		t.Fatalf("expected old output_json to remain, got %s", string(saved.OutputJSON))
	}
}

func TestRepairJSONHandlerRejectsLocallyInvalidRepairResult(t *testing.T) {
	setupRepairHandlerDB(t)
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		return `[{"type":"RCE","affected_endpoints":[]}]`, nil
	})()

	oldOutput := json.RawMessage(`[{"type":"Authentication","affected_endpoints":["OLD /old"]}]`)
	task := model.Task{ID: "task-invalid-local", Status: "completed", CreatedAt: time.Now()}
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		Result:     "raw auth text ```json\n[{bad]\n```",
		OutputJSON: oldOutput,
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	w := performRepairRequest("task-invalid-local", "auth")

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusUnprocessableEntity, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "OLD /old") {
		t.Fatalf("expected old output_json in local validation failure response, got %s", w.Body.String())
	}
}

func TestRepairJSONHandlerUsesAILogWhenStoredResultIsProcessingError(t *testing.T) {
	setupRepairHandlerDB(t)
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		if stage != "access" {
			t.Fatalf("expected access repair stage, got %q", stage)
		}
		if strings.Contains(content, "Result processing error") {
			t.Fatalf("expected repair content to use AI log, got %q", content)
		}
		if !strings.Contains(content, "authorization-source") {
			t.Fatalf("expected repair content from AI log, got %q", content)
		}
		return `[{"type":"Authorization","affected_endpoints":["GET /admin"]}]`, nil
	})()

	task := model.Task{ID: "task-access-log", Status: "failed", CreatedAt: time.Now()}
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "access",
		Status:     "failed",
		Result:     "Result processing error: AI output is not a JSON array",
		OutputJSON: json.RawMessage(`[]`),
		Logs: []string{
			`[10:00:00] AI: authorization-source ` + "```json\n" + `[
  {"type":"Authorization","affected_endpoints":["GET /admin"]}
]` + "\n```",
		},
		CreatedAt: time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	w := performRepairRequest(task.ID, "access")

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatalf("expected response count 1, got %s", w.Body.String())
	}
	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "access").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if !strings.Contains(string(saved.OutputJSON), `"Authorization"`) || !strings.Contains(string(saved.OutputJSON), `GET /admin`) {
		t.Fatalf("expected repaired access output_json to persist, got %s", string(saved.OutputJSON))
	}
}

func TestRepairJSONHandlerReturnsBadRequestWhenNoRepairableSource(t *testing.T) {
	setupRepairHandlerDB(t)
	called := false
	defer stubRepairTaskJSON(t, func(content string, stage string) (string, error) {
		called = true
		return `[]`, nil
	})()

	task := model.Task{ID: "task-no-source", Status: "failed", CreatedAt: time.Now()}
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "access",
		Status:     "failed",
		Result:     "Result processing error: AI output is not a JSON array",
		OutputJSON: json.RawMessage(`[]`),
		CreatedAt:  time.Now(),
	}
	if err := database.DB.Create(&task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(&stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	w := performRepairRequest(task.ID, "access")

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, w.Code, w.Body.String())
	}
	if called {
		t.Fatal("expected repair model not to be called without a repairable source")
	}
}

func performRepairRequest(taskID string, stage string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	setTestCurrentUser(c)
	target := "/api/tasks/" + taskID + "/repair"
	if stage != "" {
		target += "?stage=" + stage
	}
	c.Request = httptest.NewRequest(http.MethodPost, target, nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}

	RepairJSONHandler(c)
	return w
}

func setupRepairHandlerDB(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "repair-handler.sqlite")
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
}

func stubRepairTaskJSON(t *testing.T, fn func(content string, stage string) (string, error)) func() {
	t.Helper()
	old := repairTaskJSON
	repairTaskJSON = fn
	return func() {
		repairTaskJSON = old
	}
}
