package scanner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codescan/internal/config"

	"github.com/sashabaranov/go-openai"
)

func assertDurationNear(t *testing.T, got, want time.Duration) {
	t.Helper()

	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 2*time.Second {
		t.Fatalf("expected duration near %s, got %s", want, got)
	}
}

func useTestAIConfig(t *testing.T, cfg config.AIConfig) {
	t.Helper()

	prev := config.AI
	config.AI = cfg
	t.Cleanup(func() {
		config.AI = prev
	})
}

func TestPrepareChatCompletionRequestAppliesThinkingToMainScan(t *testing.T) {
	useTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled:             true,
			Effort:              "medium",
			MaxCompletionTokens: 8192,
		},
	})

	req := prepareChatCompletionRequest(openai.ChatCompletionRequest{
		Model: "gpt-5.4",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionPurposeMainScan)

	if req.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning effort to be applied, got %q", req.ReasoningEffort)
	}
	if req.MaxCompletionTokens != 8192 {
		t.Fatalf("expected max completion tokens to be applied, got %d", req.MaxCompletionTokens)
	}
}

func TestPrepareChatCompletionRequestOmitsZeroMaxCompletionTokens(t *testing.T) {
	useTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled: true,
			Effort:  "high",
		},
	})

	req := prepareChatCompletionRequest(openai.ChatCompletionRequest{
		Model: "gpt-5.4",
	}, chatCompletionPurposeMainScan)

	if req.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning effort to be applied, got %q", req.ReasoningEffort)
	}
	if req.MaxCompletionTokens != 0 {
		t.Fatalf("expected zero max completion tokens to be omitted, got %d", req.MaxCompletionTokens)
	}
}

func TestPrepareChatCompletionRequestSkipsAuxiliaryByDefault(t *testing.T) {
	useTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled:             true,
			Effort:              "high",
			MaxCompletionTokens: 8192,
		},
	})

	req := prepareChatCompletionRequest(openai.ChatCompletionRequest{
		Model: "gpt-5.4",
	}, chatCompletionPurposeAuxiliary)

	if req.ReasoningEffort != "" || req.MaxCompletionTokens != 0 {
		t.Fatalf("expected auxiliary request to skip thinking params by default, got effort=%q max=%d", req.ReasoningEffort, req.MaxCompletionTokens)
	}
}

func TestPrepareChatCompletionRequestCanApplyThinkingToAuxiliary(t *testing.T) {
	useTestAIConfig(t, config.AIConfig{
		Thinking: config.AIThinkingConfig{
			Enabled:             true,
			Effort:              "high",
			MaxCompletionTokens: 20000,
			ApplyToAuxiliary:    true,
		},
	})

	req := prepareChatCompletionRequest(openai.ChatCompletionRequest{
		Model: "gpt-5.4",
	}, chatCompletionPurposeAuxiliary)

	if req.ReasoningEffort != "high" || req.MaxCompletionTokens != 20000 {
		t.Fatalf("expected auxiliary request to include thinking params when enabled, got effort=%q max=%d", req.ReasoningEffort, req.MaxCompletionTokens)
	}
}

func TestSanitizeMessageHistoryDropsOrphanTool(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{Role: openai.ChatMessageRoleTool, Content: "orphan", ToolCallID: "call-1"},
	}

	sanitized, stats := sanitizeMessageHistory(messages)

	if len(sanitized) != 1 {
		t.Fatalf("expected 1 message after sanitization, got %d", len(sanitized))
	}
	if sanitized[0].Role != openai.ChatMessageRoleUser {
		t.Fatalf("expected user message to remain, got role %q", sanitized[0].Role)
	}
	if stats.droppedToolMessages != 1 {
		t.Fatalf("expected 1 dropped tool message, got %d", stats.droppedToolMessages)
	}
	if stats.droppedIncompleteRuns != 0 {
		t.Fatalf("expected 0 dropped incomplete rounds, got %d", stats.droppedIncompleteRuns)
	}
}

func TestSanitizeMessageHistoryDropsIncompleteToolRound(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{ID: "call-1", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
				{ID: "call-2", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "partial", ToolCallID: "call-1"},
		{Role: openai.ChatMessageRoleUser, Content: "next"},
	}

	sanitized, stats := sanitizeMessageHistory(messages)

	if len(sanitized) != 2 {
		t.Fatalf("expected incomplete tool round to be removed, got %d messages", len(sanitized))
	}
	if sanitized[0].Role != openai.ChatMessageRoleUser || sanitized[1].Role != openai.ChatMessageRoleUser {
		t.Fatalf("expected only user messages to remain, got roles %q and %q", sanitized[0].Role, sanitized[1].Role)
	}
	if stats.droppedIncompleteRuns != 1 {
		t.Fatalf("expected 1 dropped incomplete round, got %d", stats.droppedIncompleteRuns)
	}
	if stats.droppedToolMessages != 0 {
		t.Fatalf("expected 0 orphan tool drops, got %d", stats.droppedToolMessages)
	}
}

func TestSanitizeMessageHistoryKeepsCompleteToolRound(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{ID: "call-1", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "result", ToolCallID: "call-1"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
	}

	sanitized, stats := sanitizeMessageHistory(messages)

	if len(sanitized) != len(messages) {
		t.Fatalf("expected complete tool round to remain, got %d messages", len(sanitized))
	}
	if stats.changed() {
		t.Fatalf("expected no sanitization changes, got %+v", stats)
	}
}

