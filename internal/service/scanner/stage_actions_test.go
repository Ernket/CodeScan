package scanner

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestFinalizeRevalidationOutputAcceptsFullyCoveredPureJSONArray(t *testing.T) {
	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))

	outputJSON, meta, err := finalizeRunOutput(
		task,
		"auth",
		stage,
		StageRunRevalidate,
		string(mustMarshalTestJSON(t, []map[string]any{authFinding(1, summaryStatusConfirmed)})),
	)
	if err != nil {
		t.Fatalf("finalize revalidation output: %v", err)
	}

	findings := decodeStageOutput(t, outputJSON)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %d", len(findings))
	}
	if got := findings[0]["verification_status"]; got != summaryStatusConfirmed {
		t.Fatalf("expected confirmed status, got %v", got)
	}
	if meta.ConfirmedCount != 1 || meta.UncertainCount != 0 || meta.RejectedCount != 0 {
		t.Fatalf("unexpected review counts: %+v", meta)
	}
	if meta.ReviewSummary != "Revalidation completed: 1 reviewed, 1 confirmed, 0 uncertain, 0 rejected." {
		t.Fatalf("unexpected review summary: %q", meta.ReviewSummary)
	}
}

func TestFinalizeRevalidationOutputAcceptsShortIndexedReviewAndPreservesDetails(t *testing.T) {
	task := newTestTask(t)
	original := authFinding(1, "")
	original["execution_logic"] = "original execution logic"
	original["impact"] = "original impact"
	original["poc_http"] = "GET /auth/1 original"
	stage := authStageWithFindings(t, task, original)
	reviewed := []map[string]any{
		{
			"finding_index":       0,
			"verification_status": summaryStatusRejected,
			"reviewed_severity":   "low",
			"verification_reason": "code path is not reachable",
			"description":         "model attempted overwrite",
			"execution_logic":     "model attempted execution overwrite",
			"impact":              "model attempted impact overwrite",
			"poc_http":            "POST /changed",
			"location":            map[string]any{"file": "changed.go", "line": 999},
			"affected_endpoints":  []any{"POST /changed"},
			"vulnerable_code":     "changed",
			"trigger_steps":       "changed",
			"origin":              "changed",
			"severity":            "CRITICAL",
		},
	}

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err != nil {
		t.Fatalf("finalize short revalidation output: %v", err)
	}

	findings := decodeStageOutput(t, outputJSON)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %d", len(findings))
	}
	finding := findings[0]
	if got := finding["verification_status"]; got != summaryStatusRejected {
		t.Fatalf("expected rejected status, got %v", got)
	}
	if got := finding["reviewed_severity"]; got != "LOW" {
		t.Fatalf("expected reviewed severity LOW, got %v", got)
	}
	if got := finding["verification_reason"]; got != "code path is not reachable" {
		t.Fatalf("expected review reason to be applied, got %v", got)
	}
	if got := finding["severity"]; got != "HIGH" {
		t.Fatalf("expected original severity to be preserved, got %v", got)
	}
	if got := finding["description"]; got != "authentication finding 1" {
		t.Fatalf("expected original description to be preserved, got %v", got)
	}
	if got := finding["execution_logic"]; got != "original execution logic" {
		t.Fatalf("expected original execution logic to be preserved, got %v", got)
	}
	if got := finding["poc_http"]; got != "GET /auth/1 original" {
		t.Fatalf("expected original poc_http to be preserved, got %v", got)
	}
	if _, exists := finding["finding_index"]; exists {
		t.Fatalf("finding_index must not be persisted into final finding: %+v", finding)
	}
	if meta.RejectedCount != 1 || meta.ConfirmedCount != 0 || meta.UncertainCount != 0 {
		t.Fatalf("unexpected review counts: %+v", meta)
	}
}

