package scanner

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"

	"github.com/glebarez/sqlite"
	"github.com/sashabaranov/go-openai"
	"gorm.io/gorm"
)

func TestFinalizeRunOutputRepairsJSONAutomatically(t *testing.T) {
	useTestAIRequestPolicy(t, 1)

	prevAIConfig := config.AI
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}
	t.Cleanup(func() {
		config.AI = prevAIConfig
	})

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, `[{"method":"GET","path":"/health","source":"api.go","description":"health"}]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	task := newTestTask(t)
	outputJSON, meta, err := finalizeRunOutput(task, "init", nil, StageRunInitial, "raw text [{bad]")
	if err != nil {
		t.Fatalf("finalize run output: %v", err)
	}

	if callCount != 1 {
		t.Fatalf("expected one automatic repair attempt, got %d", callCount)
	}
	if meta.LastRunKind != string(StageRunInitial) {
		t.Fatalf("expected last run kind %q, got %q", StageRunInitial, meta.LastRunKind)
	}
	if !strings.Contains(string(outputJSON), `"/health"`) {
		t.Fatalf("expected repaired route JSON, got %s", string(outputJSON))
	}
}

func TestFinalizeRunOutputRejectsRepairedSchemaMismatch(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		return newChatCompletionHTTPResponse(t, req, `[{"type":"RCE","affected_endpoints":[]}]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	task := newTestTask(t)
	stage := &model.TaskStage{
		TaskID: task.ID,
		Name:   "auth",
	}
	if _, _, err := finalizeRunOutput(task, "auth", stage, StageRunInitial, "raw text [{bad]"); err == nil {
		t.Fatal("expected repaired auth output with RCE type to fail")
	}
}

func TestRunAIScanLimitReachedDoesNotWriteEmptyArrayOnRepairFailure(t *testing.T) {
	useTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:         "task-limit-failure",
		Status:     "pending",
		BasePath:   t.TempDir(),
		OutputJSON: json.RawMessage(`[{"method":"OLD","path":"/old","source":"old.go","description":"old"}]`),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		return newChatCompletionHTTPResponse(t, req, "not json"), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "init", StageRunInitial, false, ScanExecutionOptions{
		ManageTaskStatus:  true,
		PersistTaskRecord: true,
	})

	var saved model.Task
	if err := database.DB.First(&saved, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load saved task: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed task after invalid limit output, got %q", saved.Status)
	}
	if strings.TrimSpace(string(saved.OutputJSON)) == "[]" {
		t.Fatalf("expected old output_json to be preserved instead of [], got %s", string(saved.OutputJSON))
	}
	if !strings.Contains(string(saved.OutputJSON), `"/old"`) {
		t.Fatalf("expected old output_json to remain, got %s", string(saved.OutputJSON))
	}
	if !strings.Contains(saved.Result, "Result processing error:") {
		t.Fatalf("expected processing error marker in result, got %q", saved.Result)
	}
}

func TestRunAIScanStageParseFailurePreservesRawAIOutput(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-stage-parse-failure",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/old","source":"old.go","description":"old"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "access",
		Status:     "pending",
		OutputJSON: json.RawMessage(`[]`),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	raw := `analysis before malformed json
` + "```json\n" + `[
  {"type":"Authorization","affected_endpoints":["GET /admin"]}
` + "\n```"
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		return newChatCompletionHTTPResponse(t, req, raw), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "access", StageRunInitial, false, ScanExecutionOptions{
		ManageTaskStatus:  true,
		PersistTaskRecord: true,
	})

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "access").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed stage after invalid output, got %q", saved.Status)
	}
	if !strings.Contains(saved.Result, "analysis before malformed json") {
		t.Fatalf("expected raw AI output to be preserved in stage result, got %q", saved.Result)
	}
	if !strings.Contains(saved.Result, "Result processing error:") {
		t.Fatalf("expected processing error marker in stage result, got %q", saved.Result)
	}
	if strings.TrimSpace(string(saved.OutputJSON)) != "[]" {
		t.Fatalf("expected old stage output_json to remain, got %s", string(saved.OutputJSON))
	}
}

func TestExecuteQueryStageOutputFiltersStructuredFindings(t *testing.T) {
	task := newTestTask(t)
	stage := &model.TaskStage{
		TaskID: task.ID,
		Name:   "auth",
		OutputJSON: []byte(`[
			{"origin":"initial","verification_status":"confirmed","description":"one"},
			{"origin":"gap_check","verification_status":"rejected","description":"two"}
		]`),
	}

	output := ExecuteQueryStageOutput(task, stage, "", "gap_check", "rejected", 0, 10)

	if !containsAll(output, `"stage": "auth"`, `"description": "two"`) {
		t.Fatalf("expected filtered stage output, got %q", output)
	}
	if strings.Contains(output, `"description": "one"`) {
		t.Fatalf("expected origin/status filters to exclude the initial finding, got %q", output)
	}
}

func setupScannerTaskDB(t *testing.T) {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "scanner-task.sqlite")
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

func useLargeWindowTestScannerConfig(t *testing.T) {
	t.Helper()
	config.Scanner, _ = config.NormalizeScannerConfig(config.ScannerConfig{
		ContextCompression: config.ContextCompressionConfig{
			ContextWindowTokens:    320000,
			SummaryWindowMessages:  8,
			MicrocompactKeepRecent: 1,
			CompactMinTailMessages: 2,
			SessionMemoryEnabled:   true,
		},
		SessionMemory: config.SessionMemoryConfig{
			Enabled:                true,
			MinGrowthBytes:         1,
			MinToolCalls:           1,
			MaxUpdateBytes:         2048,
			FailureCooldownSeconds: 300,
		},
	})
}