func TestSanitizeMessageHistoryRepairsMalformedToolRound(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{Type: openai.ToolTypeFunction},
				{ID: "call-list", Function: openai.FunctionCall{Name: "list_files", Arguments: `{"path":"."}`}},
				{ID: "call-manifest", Function: openai.FunctionCall{Name: "query_manifest", Arguments: `{"stage":"init","limit":20}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "empty-placeholder"},
		{Role: openai.ChatMessageRoleTool, Content: "list-result", ToolCallID: "call-list"},
		{Role: openai.ChatMessageRoleTool, Content: "manifest-result", ToolCallID: "call-manifest"},
		{Role: openai.ChatMessageRoleAssistant, Content: "next"},
	}

	sanitized, stats := sanitizeMessageHistory(messages)

	if len(sanitized) != 5 {
		t.Fatalf("expected repaired round plus trailing assistant, got %d messages", len(sanitized))
	}
	if len(sanitized[1].ToolCalls) != 2 {
		t.Fatalf("expected malformed placeholder tool call to be removed, got %+v", sanitized[1].ToolCalls)
	}
	if sanitized[2].ToolCallID != "call-list" || sanitized[3].ToolCallID != "call-manifest" {
		t.Fatalf("expected valid tool outputs to remain matched, got %+v", sanitized)
	}
	if stats.droppedToolMessages != 1 {
		t.Fatalf("expected orphan placeholder tool output to be dropped, got %d", stats.droppedToolMessages)
	}
	if stats.toolCallStats.droppedInvalidToolCalls != 1 {
		t.Fatalf("expected one invalid assistant tool call to be dropped, got %+v", stats.toolCallStats)
	}
	if stats.droppedIncompleteRuns != 0 {
		t.Fatalf("expected repaired round to remain complete, got %+v", stats)
	}
}

func TestNormalizeToolCallsGeneratesIDsForMissingOrDuplicateIDs(t *testing.T) {
	toolCalls := []openai.ToolCall{
		{Function: openai.FunctionCall{Name: "list_files", Arguments: `{"path":"."}`}},
		{ID: "call-1", Function: openai.FunctionCall{Name: "query_manifest", Arguments: `{"stage":"init"}`}},
		{ID: "call-1", Function: openai.FunctionCall{Name: "query_routes", Arguments: `{"limit":10}`}},
	}

	normalized, stats := normalizeToolCalls(toolCalls)

	if len(normalized) != 3 {
		t.Fatalf("expected all valid tool calls to remain, got %+v", normalized)
	}
	if normalized[0].ID == "" || normalized[0].ID == "call-1" {
		t.Fatalf("expected missing id to be generated, got %+v", normalized[0])
	}
	if normalized[1].ID != "call-1" {
		t.Fatalf("expected existing unique id to remain, got %+v", normalized[1])
	}
	if normalized[2].ID == "" || normalized[2].ID == "call-1" || normalized[2].ID == normalized[0].ID {
		t.Fatalf("expected duplicate id to be regenerated uniquely, got %+v", normalized[2])
	}
	if stats.generatedToolCallIDs != 2 {
		t.Fatalf("expected two generated ids, got %+v", stats)
	}
}

func TestSelectCompressionWindowAlignsToToolRoundStart(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{Role: openai.ChatMessageRoleAssistant, Content: "thinking"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{ID: "call-1", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
				{ID: "call-2", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "result-a", ToolCallID: "call-1"},
		{Role: openai.ChatMessageRoleTool, Content: "result-b", ToolCallID: "call-2"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
		{Role: openai.ChatMessageRoleUser, Content: "next"},
	}

	selection := selectCompressionWindow(messages, 3)

	if selection.candidateStart != 4 {
		t.Fatalf("expected candidate start 4, got %d", selection.candidateStart)
	}
	if selection.candidateRole != openai.ChatMessageRoleTool {
		t.Fatalf("expected candidate role tool, got %q", selection.candidateRole)
	}
	if selection.adjustedStart != 2 {
		t.Fatalf("expected adjusted start 2, got %d", selection.adjustedStart)
	}
	if selection.usedFullHistory {
		t.Fatal("expected aligned tail to be used without full-history fallback")
	}
	if selection.tailSanitizeStats.changed() {
		t.Fatalf("expected aligned tail to remain valid, got %+v", selection.tailSanitizeStats)
	}
	if len(selection.selectedMessages) != 5 {
		t.Fatalf("expected 5 selected messages, got %d", len(selection.selectedMessages))
	}
	if len(selection.selectedMessages[0].ToolCalls) != 2 {
		t.Fatalf("expected selected window to start with assistant tool calls, got %+v", selection.selectedMessages[0])
	}
	if selection.selectedMessages[1].ToolCallID != "call-1" || selection.selectedMessages[2].ToolCallID != "call-2" {
		t.Fatalf("expected both tool outputs to remain in selected window, got %+v", selection.selectedMessages)
	}
}

func TestSelectCompressionWindowKeepsValidBoundary(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{ID: "call-1", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "result-a", ToolCallID: "call-1"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
		{Role: openai.ChatMessageRoleUser, Content: "next"},
	}

	selection := selectCompressionWindow(messages, 2)

	if selection.candidateStart != 3 || selection.adjustedStart != 3 {
		t.Fatalf("expected valid boundary to remain unchanged, got candidate=%d adjusted=%d", selection.candidateStart, selection.adjustedStart)
	}
	if selection.usedFullHistory {
		t.Fatal("expected no full-history fallback on valid boundary")
	}
	if len(selection.selectedMessages) != 2 {
		t.Fatalf("expected 2 selected messages, got %d", len(selection.selectedMessages))
	}
	if selection.selectedMessages[0].Role != openai.ChatMessageRoleAssistant || selection.selectedMessages[1].Role != openai.ChatMessageRoleUser {
		t.Fatalf("expected assistant/user tail, got %+v", selection.selectedMessages)
	}
}

func TestSelectCompressionWindowFallsBackToSanitizedFullHistory(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{ID: "call-1", Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
			},
		},
		{Role: openai.ChatMessageRoleTool, Content: "mismatch", ToolCallID: "call-2"},
		{Role: openai.ChatMessageRoleAssistant, Content: "done"},
	}

	selection := selectCompressionWindow(messages, 2)

	if !selection.usedFullHistory {
		t.Fatal("expected invalid tail to trigger full-history fallback")
	}
	if !selection.tailSanitizeStats.changed() {
		t.Fatalf("expected tail sanitization to detect invalid tail, got %+v", selection.tailSanitizeStats)
	}
	if len(selection.selectedMessages) != 2 {
		t.Fatalf("expected fallback to sanitized full history with 2 messages, got %d", len(selection.selectedMessages))
	}
	if selection.selectedMessages[0].Role != openai.ChatMessageRoleUser || selection.selectedMessages[1].Role != openai.ChatMessageRoleAssistant {
		t.Fatalf("expected fallback history to keep prompt and final assistant, got %+v", selection.selectedMessages)
	}
}

func TestIsRetryableAIError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retryable bool
	}{
		{
			name:      "timeout",
			err:       context.DeadlineExceeded,
			retryable: true,
		},
		{
			name:      "protocol 400",
			err:       errors.New("error, status code: 400, status: 400 Bad Request, message: No tool call found for function call output"),
			retryable: false,
		},
		{
			name:      "rate limit 429",
			err:       errors.New("error, status code: 429, status: 429 Too Many Requests"),
			retryable: true,
		},
		{
			name:      "cdn 524",
			err:       new524Error(),
			retryable: true,
		},
		{
			name:      "http2 internal stream error",
			err:       errors.New("error, stream error: stream ID 1877; INTERNAL_ERROR; received from peer"),
			retryable: true,
		},
		{
			name:      "http2 refused stream",
			err:       errors.New("stream error: stream ID 11; REFUSED_STREAM; received from peer"),
			retryable: true,
		},
		{
			name:      "http2 goaway",
			err:       errors.New("http2: server sent GOAWAY and closed the connection"),
			retryable: true,
		},
		{
			name:      "unexpected eof",
			err:       io.ErrUnexpectedEOF,
			retryable: true,
		},
		{
			name:      "connection reset",
			err:       errors.New("read tcp 127.0.0.1:12345->127.0.0.1:443: connection reset by peer"),
			retryable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetryableAIError(tc.err)
			if got != tc.retryable {
				t.Fatalf("expected retryable=%v, got %v", tc.retryable, got)
			}
		})
	}
}

func TestIsAIRequestTimeoutRecognizes524(t *testing.T) {
	if !isAIRequestTimeout(new524Error()) {
		t.Fatal("expected 524 to be treated as a timeout-like AI error")
	}
}

func TestCreateChatCompletionWithRetryIncreasesDeadlineAfterTimeoutLikeErrors(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	deadlines := []time.Duration{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("expected request context deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		callCount++
		if callCount < 3 {
			return nil, new524Error()
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with retry: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("expected final response content, got %+v", resp.Choices)
	}
	if len(deadlines) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(deadlines))
	}
	assertDurationNear(t, deadlines[0], 180*time.Second)
	assertDurationNear(t, deadlines[1], 240*time.Second)
	assertDurationNear(t, deadlines[2], 300*time.Second)
}

func TestCreateChatCompletionWithRetryRemembersRaisedTimeoutAcrossCalls(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	prevAIConfig := config.AI
	config.AI = config.AIConfig{
		BaseURL: "https://unit-test.example/v1",
		Model:   "gpt-4o-mini",
	}
	t.Cleanup(func() {
		config.AI = prevAIConfig
	})

	deadlines := []time.Duration{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("expected request context deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		callCount++
		if callCount < 3 {
			return nil, new524Error()
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	for i := 0; i < 2; i++ {
		resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
			Model: "gpt-4o-mini",
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "hello"},
			},
		}, chatCompletionRetryHooks{})
		if err != nil {
			t.Fatalf("call %d create chat completion with retry: %v", i+1, err)
		}
		if resp.Choices[0].Message.Content != "ok" {
			t.Fatalf("call %d expected final response content, got %+v", i+1, resp.Choices)
		}
	}

	if len(deadlines) != 4 {
		t.Fatalf("expected 4 attempts across two calls, got %d", len(deadlines))
	}
	assertDurationNear(t, deadlines[0], 180*time.Second)
	assertDurationNear(t, deadlines[1], 240*time.Second)
	assertDurationNear(t, deadlines[2], 300*time.Second)
	assertDurationNear(t, deadlines[3], 300*time.Second)
}

func TestCreateChatCompletionWithRetryDoesNotRememberNonTimeoutRetryableErrors(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	prevAIConfig := config.AI
	config.AI = config.AIConfig{
		BaseURL: "https://unit-test.example/v1",
		Model:   "gpt-4o-mini",
	}
	t.Cleanup(func() {
		config.AI = prevAIConfig
	})

	deadlines := []time.Duration{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("expected request context deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		callCount++
		if callCount == 1 {
			return nil, errors.New("error, status code: 503, status: 503 Service Unavailable")
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	for i := 0; i < 2; i++ {
		resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
			Model: "gpt-4o-mini",
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "hello"},
			},
		}, chatCompletionRetryHooks{})
		if err != nil {
			t.Fatalf("call %d create chat completion with retry: %v", i+1, err)
		}
		if resp.Choices[0].Message.Content != "ok" {
			t.Fatalf("call %d expected final response content, got %+v", i+1, resp.Choices)
		}
	}

	if len(deadlines) != 3 {
		t.Fatalf("expected 3 attempts across two calls, got %d", len(deadlines))
	}
	assertDurationNear(t, deadlines[0], 180*time.Second)
	assertDurationNear(t, deadlines[1], 180*time.Second)
	assertDurationNear(t, deadlines[2], 180*time.Second)
}

func TestCreateChatCompletionWithRetryDoesNotLetRememberedTimeoutExceedMax(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	prevAIConfig := config.AI
	config.AI = config.AIConfig{
		BaseURL: "https://unit-test.example/v1",
		Model:   "gpt-4o-mini",
	}
	t.Cleanup(func() {
		config.AI = prevAIConfig
	})

	key := aiRequestTimeoutMemoryKey(config.AI.BaseURL, "gpt-4o-mini")
	maxTimeout := maxAIRequestTimeout(defaultAIRequestInitialTimeout, defaultAIRequestTimeoutStep, 3)
	aiRequestTimeoutMemory.store(key, maxTimeout, defaultAIRequestInitialTimeout, maxTimeout)

	deadlines := []time.Duration{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("expected request context deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		callCount++
		if callCount == 1 {
			return nil, new524Error()
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with retry: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("expected final response content, got %+v", resp.Choices)
	}
	if len(deadlines) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(deadlines))
	}
	assertDurationNear(t, deadlines[0], maxTimeout)
	assertDurationNear(t, deadlines[1], maxTimeout)
}

func TestCreateChatCompletionWithRetryKeepsRememberedTimeoutAfterNonRetryableError(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	prevAIConfig := config.AI
	config.AI = config.AIConfig{
		BaseURL: "https://unit-test.example/v1",
		Model:   "gpt-4o-mini",
	}
	t.Cleanup(func() {
		config.AI = prevAIConfig
	})

	key := aiRequestTimeoutMemoryKey(config.AI.BaseURL, "gpt-4o-mini")
	maxTimeout := maxAIRequestTimeout(defaultAIRequestInitialTimeout, defaultAIRequestTimeoutStep, 3)
	aiRequestTimeoutMemory.store(key, maxTimeout, defaultAIRequestInitialTimeout, maxTimeout)

	deadlines := []time.Duration{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok {
			t.Fatal("expected request context deadline")
		}
		deadlines = append(deadlines, time.Until(deadline))
		callCount++
		if callCount == 1 {
			return nil, errors.New("error, status code: 400, status: 400 Bad Request")
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected non-retryable error")
	}

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello again"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with retry after non-retryable error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("expected final response content, got %+v", resp.Choices)
	}
	if len(deadlines) != 2 {
		t.Fatalf("expected 2 attempts across both calls, got %d", len(deadlines))
	}
	assertDurationNear(t, deadlines[0], maxTimeout)
	assertDurationNear(t, deadlines[1], maxTimeout)
}

func TestCreateChatCompletionWithRetryStopsOnNonRetryableError(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return nil, errors.New("error, status code: 400, status: 400 Bad Request, message: No tool call found for function call output")
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected non-retryable error")
	}
	if callCount != 1 {
		t.Fatalf("expected non-retryable error to stop after one attempt, got %d", callCount)
	}
}

func TestCreateChatCompletionWithRetryReturnsLastErrorAfterMaxAttempts(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return nil, new524Error()
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected final retryable error after max attempts")
	}
	if !strings.Contains(err.Error(), "524") {
		t.Fatalf("expected final error to preserve 524 context, got %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected max-attempt exhaustion after 3 tries, got %d", callCount)
	}
}

func TestCreateChatCompletionWithRetryDoesNotMutateRequestAfterRateLimit(t *testing.T) {
	useTestAIRequestPolicy(t, 2)

	bodies := []string{}
	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		bodies = append(bodies, string(body))

		statusCode := http.StatusTooManyRequests
		status := "429 Too Many Requests"
		message := "Too Many Requests"
		if callCount == 2 {
			statusCode = http.StatusInternalServerError
			status = "500 Internal Server Error"
			message = "Rate limit exceeded"
		}

		payload := fmt.Sprintf(`{"error":{"message":%q,"type":"rate_limit_reached","param":null,"code":"rate_limit_reached"}}`, message)
		return &http.Response{
			StatusCode: statusCode,
			Status:     status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(payload)),
		}, nil
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
		Tools: Tools[:1],
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected retry exhaustion on rate-limit responses")
	}
	if !strings.Contains(err.Error(), "status code: 500") || !strings.Contains(err.Error(), "Rate limit exceeded") {
		t.Fatalf("expected final error to preserve HTTP 500 rate-limit context, got %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected two request attempts, got %d", len(bodies))
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("expected retry after rate limit to resend the same request body\nfirst:  %s\nsecond: %s", bodies[0], bodies[1])
	}
}

func TestCreateChatCompletionWithRetryStripsUnsupportedReasoningContentOnce(t *testing.T) {
	useTestAIRequestPolicy(t, 1)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		bodyText := string(body)
		req.Body = io.NopCloser(strings.NewReader(bodyText))

		if callCount == 1 {
			if !strings.Contains(bodyText, `"reasoning_content"`) {
				t.Fatalf("expected first request to include reasoning_content, got %s", bodyText)
			}
			return nil, errors.New("error, status code: 400, status: 400 Bad Request, message: unknown field `reasoning_content`")
		}
		if strings.Contains(bodyText, `"reasoning_content"`) {
			t.Fatalf("expected compatibility retry to strip reasoning_content, got %s", bodyText)
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleAssistant, Content: "previous", ReasoningContent: "thinking"},
			{Role: openai.ChatMessageRoleUser, Content: "next"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with reasoning compatibility retry: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("expected stripped retry to recover response, got %+v", resp.Choices)
	}
	if callCount != 2 {
		t.Fatalf("expected exactly one compatibility retry, got %d calls", callCount)
	}
}

func TestCreateChatCompletionWithRetryStripsUnsupportedThinkingParamsOnce(t *testing.T) {
	useTestAIRequestPolicy(t, 1)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		bodyText := string(body)
		req.Body = io.NopCloser(strings.NewReader(bodyText))

		if callCount == 1 {
			if !strings.Contains(bodyText, `"reasoning_effort"`) {
				t.Fatalf("expected first request to include reasoning_effort, got %s", bodyText)
			}
			if !strings.Contains(bodyText, `"max_completion_tokens"`) {
				t.Fatalf("expected first request to include max_completion_tokens, got %s", bodyText)
			}
			return nil, errors.New("error, status code: 400, status: 400 Bad Request, message: unknown parameter `reasoning_effort`")
		}
		if strings.Contains(bodyText, `"reasoning_effort"`) || strings.Contains(bodyText, `"max_completion_tokens"`) {
			t.Fatalf("expected compatibility retry to strip thinking params, got %s", bodyText)
		}
		return newChatCompletionHTTPResponse(t, req, "ok"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model:               "gpt-5.4",
		ReasoningEffort:     "high",
		MaxCompletionTokens: 8192,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "next"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with thinking compatibility retry: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("expected stripped retry to recover response, got %+v", resp.Choices)
	}
	if callCount != 2 {
		t.Fatalf("expected exactly one compatibility retry, got %d calls", callCount)
	}
}

func TestCreateChatCompletionWithRetryDoesNotStripWhenReasoningContentRequired(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return nil, errors.New("error, status code: 400, status: 400 Bad Request, message: thinking mode must be passed back with reasoning_content")
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "deepseek-reasoner",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleAssistant, Content: "previous", ReasoningContent: "thinking"},
			{Role: openai.ChatMessageRoleUser, Content: "next"},
		},
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected missing reasoning_content protocol error")
	}
	if callCount != 1 {
		t.Fatalf("expected required reasoning_content error to stop without strip retry, got %d calls", callCount)
	}
}

func TestChatCompletionStreamAccumulatorMergesToolCalls(t *testing.T) {
	accumulator := newChatCompletionStreamAccumulator("gpt-4o-mini")
	firstToolIndex := 0

	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		ID:      "chatcmpl-stream",
		Object:  "chat.completion.chunk",
		Created: 123,
		Model:   "gpt-4o-mini",
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Role: openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ToolCall{
						{
							Index: &firstToolIndex,
							ID:    "call-1",
							Type:  openai.ToolTypeFunction,
							Function: openai.FunctionCall{
								Name:      "read",
								Arguments: "{\"path\"",
							},
						},
					},
				},
			},
		},
	})
	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					ToolCalls: []openai.ToolCall{
						{
							Index: &firstToolIndex,
							Function: openai.FunctionCall{
								Name:      "_file",
								Arguments: ":\"a.go\"}",
							},
						},
					},
				},
				FinishReason: openai.FinishReasonToolCalls,
			},
		},
	})

	resp, err := accumulator.finalize()
	if err != nil {
		t.Fatalf("finalize accumulated stream response: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one aggregated choice, got %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if msg.Role != openai.ChatMessageRoleAssistant {
		t.Fatalf("expected assistant role, got %q", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected one aggregated tool call, got %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected merged tool name, got %q", msg.ToolCalls[0].Function.Name)
	}
	if msg.ToolCalls[0].Function.Arguments != "{\"path\":\"a.go\"}" {
		t.Fatalf("expected merged tool arguments, got %q", msg.ToolCalls[0].Function.Arguments)
	}
	if resp.Choices[0].FinishReason != openai.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", resp.Choices[0].FinishReason)
	}
}

func TestChatCompletionStreamAccumulatorMergesReasoningContent(t *testing.T) {
	accumulator := newChatCompletionStreamAccumulator("deepseek-reasoner")

	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Role:             openai.ChatMessageRoleAssistant,
					ReasoningContent: "first ",
				},
			},
		},
	})
	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					ReasoningContent: "second",
					Content:          "answer",
				},
				FinishReason: openai.FinishReasonStop,
			},
		},
	})

	resp, err := accumulator.finalize()
	if err != nil {
		t.Fatalf("finalize accumulated stream response: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.ReasoningContent != "first second" {
		t.Fatalf("expected merged reasoning content, got %q", msg.ReasoningContent)
	}
	if msg.Content != "answer" {
		t.Fatalf("expected final content to be preserved, got %q", msg.Content)
	}
}

func TestChatCompletionStreamAccumulatorCompactsNonZeroBasedToolCallIndexes(t *testing.T) {
	accumulator := newChatCompletionStreamAccumulator("gpt-4o-mini")
	firstToolIndex := 1
	secondToolIndex := 2

	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					Role: openai.ChatMessageRoleAssistant,
					ToolCalls: []openai.ToolCall{
						{
							Index: &firstToolIndex,
							ID:    "call-list",
							Type:  openai.ToolTypeFunction,
							Function: openai.FunctionCall{
								Name:      "list",
								Arguments: "{\"path\"",
							},
						},
						{
							Index: &secondToolIndex,
							ID:    "call-manifest",
							Type:  openai.ToolTypeFunction,
							Function: openai.FunctionCall{
								Name:      "query_",
								Arguments: "{\"stage\":\"init\"",
							},
						},
					},
				},
			},
		},
	})
	accumulator.addChunk(openai.ChatCompletionStreamResponse{
		Choices: []openai.ChatCompletionStreamChoice{
			{
				Index: 0,
				Delta: openai.ChatCompletionStreamChoiceDelta{
					ToolCalls: []openai.ToolCall{
						{
							Index: &firstToolIndex,
							Function: openai.FunctionCall{
								Name:      "_files",
								Arguments: ":\".\"}",
							},
						},
						{
							Index: &secondToolIndex,
							Function: openai.FunctionCall{
								Name:      "manifest",
								Arguments: ",\"limit\":20}",
							},
						},
					},
				},
				FinishReason: openai.FinishReasonToolCalls,
			},
		},
	})

	resp, err := accumulator.finalize()
	if err != nil {
		t.Fatalf("finalize accumulated stream response: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one aggregated choice, got %d", len(resp.Choices))
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) != 2 {
		t.Fatalf("expected sparse external indexes to compact into two tool calls, got %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].ID != "call-list" || msg.ToolCalls[0].Function.Name != "list_files" || msg.ToolCalls[0].Function.Arguments != "{\"path\":\".\"}" {
		t.Fatalf("expected first tool call to merge without empty placeholder, got %+v", msg.ToolCalls[0])
	}
	if msg.ToolCalls[1].ID != "call-manifest" || msg.ToolCalls[1].Function.Name != "query_manifest" || msg.ToolCalls[1].Function.Arguments != "{\"stage\":\"init\",\"limit\":20}" {
		t.Fatalf("expected second tool call to merge without empty placeholder, got %+v", msg.ToolCalls[1])
	}
}

func TestCreateChatCompletionWithRetryRequestsStreamUsageAndPreservesIt(t *testing.T) {
	useTestAIRequestPolicy(t, 1)

	sawIncludeUsage := false
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		if !requestUsesStream(t, req) {
			t.Fatal("expected streaming request")
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		sawIncludeUsage = strings.Contains(string(body), `"include_usage":true`) || strings.Contains(string(body), `"include_usage": true`)

		return newChatCompletionStreamResponse(
			t,
			openai.ChatCompletionStreamResponse{
				ID:                "chatcmpl-stream-usage",
				Object:            "chat.completion.chunk",
				Created:           123,
				Model:             "gpt-4o-mini",
				SystemFingerprint: "fp-test",
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Role:    openai.ChatMessageRoleAssistant,
							Content: "usage-aware",
						},
						FinishReason: openai.FinishReasonStop,
					},
				},
			},
			openai.ChatCompletionStreamResponse{
				ID:      "chatcmpl-stream-usage",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []openai.ChatCompletionStreamChoice{},
				Usage: &openai.Usage{
					PromptTokens:     11,
					CompletionTokens: 3,
					TotalTokens:      14,
				},
			},
		), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with retry: %v", err)
	}
	if !sawIncludeUsage {
		t.Fatal("expected stream request to include stream_options.include_usage=true")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "usage-aware" {
		t.Fatalf("expected streamed content to be preserved, got %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens != 11 || resp.Usage.CompletionTokens != 3 || resp.Usage.TotalTokens != 14 {
		t.Fatalf("expected streamed usage to be preserved, got %+v", resp.Usage)
	}
}

func TestCreateChatCompletionWithRetryFallsBackToNonStreamWhenStreamingUnsupported(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	streamRequests := 0
	fallbackRequests := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if requestUsesStream(t, req) {
			streamRequests++
			return newChatCompletionResponse(t, "fallback-ok"), nil
		}
		fallbackRequests++
		return newChatCompletionResponse(t, "fallback-ok"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with retry: %v", err)
	}
	if resp.Choices[0].Message.Content != "fallback-ok" {
		t.Fatalf("expected fallback content, got %+v", resp.Choices)
	}
	if callCount != 2 {
		t.Fatalf("expected one stream attempt plus one fallback request, got %d calls", callCount)
	}
	if streamRequests != 1 || fallbackRequests != 1 {
		t.Fatalf("expected 1 stream request and 1 fallback request, got stream=%d fallback=%d", streamRequests, fallbackRequests)
	}
}

func TestCreateChatCompletionWithRetryDoesNotRetryAfterPartialStreamError(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++

		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		body := strings.Join([]string{
			`data: {"id":"chatcmpl-stream","object":"chat.completion.chunk","created":123,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":""}]}`,
			``,
			`data: {"choices":[{"index":0,"delta":{bad json},"finish_reason":""}]}`,
			``,
		}, "\n")

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}))

	_, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err == nil {
		t.Fatal("expected partial stream error")
	}
	if callCount != 1 {
		t.Fatalf("expected partial stream failure to abort without retry, got %d calls", callCount)
	}
}