func TestFinalizeRevalidationOutputLegacyFullFindingOnlyUpdatesReviewFields(t *testing.T) {
	task := newTestTask(t)
	original := authFinding(1, "")
	original["poc_http"] = "GET /auth/1 original"
	original["vulnerable_code"] = "original vulnerable code"
	stage := authStageWithFindings(t, task, original)
	reviewed := authFinding(1, summaryStatusConfirmed)
	reviewed["description"] = "model attempted description overwrite"
	reviewed["poc_http"] = "POST /changed"
	reviewed["vulnerable_code"] = "changed vulnerable code"
	reviewed["affected_endpoints"] = []any{"POST /changed"}
	reviewed["severity"] = "LOW"
	reviewed["reviewed_severity"] = "medium"
	reviewed["verification_reason"] = "sink remains reachable"

	outputJSON, _, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, []map[string]any{reviewed})))
	if err != nil {
		t.Fatalf("finalize legacy full revalidation output: %v", err)
	}

	findings := decodeStageOutput(t, outputJSON)
	finding := findings[0]
	if got := finding["verification_status"]; got != summaryStatusConfirmed {
		t.Fatalf("expected confirmed status, got %v", got)
	}
	if got := finding["reviewed_severity"]; got != "MEDIUM" {
		t.Fatalf("expected reviewed severity MEDIUM, got %v", got)
	}
	if got := finding["severity"]; got != "HIGH" {
		t.Fatalf("expected original severity to be preserved, got %v", got)
	}
	if got := finding["description"]; got != "authentication finding 1" {
		t.Fatalf("expected original description to be preserved, got %v", got)
	}
	if got := finding["poc_http"]; got != "GET /auth/1 original" {
		t.Fatalf("expected original poc_http to be preserved, got %v", got)
	}
	if got := finding["vulnerable_code"]; got != "original vulnerable code" {
		t.Fatalf("expected original vulnerable_code to be preserved, got %v", got)
	}
}

func TestFinalizeRevalidationOutputExtractsMarkdownJSONArray(t *testing.T) {
	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""), authFinding(2, ""))
	reviewed := []map[string]any{
		authFinding(1, summaryStatusConfirmed),
		authFinding(2, summaryStatusRejected),
	}
	content := "Review notes before JSON.\n```json\n" + string(mustMarshalTestJSON(t, reviewed)) + "\n```\nFooter."

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, content)
	if err != nil {
		t.Fatalf("finalize markdown revalidation output: %v", err)
	}

	findings := decodeStageOutput(t, outputJSON)
	if len(findings) != 2 {
		t.Fatalf("expected two findings, got %d", len(findings))
	}
	if meta.ConfirmedCount != 1 || meta.RejectedCount != 1 || meta.UncertainCount != 0 {
		t.Fatalf("unexpected review counts: %+v", meta)
	}
}

func TestFinalizeRevalidationOutputExtractsNarratedBareJSONArray(t *testing.T) {
	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))
	reviewed := []map[string]any{authFinding(1, summaryStatusUncertain)}
	content := "The final reviewed array follows:\n" + string(mustMarshalTestJSON(t, reviewed)) + "\nDone."

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, content)
	if err != nil {
		t.Fatalf("finalize narrated bare JSON output: %v", err)
	}

	findings := decodeStageOutput(t, outputJSON)
	if len(findings) != 1 {
		t.Fatalf("expected one finding, got %d", len(findings))
	}
	if got := findings[0]["verification_status"]; got != summaryStatusUncertain {
		t.Fatalf("expected uncertain status, got %v", got)
	}
	if meta.UncertainCount != 1 {
		t.Fatalf("expected one uncertain finding, got %+v", meta)
	}
}

func TestFinalizeRevalidationOutputRepairsMissingVerificationStatus(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	repaired := []map[string]any{authFinding(1, summaryStatusUncertain)}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		body := readRequestBody(t, req)
		if !containsAll(body, "failed vulnerability revalidation JSON result", `invalid verification_status`, `array length MUST be exactly 1`) {
			t.Fatalf("unexpected repair prompt: %s", body)
		}
		return newChatCompletionHTTPResponse(t, req, string(mustMarshalTestJSON(t, repaired))), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))
	reviewed := []map[string]any{authFinding(1, "")}

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err != nil {
		t.Fatalf("expected revalidation repair to succeed: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected one revalidation repair request, got %d", callCount)
	}
	findings := decodeStageOutput(t, outputJSON)
	if got := findings[0]["verification_status"]; got != summaryStatusUncertain {
		t.Fatalf("expected repaired uncertain status, got %v", got)
	}
	if meta.UncertainCount != 1 || meta.ConfirmedCount != 0 || meta.RejectedCount != 0 {
		t.Fatalf("unexpected repaired review counts: %+v", meta)
	}
}

