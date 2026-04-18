package scanner

import (
	"context"
	"errors"
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

func containsAll(input string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(input, part) {
			return false
		}
	}
	return true
}