func TestCreateChatCompletionWithRetryRetriesStreamTransportError(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("error, stream error: stream ID 1877; INTERNAL_ERROR; received from peer")
		}
		return newChatCompletionHTTPResponse(t, req, "recovered"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion after transport retry: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected stream transport error to retry once, got %d calls", callCount)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("expected retry to recover a complete response, got %+v", resp.Choices)
	}
}

func TestCreateChatCompletionWithRetryAcceptsTerminalStreamTimeout(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionStreamResponseWithTerminalError(t, context.DeadlineExceeded, openai.ChatCompletionStreamResponse{
			ID:                "chatcmpl-stream-terminal",
			Object:            "chat.completion.chunk",
			Created:           123,
			Model:             "gpt-4o-mini",
			SystemFingerprint: "fp-test",
			Choices: []openai.ChatCompletionStreamChoice{
				{
					Index: 0,
					Delta: openai.ChatCompletionStreamChoiceDelta{
						Role:    openai.ChatMessageRoleAssistant,
						Content: "complete",
					},
					FinishReason: openai.FinishReasonStop,
				},
			},
		}), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with terminal stream timeout: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected terminal stream timeout to finalize without retry, got %d calls", callCount)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "complete" {
		t.Fatalf("expected finalized content, got %+v", resp.Choices[0].Message)
	}
	if resp.Choices[0].FinishReason != openai.FinishReasonStop {
		t.Fatalf("expected stop finish reason, got %q", resp.Choices[0].FinishReason)
	}
}