func TestFinalizeRevalidationOutputRepairsPartialCoverage(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	repaired := []map[string]any{
		authFinding(1, summaryStatusConfirmed),
		authFinding(2, summaryStatusUncertain),
		authFinding(3, summaryStatusRejected),
		authFinding(4, summaryStatusUncertain),
		authFinding(5, summaryStatusUncertain),
	}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		body := readRequestBody(t, req)
		if !containsAll(body, `5 existing findings`, `3 reviewed`, `2 unreviewed`, `array length MUST be exactly 5`) {
			t.Fatalf("unexpected repair prompt: %s", body)
		}
		return newChatCompletionHTTPResponse(t, req, string(mustMarshalTestJSON(t, repaired))), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task,
		authFinding(1, ""),
		authFinding(2, ""),
		authFinding(3, ""),
		authFinding(4, ""),
		authFinding(5, ""),
	)
	reviewed := []map[string]any{
		authFinding(1, summaryStatusConfirmed),
		authFinding(2, summaryStatusUncertain),
		authFinding(3, summaryStatusRejected),
	}

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err != nil {
		t.Fatalf("expected partial revalidation repair to succeed: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected one revalidation repair request, got %d", callCount)
	}
	findings := decodeStageOutput(t, outputJSON)
	if len(findings) != 5 {
		t.Fatalf("expected five repaired findings, got %d", len(findings))
	}
	if meta.ConfirmedCount != 1 || meta.UncertainCount != 3 || meta.RejectedCount != 1 {
		t.Fatalf("unexpected repaired review counts: %+v", meta)
	}
}

