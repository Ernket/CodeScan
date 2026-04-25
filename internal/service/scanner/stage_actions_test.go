package scanner

import (
	"net/http"
	"strings"
	"testing"

	"codescan/internal/config"
	"codescan/internal/model"

	"github.com/sashabaranov/go-openai"
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