func TestCreateChatCompletionWithRetryAcceptsTerminalToolCallStreamTimeout(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	firstToolIndex := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return newChatCompletionStreamResponseWithTerminalError(t, context.DeadlineExceeded,
			openai.ChatCompletionStreamResponse{
				ID:      "chatcmpl-stream-tool-timeout",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Role: openai.ChatMessageRoleAssistant,
							ToolCalls: []openai.ToolCall{
								{
									Index: &firstToolIndex,
									ID:    "call-1",
									Type:  openai.ToolTypeFunction,
									Function: openai.FunctionCall{
										Name:      "read",
										Arguments: "{\"path\"",
									},
								},
							},
						},
					},
				},
			},
			openai.ChatCompletionStreamResponse{
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							ToolCalls: []openai.ToolCall{
								{
									Index: &firstToolIndex,
									Function: openai.FunctionCall{
										Name:      "_file",
										Arguments: ":\"a.go\"}",
									},
								},
							},
						},
						FinishReason: openai.FinishReasonToolCalls,
					},
				},
			},
		), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion with terminal tool-call stream timeout: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected terminal tool-call stream timeout to finalize without retry, got %d calls", callCount)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].FinishReason != openai.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected one merged tool call, got %+v", resp.Choices[0].Message.ToolCalls)
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("expected merged tool name, got %q", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Arguments != "{\"path\":\"a.go\"}" {
		t.Fatalf("expected merged tool arguments, got %q", resp.Choices[0].Message.ToolCalls[0].Function.Arguments)
	}
}