func TestFinalizeRevalidationOutputRejectsNaturalLanguageEvenAfterRepair(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, `[]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))

	_, _, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, "Confirmed by code review.")
	if err == nil {
		t.Fatal("expected natural-language revalidation to fail coverage validation")
	}
	if callCount != 1 {
		t.Fatalf("expected one revalidation repair attempt, got %d", callCount)
	}
	if !strings.Contains(err.Error(), "Revalidation incomplete: 1 existing findings, 0 reviewed, 1 unreviewed.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinalizeRevalidationOutputRejectsPartialCoverageAfterRepairFailure(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task,
		authFinding(1, ""),
		authFinding(2, ""),
		authFinding(3, ""),
		authFinding(4, ""),
		authFinding(5, ""),
	)
	reviewed := []map[string]any{
		authFinding(1, summaryStatusConfirmed),
		authFinding(2, summaryStatusUncertain),
		authFinding(3, summaryStatusRejected),
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, string(mustMarshalTestJSON(t, reviewed))), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	_, _, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err == nil {
		t.Fatal("expected partial revalidation coverage to fail")
	}
	if callCount != 1 {
		t.Fatalf("expected one failed revalidation repair request, got %d", callCount)
	}
	if !strings.Contains(err.Error(), "Revalidation incomplete: 5 existing findings, 3 reviewed, 2 unreviewed.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinalizeRevalidationOutputRejectsInvalidStatus(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task,
		authFinding(1, ""),
		authFinding(2, ""),
		authFinding(3, ""),
		authFinding(4, ""),
		authFinding(5, ""),
	)
	reviewed := []map[string]any{
		authFinding(1, summaryStatusConfirmed),
		authFinding(2, summaryStatusUncertain),
		authFinding(3, "verified"),
		authFinding(4, summaryStatusRejected),
		authFinding(5, summaryStatusConfirmed),
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, string(mustMarshalTestJSON(t, reviewed))), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	_, _, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err == nil {
		t.Fatal("expected invalid verification_status to fail")
	}
	if callCount != 1 {
		t.Fatalf("expected one failed revalidation repair request, got %d", callCount)
	}
	if !strings.Contains(err.Error(), `invalid verification_status "verified"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinalizeRevalidationOutputRejectsUnmatchedFinding(t *testing.T) {
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})

	task := newTestTask(t)
	stage := authStageWithFindings(t, task, authFinding(1, ""))
	reviewed := []map[string]any{authFinding(99, summaryStatusConfirmed)}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, string(mustMarshalTestJSON(t, reviewed))), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	_, _, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, string(mustMarshalTestJSON(t, reviewed)))
	if err == nil {
		t.Fatal("expected unmatched revalidation item to fail")
	}
	if callCount != 1 {
		t.Fatalf("expected one failed revalidation repair request, got %d", callCount)
	}
	if !strings.Contains(err.Error(), "does not match an existing finding") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinalizeRevalidationOutputAllowsEmptyWhenNoExistingFindings(t *testing.T) {
	task := newTestTask(t)
	stage := authStageWithFindings(t, task)

	outputJSON, meta, err := finalizeRunOutput(task, "auth", stage, StageRunRevalidate, `[]`)
	if err != nil {
		t.Fatalf("expected empty revalidation to succeed for empty stage: %v", err)
	}
	if strings.TrimSpace(string(outputJSON)) != "[]" {
		t.Fatalf("expected empty output JSON, got %s", outputJSON)
	}
	if meta.ReviewSummary != "Revalidation skipped: no findings to review." {
		t.Fatalf("unexpected review summary: %q", meta.ReviewSummary)
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

func TestRunAIScanInitUsesSubmittedRoutesForFinalOutput(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-init-submit-routes",
		Status:   "pending",
		BasePath: t.TempDir(),
	}
	mustWriteFile(t, filepath.Join(task.BasePath, "routes.go"), "package main\n")
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		switch callCount {
		case 1:
			return newChatCompletionHTTPResponseWithToolCalls(t, req, []openai.ToolCall{
				{
					ID:   "call-submit",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name: "submit_routes",
						Arguments: `{"routes":[` +
							`{"method":"get","path":"/api/users","source":"routes.go","description":"list users"},` +
							`{"method":"post","path":"/api/login","source":"routes.go","description":"login"}` +
							`]}`,
					},
				},
			}), nil
		default:
			return newChatCompletionHTTPResponse(t, req, "ROUTE_DISCOVERY_COMPLETE"), nil
		}
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
	if saved.Status != "completed" {
		t.Fatalf("expected completed task, got %q result=%q", saved.Status, saved.Result)
	}
	if strings.TrimSpace(saved.Result) != "ROUTE_DISCOVERY_COMPLETE" {
		t.Fatalf("expected short final marker, got %q", saved.Result)
	}
	routes := decodeStageOutput(t, saved.OutputJSON)
	if len(routes) != 2 {
		t.Fatalf("expected two submitted routes, got %+v", routes)
	}
	if routes[0]["method"] != "GET" || routes[0]["path"] != "/api/users" || routes[0]["source"] != "routes.go" {
		t.Fatalf("unexpected first route: %+v", routes[0])
	}
	if callCount < 2 {
		t.Fatalf("expected at least tool call round plus final marker, got %d calls", callCount)
	}
}

func TestRunAIScanStageUsesSubmittedFindingsForFinalOutput(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-auth-submit-findings",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "pending",
		OutputJSON: json.RawMessage(`[]`),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	args, err := json.Marshal(map[string]any{
		"findings": []map[string]any{authFinding(1, "")},
	})
	if err != nil {
		t.Fatalf("marshal submit_findings args: %v", err)
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		switch callCount {
		case 1:
			return newChatCompletionHTTPResponseWithToolCalls(t, req, []openai.ToolCall{
				{
					ID:   "call-submit-findings",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      "submit_findings",
						Arguments: string(args),
					},
				},
			}), nil
		default:
			return newChatCompletionHTTPResponse(t, req, findingDiscoveryCompleteMarker), nil
		}
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunInitial, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "completed" {
		t.Fatalf("expected completed stage, got %q result=%q", saved.Status, saved.Result)
	}
	if strings.TrimSpace(saved.Result) != findingDiscoveryCompleteMarker {
		t.Fatalf("expected short final marker, got %q", saved.Result)
	}
	findings := decodeStageOutput(t, saved.OutputJSON)
	if len(findings) != 1 {
		t.Fatalf("expected one submitted finding, got %+v", findings)
	}
	if findings[0]["description"] != "authentication finding 1" {
		t.Fatalf("unexpected submitted finding: %+v", findings[0])
	}
	if findings[0]["origin"] != "initial" {
		t.Fatalf("expected initial origin, got %+v", findings[0])
	}
	if callCount < 2 {
		t.Fatalf("expected at least tool call round plus final marker, got %d calls", callCount)
	}
}