func TestCreateChatCompletionWithRetryRetriesAfterPartialStreamTimeout(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return newChatCompletionStreamResponseWithTerminalError(t, context.DeadlineExceeded, openai.ChatCompletionStreamResponse{
				ID:      "chatcmpl-stream-partial-timeout",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Role:    openai.ChatMessageRoleAssistant,
							Content: "partial",
						},
					},
				},
			}), nil
		}
		return newChatCompletionHTTPResponse(t, req, "recovered"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion after partial stream timeout retry: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected partial stream timeout to retry once, got %d calls", callCount)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("expected retry to recover a complete response, got %+v", resp.Choices[0].Message)
	}
}

func TestCreateChatCompletionWithRetryRetriesAfterPartialStreamTransportError(t *testing.T) {
	useTestAIRequestPolicy(t, 3)

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount == 1 {
			return newChatCompletionStreamResponseWithTerminalError(t, errors.New("stream error: stream ID 1877; INTERNAL_ERROR; received from peer"), openai.ChatCompletionStreamResponse{
				ID:      "chatcmpl-stream-partial-transport",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "gpt-4o-mini",
				Choices: []openai.ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: openai.ChatCompletionStreamChoiceDelta{
							Role:    openai.ChatMessageRoleAssistant,
							Content: "partial",
						},
					},
				},
			}), nil
		}
		return newChatCompletionHTTPResponse(t, req, "recovered"), nil
	}))

	resp, err := createChatCompletionWithRetry(context.Background(), client, openai.ChatCompletionRequest{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: "hello"},
		},
	}, chatCompletionRetryHooks{})
	if err != nil {
		t.Fatalf("create chat completion after partial stream transport retry: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected partial stream transport error to retry once, got %d calls", callCount)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected one choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("expected retry to recover a complete response, got %+v", resp.Choices[0].Message)
	}
}

func TestRepairJSONRetriesTimeoutLikeErrors(t *testing.T) {
	useTestAIRequestPolicy(t, 3)
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}

	callCount := 0
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount < 3 {
			return nil, new524Error()
		}
		return newChatCompletionHTTPResponse(t, req, `[{"method":"GET","path":"/health","source":"api.go","description":"健康检查"}]`), nil
	}))
	useTestAIClientFactory(t, func() *openai.Client {
		return client
	})

	result, err := RepairJSON("raw text [{bad]", "init")
	if err != nil {
		t.Fatalf("repair json: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected RepairJSON to retry 3 times, got %d", callCount)
	}
	if !strings.Contains(result, `"path":"/health"`) {
		t.Fatalf("expected repaired JSON output, got %q", result)
	}
}

func TestResetConversationMessagesIncludesEvidenceIndex(t *testing.T) {
	messages := resetConversationMessages("prompt", "summary", "- ev-1 | api/user.go | lines 10-20 | 512 bytes")

	if len(messages) != 4 {
		t.Fatalf("expected 4 messages after reset with evidence index, got %d", len(messages))
	}
	if messages[2].Role != openai.ChatMessageRoleUser {
		t.Fatalf("expected evidence index message to be a user message, got %q", messages[2].Role)
	}
	if got := messages[2].Content; got == "" || !containsAll(got, "PRESERVED READ_FILE EVIDENCE INDEX", "ev-1", "api/user.go") {
		t.Fatalf("expected evidence index content in reset message, got %q", got)
	}
	if got := messages[3].Content; !containsAll(got, "get_evidence", "summary above") {
		t.Fatalf("expected continuation guidance to mention get_evidence, got %q", got)
	}
}