func TestRunAIScanRevalidationUsesSubmittedReviewsForFinalOutput(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	original := authFinding(1, "")
	original["poc_http"] = "GET /auth/1 original"
	task := &model.Task{
		ID:       "task-auth-submit-reviews",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		OutputJSON: mustMarshalTestJSON(t, []map[string]any{original}),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	args, err := json.Marshal(map[string]any{
		"reviews": []map[string]any{
			{
				"finding_index":       0,
				"verification_status": summaryStatusConfirmed,
				"reviewed_severity":   "medium",
				"verification_reason": "sink remains reachable",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal submit_reviews args: %v", err)
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		switch callCount {
		case 1:
			return newChatCompletionHTTPResponseWithToolCalls(t, req, []openai.ToolCall{
				{
					ID:   "call-submit-reviews",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      "submit_reviews",
						Arguments: string(args),
					},
				},
			}), nil
		default:
			return newChatCompletionHTTPResponse(t, req, revalidationCompleteMarker), nil
		}
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunRevalidate, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "completed" {
		t.Fatalf("expected completed stage, got %q result=%q", saved.Status, saved.Result)
	}
	if strings.TrimSpace(saved.Result) != revalidationCompleteMarker {
		t.Fatalf("expected short final marker, got %q", saved.Result)
	}
	findings := decodeStageOutput(t, saved.OutputJSON)
	if len(findings) != 1 {
		t.Fatalf("expected one reviewed finding, got %+v", findings)
	}
	finding := findings[0]
	if got := finding["verification_status"]; got != summaryStatusConfirmed {
		t.Fatalf("expected confirmed status, got %v", got)
	}
	if got := finding["reviewed_severity"]; got != "MEDIUM" {
		t.Fatalf("expected reviewed severity MEDIUM, got %v", got)
	}
	if got := finding["description"]; got != "authentication finding 1" {
		t.Fatalf("expected original description to be preserved, got %v", got)
	}
	if got := finding["poc_http"]; got != "GET /auth/1 original" {
		t.Fatalf("expected original poc_http to be preserved, got %v", got)
	}
	if callCount < 2 {
		t.Fatalf("expected at least tool call round plus final marker, got %d calls", callCount)
	}
}

func TestRunAIScanRevalidationIncompleteFailsStageAndPreservesOutput(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-revalidation-incomplete",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		OutputJSON: mustMarshalTestJSON(t, []map[string]any{authFinding(1, "")}),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		return newChatCompletionHTTPResponse(t, req, `[]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunRevalidate, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed stage after incomplete revalidation, got %q", saved.Status)
	}
	if !strings.Contains(saved.Result, "Result processing error:") {
		t.Fatalf("expected processing error marker in stage result, got %q", saved.Result)
	}
	if !strings.Contains(saved.Result, "Revalidation incomplete: 1 existing findings, 0 reviewed, 1 unreviewed.") {
		t.Fatalf("expected incomplete revalidation error in stage result, got %q", saved.Result)
	}
	if !containsAll(saved.Result, "Result processing diagnostics:", "parsed_candidate:", "repair_output:", "repair_error:") {
		t.Fatalf("expected revalidation diagnostics in stage result, got %q", saved.Result)
	}
	if strings.TrimSpace(string(saved.OutputJSON)) == "[]" {
		t.Fatalf("expected old stage output_json to be preserved instead of [], got %s", string(saved.OutputJSON))
	}
	if !strings.Contains(string(saved.OutputJSON), `"authentication finding 1"`) {
		t.Fatalf("expected old finding output_json to remain, got %s", string(saved.OutputJSON))
	}
}

func TestRunAIScanRevalidationMissingStatusRepairFailureStoresDiagnostics(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-revalidation-diagnostics",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		OutputJSON: mustMarshalTestJSON(t, []map[string]any{authFinding(1, "")}),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	badReview := `[{"finding_index":0,"reviewed_severity":"HIGH","verification_reason":"missing status"}]`
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponse(t, req, badReview), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunRevalidate, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	if callCount != 2 {
		t.Fatalf("expected main response plus one revalidation repair attempt, got %d", callCount)
	}
	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed stage, got %q", saved.Status)
	}
	if !containsAll(saved.Result,
		"Result processing error:",
		`invalid verification_status`,
		"Result processing diagnostics:",
		"parsed_candidate:",
		`"reviewed_severity": "HIGH"`,
		"repair_output:",
		badReview,
		"repair_error:",
	) {
		t.Fatalf("expected raw, parsed candidate, repair output, and repair error in result, got %q", saved.Result)
	}
}

func TestRunAIScanAbnormalFinishReasonFailsWithoutRepair(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:         "task-revalidation-length",
		Status:     "pending",
		BasePath:   t.TempDir(),
		OutputJSON: json.RawMessage(`[{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "completed",
		OutputJSON: mustMarshalTestJSON(t, []map[string]any{authFinding(1, "")}),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionHTTPResponseWithFinishReason(t, req, `[{"finding_index":0`, openai.FinishReason("length")), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunRevalidate, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	if callCount != 1 {
		t.Fatalf("expected abnormal finish to fail without JSON repair, got %d calls", callCount)
	}
	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed stage, got %q", saved.Status)
	}
	if !containsAll(saved.Result, "Result processing error:", `finish_reason="length"`, `[{"finding_index":0`) {
		t.Fatalf("expected length finish reason and raw partial output in result, got %q", saved.Result)
	}
	if strings.Contains(saved.Result, "Result processing diagnostics:") {
		t.Fatalf("abnormal finish should not enter JSON repair diagnostics, got %q", saved.Result)
	}
	if !strings.Contains(string(saved.OutputJSON), `"authentication finding 1"`) {
		t.Fatalf("expected old output_json to remain, got %s", string(saved.OutputJSON))
	}
}

func TestRunAIScanAbnormalFinishReasonReportsSubmittedFindings(t *testing.T) {
	useLargeWindowTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	useTestAIConfig(t, config.AIConfig{Model: "gpt-4o-mini"})
	setupScannerTaskDB(t)

	task := &model.Task{
		ID:       "task-auth-length-after-submit",
		Status:   "pending",
		BasePath: t.TempDir(),
		OutputJSON: json.RawMessage(`[
			{"method":"GET","path":"/auth/1","source":"routes.go","description":"auth route"}
		]`),
	}
	stage := &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		Status:     "pending",
		OutputJSON: json.RawMessage(`[]`),
	}
	if err := database.DB.Create(task).Error; err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := database.DB.Create(stage).Error; err != nil {
		t.Fatalf("create stage: %v", err)
	}

	args, err := json.Marshal(map[string]any{
		"findings": []map[string]any{authFinding(1, "")},
	})
	if err != nil {
		t.Fatalf("marshal submit_findings args: %v", err)
	}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		switch callCount {
		case 1:
			return newChatCompletionHTTPResponseWithToolCalls(t, req, []openai.ToolCall{
				{
					ID:   "call-submit-findings",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      "submit_findings",
						Arguments: string(args),
					},
				},
			}), nil
		default:
			return newChatCompletionHTTPResponseWithFinishReason(t, req, `[{"type":"Authentication"`, openai.FinishReason("length")), nil
		}
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	ExecuteAIScan(task, "auth", StageRunInitial, false, ScanExecutionOptions{
		ManageTaskStatus:  false,
		PersistTaskRecord: false,
	})

	var saved model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, "auth").First(&saved).Error; err != nil {
		t.Fatalf("load saved stage: %v", err)
	}
	if saved.Status != "failed" {
		t.Fatalf("expected failed stage, got %q", saved.Status)
	}
	if !containsAll(saved.Result, "Result processing error:", `finish_reason="length"`, "1 submitted finding(s) were preserved in runtime state") {
		t.Fatalf("expected submitted finding count in abnormal finish result, got %q", saved.Result)
	}
	if strings.Contains(saved.Result, "Result processing diagnostics:") {
		t.Fatalf("abnormal finish should not enter JSON repair diagnostics, got %q", saved.Result)
	}
	if callCount < 2 {
		t.Fatalf("expected at least submit round plus abnormal finish, got %d calls", callCount)
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

const (
	summaryStatusConfirmed = "confirmed"
	summaryStatusUncertain = "uncertain"
	summaryStatusRejected  = "rejected"
)

func authStageWithFindings(t *testing.T, task *model.Task, findings ...map[string]any) *model.TaskStage {
	t.Helper()
	if findings == nil {
		findings = []map[string]any{}
	}
	return &model.TaskStage{
		TaskID:     task.ID,
		Name:       "auth",
		OutputJSON: mustMarshalTestJSON(t, findings),
	}
}

func authFinding(id int, verificationStatus string) map[string]any {
	finding := map[string]any{
		"type":     "Authentication",
		"subtype":  fmt.Sprintf("Session Issue %d", id),
		"severity": "HIGH",
		"location": map[string]any{
			"file":     fmt.Sprintf("internal/auth/%d.go", id),
			"line":     id,
			"function": fmt.Sprintf("HandleAuth%d", id),
		},
		"trigger": map[string]any{
			"method":    "GET",
			"path":      fmt.Sprintf("/auth/%d", id),
			"parameter": "session",
		},
		"affected_endpoints": []any{fmt.Sprintf("GET /auth/%d", id)},
		"description":        fmt.Sprintf("authentication finding %d", id),
	}
	if strings.TrimSpace(verificationStatus) != "" {
		finding["verification_status"] = verificationStatus
		finding["reviewed_severity"] = "HIGH"
		finding["verification_reason"] = fmt.Sprintf("review reason %d", id)
	}
	return finding
}

func mustMarshalTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return data
}

func decodeStageOutput(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var findings []map[string]any
	if err := json.Unmarshal(raw, &findings); err != nil {
		t.Fatalf("decode stage output: %v", err)
	}
	return findings
}

func newChatCompletionHTTPResponseWithFinishReason(t *testing.T, req *http.Request, content string, finishReason openai.FinishReason) *http.Response {
	t.Helper()

	if requestUsesStream(t, req) {
		return newChatCompletionStreamResponse(t, openai.ChatCompletionStreamResponse{
			ID:                "chatcmpl-stream-test",
			Object:            "chat.completion.chunk",
			Created:           time.Now().Unix(),
			Model:             "gpt-4o-mini",
			SystemFingerprint: "fp-test",
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index: 0,
					Delta: openai.ChatCompletionStreamChoiceDelta{
						Role:    openai.ChatMessageRoleAssistant,
						Content: content,
					},
					FinishReason: finishReason,
				},
			},
		})
	}

	payload := openai.ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "gpt-4o-mini",
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleAssistant,
					Content: content,
				},
				FinishReason: finishReason,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     1,
			CompletionTokens: 1,
			TotalTokens:      2,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal chat completion response: %v", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

func newChatCompletionHTTPResponseWithToolCalls(t *testing.T, req *http.Request, toolCalls []openai.ToolCall) *http.Response {
	t.Helper()

	if requestUsesStream(t, req) {
		streamCalls := make([]openai.ToolCall, 0, len(toolCalls))
		for i, call := range toolCalls {
			index := i
			call.Index = &index
			streamCalls = append(streamCalls, call)
		}
		return newChatCompletionStreamResponse(t, openai.ChatCompletionStreamResponse{
			ID:                "chatcmpl-stream-test",
			Object:            "chat.completion.chunk",
			Created:           time.Now().Unix(),
			Model:             "gpt-4o-mini",
			SystemFingerprint: "fp-test",
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index: 0,
					Delta: openai.ChatCompletionStreamChoiceDelta{
						Role:      openai.ChatMessageRoleAssistant,
						ToolCalls: streamCalls,
					},
					FinishReason: openai.FinishReasonToolCalls,
				},
			},
		})
	}

	payload := openai.ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   "gpt-4o-mini",
		Choices: []openai.ChatCompletionChoice{
			{
				Index: 0,
				Message: openai.ChatCompletionMessage{
					Role:      openai.ChatMessageRoleAssistant,
					ToolCalls: toolCalls,
				},
				FinishReason: openai.FinishReasonToolCalls,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     1,
			CompletionTokens: 1,
			TotalTokens:      2,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal chat completion response: %v", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}