func TestAppendChineseNarrativeRulesIncludesLanguageRequirements(t *testing.T) {
	prompt := appendChineseNarrativeRules("BASE PROMPT", "rce")

	if !containsAll(prompt, "LANGUAGE REQUIREMENTS", "Simplified Chinese", "CURRENT STAGE: rce") {
		t.Fatalf("expected Chinese narrative guidance to be appended, got %q", prompt)
	}
}

func TestRepairChineseNarrativeRulesIncludesLanguageRequirements(t *testing.T) {
	prompt := repairChineseNarrativeRules("logic")

	if !containsAll(prompt, "LANGUAGE REQUIREMENTS", "Simplified Chinese", "CURRENT STAGE: logic") {
		t.Fatalf("expected repair guidance to preserve Chinese narrative rules, got %q", prompt)
	}
}

func TestEvidenceStorePreservesReadFileSnippet(t *testing.T) {
	store := newEvidenceStore(64)
	record := store.addReadFileEvidence("internal/service/foo.go", 10, 30, strings.Repeat("x", 80))

	if record.ID == "" {
		t.Fatal("expected evidence ID to be assigned")
	}
	if !record.Truncated {
		t.Fatal("expected oversized evidence payload to be marked truncated")
	}
	if _, ok := store.get(record.ID); !ok {
		t.Fatalf("expected evidence %q to be retrievable", record.ID)
	}

	index := store.compactIndex(5)
	if !containsAll(index, record.ID, "internal/service/foo.go", "lines 10-30") {
		t.Fatalf("expected compact index to include stored evidence, got %q", index)
	}

	payload := formatEvidencePayload(record)
	if !containsAll(payload, "EVIDENCE "+record.ID, "Range: lines 10-30", "Truncated: true") {
		t.Fatalf("expected formatted evidence payload to include metadata, got %q", payload)
	}
}

func TestDisplayToolPathReturnsRelativeProjectPath(t *testing.T) {
	basePath := t.TempDir()
	resolvedPath := filepath.Join(basePath, "internal", "service", "foo.go")

	got := displayToolPath(basePath, resolvedPath)

	if got != "internal/service/foo.go" {
		t.Fatalf("expected project-relative path, got %q", got)
	}
}

func TestDisplayToolPathFallsBackToSafeFileNameForOutsidePath(t *testing.T) {
	root := t.TempDir()
	basePath := filepath.Join(root, "project")
	resolvedPath := filepath.Join(root, "other", "secret.go")

	got := displayToolPath(basePath, resolvedPath)

	if got != "secret.go" {
		t.Fatalf("expected safe filename fallback, got %q", got)
	}
	if strings.Contains(got, "..") || filepath.IsAbs(got) {
		t.Fatalf("expected non-absolute, non-escaping fallback path, got %q", got)
	}
}

func TestCalculateContextBytesIncludesToolCallArgumentsAndName(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: "prompt"},
		{
			Role: openai.ChatMessageRoleAssistant,
			ToolCalls: []openai.ToolCall{
				{Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
			},
		},
	}

	got := calculateContextBytes(messages)
	want := len("prompt") + len("read_file") + len(`{"path":"a.go"}`)
	if got != want {
		t.Fatalf("expected %d context bytes, got %d", want, got)
	}
}

func TestEstimateContextTokensIncludesMessageOverhead(t *testing.T) {
	messages := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleUser, Content: strings.Repeat("x", 8)},
		{Role: openai.ChatMessageRoleAssistant, Content: strings.Repeat("y", 4)},
	}

	got := estimateContextTokens(messages)
	want := 3 + len(messages)*4
	if got != want {
		t.Fatalf("expected estimated tokens %d, got %d", want, got)
	}
}

func TestIsContextOverflowAIErrorRecognizesPromptTooLong(t *testing.T) {
	if !isContextOverflowAIError(errors.New("status code: 413, prompt is too long for context window")) {
		t.Fatal("expected context overflow error to be recognized")
	}
	if isContextOverflowAIError(errors.New("status code: 429, rate limit")) {
		t.Fatal("expected non-context error to be ignored")
	}
}

func containsAll(input string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(input, part) {
			return false
		}
	}
	return true
}
