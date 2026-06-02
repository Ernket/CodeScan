package scanner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"

	"github.com/sashabaranov/go-openai"
)

func useTestScannerConfig(t *testing.T) {
	t.Helper()
	config.Scanner, _ = config.NormalizeScannerConfig(config.ScannerConfig{
		ContextCompression: config.ContextCompressionConfig{
			ContextWindowTokens:    13024,
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

type testHTTPDoer func(req *http.Request) (*http.Response, error)

func (d testHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	return d(req)
}

type errAfterDataReadCloser struct {
	data []byte
	err  error
}

func (r *errAfterDataReadCloser) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	if r.err == nil {
		return 0, io.EOF
	}
	return 0, r.err
}

func (r *errAfterDataReadCloser) Close() error {
	return nil
}

func newTestAIClient(doer openai.HTTPDoer) *openai.Client {
	cfg := openai.DefaultConfig("test-key")
	cfg.BaseURL = "http://example.com/v1"
	cfg.HTTPClient = doer
	return openai.NewClientWithConfig(cfg)
}

func useTestAIRequestPolicy(t *testing.T, maxAttempts int) {
	t.Helper()

	prevInitialTimeout := aiRequestInitialTimeout
	prevTimeoutStep := aiRequestTimeoutStep
	prevMaxAttempts := aiRequestMaxAttempts
	prevSleep := aiRequestSleep

	aiRequestInitialTimeout = defaultAIRequestInitialTimeout
	aiRequestTimeoutStep = defaultAIRequestTimeoutStep
	aiRequestMaxAttempts = maxAttempts
	aiRequestSleep = func(ctx context.Context, delay time.Duration) bool {
		return true
	}
	aiRequestTimeoutMemory.reset()

	t.Cleanup(func() {
		aiRequestInitialTimeout = prevInitialTimeout
		aiRequestTimeoutStep = prevTimeoutStep
		aiRequestMaxAttempts = prevMaxAttempts
		aiRequestSleep = prevSleep
		aiRequestTimeoutMemory.reset()
	})
}

func useTestAIClientFactory(t *testing.T, factory func() *openai.Client) {
	t.Helper()

	prevFactory := aiClientFactory
	aiClientFactory = factory
	t.Cleanup(func() {
		aiClientFactory = prevFactory
	})
}

func new524Error() error {
	return errors.New("error, status code: 524, status: 524 A Timeout Occurred")
}

func requestUsesStream(t *testing.T, req *http.Request) bool {
	t.Helper()

	body := readRequestBody(t, req)
	return strings.Contains(body, `"stream":true`) || strings.Contains(body, `"stream": true`)
}

func readRequestBody(t *testing.T, req *http.Request) string {
	t.Helper()

	if req == nil || req.Body == nil {
		return ""
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return string(body)
}

func newChatCompletionResponse(t *testing.T, content string) *http.Response {
	t.Helper()

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
				FinishReason: openai.FinishReasonStop,
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

func newChatCompletionStreamResponse(t *testing.T, chunks ...openai.ChatCompletionStreamResponse) *http.Response {
	t.Helper()

	var body strings.Builder
	for _, chunk := range chunks {
		data, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal chat completion stream response: %v", err)
		}
		body.WriteString("data: ")
		body.Write(data)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")

	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body.String())),
	}
}

func newChatCompletionStreamResponseWithTerminalError(t *testing.T, err error, chunks ...openai.ChatCompletionStreamResponse) *http.Response {
	t.Helper()

	var body strings.Builder
	for _, chunk := range chunks {
		data, marshalErr := json.Marshal(chunk)
		if marshalErr != nil {
			t.Fatalf("marshal chat completion stream response: %v", marshalErr)
		}
		body.WriteString("data: ")
		body.Write(data)
		body.WriteString("\n\n")
	}

	header := make(http.Header)
	header.Set("Content-Type", "text/event-stream")
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body: &errAfterDataReadCloser{
			data: []byte(body.String()),
			err:  err,
		},
	}
}

func newChatCompletionHTTPResponse(t *testing.T, req *http.Request, content string) *http.Response {
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
					FinishReason: openai.FinishReasonStop,
				},
			},
		})
	}

	return newChatCompletionResponse(t, content)
}

func newTestTask(t *testing.T) *model.Task {
	t.Helper()
	base := t.TempDir()
	task := &model.Task{
		ID:       "task-test",
		BasePath: base,
	}
	if err := os.MkdirAll(task.RuntimeRootPath(), 0o755); err != nil {
		t.Fatalf("create runtime root: %v", err)
	}
	return task
}

func TestPreservedReadFileArtifactLogUsesRelativePath(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	resolvedPath := filepath.Join(task.BasePath, "internal", "service", "scanner.go")
	record, err := session.createArtifact(
		"read_file",
		"read_file",
		displayToolPath(task.BasePath, resolvedPath),
		12,
		34,
		"package scanner",
	)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	logLine := fmt.Sprintf("Preserved read_file artifact %s for %s (%s).", record.ID, record.Path, formatLineRange(record.StartLine, record.EndLine))

	if !strings.Contains(logLine, "internal/service/scanner.go") {
		t.Fatalf("expected log line to include relative path, got %q", logLine)
	}
	if strings.Contains(logLine, task.BasePath) || strings.Contains(logLine, filepath.ToSlash(task.BasePath)) {
		t.Fatalf("expected log line to avoid absolute base path, got %q", logLine)
	}
}

func TestSelectResumableRuntimeStageChoosesNewestPausedOrRunningState(t *testing.T) {
	task := newTestTask(t)

	writeState := func(stage, status string, updatedAt time.Time) {
		stageDir := task.StageRuntimePath(stage)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			t.Fatalf("create stage dir: %v", err)
		}
		state := runtimeState{
			Version:   1,
			TaskID:    task.ID,
			Stage:     stage,
			Status:    status,
			UpdatedAt: updatedAt,
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatalf("marshal state: %v", err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, runtimeStateFile), data, 0o644); err != nil {
			t.Fatalf("write state: %v", err)
		}
	}

	writeState("init", runtimeStatusPaused, time.Now().Add(-2*time.Minute))
	writeState("auth", runtimeStatusRunning, time.Now())

	stage, err := selectResumableRuntimeStage(task)
	if err != nil {
		t.Fatalf("select resumable stage: %v", err)
	}
	if stage != "auth" {
		t.Fatalf("expected auth to win, got %q", stage)
	}
}

func TestActiveMessagesIncludePreservedSegmentAfterBoundary(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "old-a", nil); err != nil {
		t.Fatalf("append old-a: %v", err)
	}
	oldA := session.transcript[len(session.transcript)-1]
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindNormal, "old-b", nil); err != nil {
		t.Fatalf("append old-b: %v", err)
	}
	oldB := session.transcript[len(session.transcript)-1]
	if err := session.appendSynthetic(openai.ChatMessageRoleSystem, runtimeKindCompactBoundary, "", &compactBoundary{
		Source: "test",
		HeadID: oldA.ID,
		TailID: oldB.ID,
	}); err != nil {
		t.Fatalf("append boundary: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindCompactSummary, "summary", nil); err != nil {
		t.Fatalf("append summary: %v", err)
	}

	active := session.activeMessages()
	if len(active) != 3 {
		t.Fatalf("expected preserved segment + summary, got %d messages", len(active))
	}
	if active[0].Content != "summary" || active[1].Content != "old-a" || active[2].Content != "old-b" {
		t.Fatalf("unexpected active message sequence: %+v", active)
	}
}

func TestReconcileActiveBoundaryFallsBackToLatestTranscriptBoundary(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "old", nil); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleSystem, runtimeKindCompactBoundary, "", &compactBoundary{Source: "test"}); err != nil {
		t.Fatalf("append boundary: %v", err)
	}
	boundaryID := session.transcript[len(session.transcript)-1].ID
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindCompactSummary, "summary", nil); err != nil {
		t.Fatalf("append summary: %v", err)
	}

	session.state.ActiveBoundaryID = "missing-boundary"
	if err := session.saveState(); err != nil {
		t.Fatalf("save broken state: %v", err)
	}

	reloaded, err := loadScanSession(task, "auth", "prompt")
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if reloaded.state.ActiveBoundaryID != boundaryID {
		t.Fatalf("expected active boundary to recover to %q, got %q", boundaryID, reloaded.state.ActiveBoundaryID)
	}
	active := reloaded.activeMessages()
	if len(active) != 1 || active[0].Content != "summary" {
		t.Fatalf("expected old history to be pruned after recovered boundary, got %+v", active)
	}
}

func TestAppendChatMessagePersistsReasoningContent(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	msg := openai.ChatCompletionMessage{
		Role:             openai.ChatMessageRoleAssistant,
		Content:          "final answer",
		ReasoningContent: "private thinking trace",
	}
	if err := session.appendChatMessage(msg); err != nil {
		t.Fatalf("append chat message: %v", err)
	}
	persistedID := session.transcript[len(session.transcript)-1].ID
	if session.transcript[len(session.transcript)-1].ReasoningContent != msg.ReasoningContent {
		t.Fatalf("expected transcript reasoning content to persist, got %+v", session.transcript[len(session.transcript)-1])
	}

	reloaded, err := loadScanSession(task, "auth", "prompt")
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	entries := reloaded.buildChatEntries()
	for _, entry := range entries {
		if entry.Message.ID == persistedID {
			if entry.Chat.ReasoningContent != msg.ReasoningContent {
				t.Fatalf("expected rebuilt chat message reasoning content %q, got %q", msg.ReasoningContent, entry.Chat.ReasoningContent)
			}
			return
		}
	}
	t.Fatalf("expected persisted message %s in rebuilt entries", persistedID)
}

func TestTruncateLastToolMessageStoresArtifact(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendToolMessage("call-1", "grep_files", strings.Repeat("x", 120), ""); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	originalBytes, updatedBytes, changed, err := session.truncateLastToolMessage(40)
	if err != nil {
		t.Fatalf("truncate last tool message: %v", err)
	}
	if !changed {
		t.Fatal("expected truncateLastToolMessage to report a change")
	}
	if originalBytes <= updatedBytes {
		t.Fatalf("expected updated bytes to be smaller, got original=%d updated=%d", originalBytes, updatedBytes)
	}
	last := session.transcript[len(session.transcript)-1]
	if last.ArtifactID == "" {
		t.Fatal("expected truncated tool message to reference an artifact")
	}
	if _, ok := session.loadArtifact(last.ArtifactID); !ok {
		t.Fatalf("expected artifact %q to be retrievable", last.ArtifactID)
	}
}

func TestApplyMicrocompactClearsOlderToolResultsOnly(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	oldRound := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{
			{ID: "call-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
		},
	}
	if err := session.appendChatMessage(oldRound); err != nil {
		t.Fatalf("append old assistant round: %v", err)
	}
	if err := session.appendToolMessage("call-1", "read_file", "older result", ""); err != nil {
		t.Fatalf("append old tool result: %v", err)
	}
	oldToolID := session.transcript[len(session.transcript)-1].ID

	newRound := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{
			{ID: "call-2", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "grep_files", Arguments: `{"pattern":"TODO"}`}},
		},
	}
	if err := session.appendChatMessage(newRound); err != nil {
		t.Fatalf("append new assistant round: %v", err)
	}
	if err := session.appendToolMessage("call-2", "grep_files", "new result", ""); err != nil {
		t.Fatalf("append new tool result: %v", err)
	}
	newToolID := session.transcript[len(session.transcript)-1].ID

	entries, err := session.applyMicrocompact(session.buildChatEntries(), nil)
	if err != nil {
		t.Fatalf("apply microcompact: %v", err)
	}
	if _, ok := session.state.Microcompact.ClearedMessages[oldToolID]; !ok {
		t.Fatal("expected older tool result to be microcompacted")
	}
	if _, ok := session.state.Microcompact.ClearedMessages[newToolID]; ok {
		t.Fatal("expected most recent tool round to remain intact")
	}
	foundPlaceholder := false
	for _, entry := range entries {
		if entry.Message.ID == oldToolID && strings.Contains(entry.Chat.Content, "get_artifact") {
			foundPlaceholder = true
		}
	}
	if !foundPlaceholder {
		t.Fatal("expected returned chat entries to contain an artifact recovery placeholder")
	}
}

func TestMaybeApplyMicrocompactForPromptTokensSkipsBelowThreshold(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	oldToolID := appendMicrocompactToolRound(t, session, "call-1", "read_file", `{"path":"a.go"}`, "older result")
	newToolID := appendMicrocompactToolRound(t, session, "call-2", "grep_files", `{"pattern":"TODO"}`, "new result")

	entries, triggered, err := session.maybeApplyMicrocompactForPromptTokens(openai.Usage{PromptTokens: 99}, 100, nil)
	if err != nil {
		t.Fatalf("maybe apply microcompact: %v", err)
	}
	if triggered {
		t.Fatal("expected microcompact trigger to stay inactive below token threshold")
	}
	if len(session.state.Microcompact.ClearedMessages) != 0 {
		t.Fatalf("expected no microcompact records below token threshold, got %+v", session.state.Microcompact.ClearedMessages)
	}
	assertEntryContent(t, entries, oldToolID, "older result")
	assertEntryContent(t, entries, newToolID, "new result")
}

func TestMaybeApplyMicrocompactForPromptTokensClearsAtThreshold(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	oldToolID := appendMicrocompactToolRound(t, session, "call-1", "read_file", `{"path":"a.go"}`, "older result")
	newToolID := appendMicrocompactToolRound(t, session, "call-2", "grep_files", `{"pattern":"TODO"}`, "new result")

	entries, triggered, err := session.maybeApplyMicrocompactForPromptTokens(openai.Usage{PromptTokens: 100}, 100, nil)
	if err != nil {
		t.Fatalf("maybe apply microcompact: %v", err)
	}
	if !triggered {
		t.Fatal("expected microcompact trigger at token threshold")
	}
	if _, ok := session.state.Microcompact.ClearedMessages[oldToolID]; !ok {
		t.Fatal("expected older tool result to be microcompacted at token threshold")
	}
	if _, ok := session.state.Microcompact.ClearedMessages[newToolID]; ok {
		t.Fatal("expected latest tool round to remain intact at token threshold")
	}
	assertEntryContains(t, entries, oldToolID, "get_artifact")
	assertEntryContent(t, entries, newToolID, "new result")
}

func TestMaybeApplyMicrocompactForPromptTokensSkipsWithoutUsage(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	oldToolID := appendMicrocompactToolRound(t, session, "call-1", "read_file", `{"path":"a.go"}`, "older result")
	appendMicrocompactToolRound(t, session, "call-2", "grep_files", `{"pattern":"TODO"}`, "new result")

	entries, triggered, err := session.maybeApplyMicrocompactForPromptTokens(openai.Usage{}, 100, nil)
	if err != nil {
		t.Fatalf("maybe apply microcompact: %v", err)
	}
	if triggered {
		t.Fatal("expected microcompact trigger to stay inactive without API usage")
	}
	if len(session.state.Microcompact.ClearedMessages) != 0 {
		t.Fatalf("expected no microcompact records without usage, got %+v", session.state.Microcompact.ClearedMessages)
	}
	assertEntryContent(t, entries, oldToolID, "older result")
}

func appendMicrocompactToolRound(t *testing.T, session *scanSession, callID, toolName, arguments, result string) string {
	t.Helper()
	round := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{
			{ID: callID, Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: toolName, Arguments: arguments}},
		},
	}
	if err := session.appendChatMessage(round); err != nil {
		t.Fatalf("append assistant round %s: %v", callID, err)
	}
	if err := session.appendToolMessage(callID, toolName, result, ""); err != nil {
		t.Fatalf("append tool result %s: %v", callID, err)
	}
	return session.transcript[len(session.transcript)-1].ID
}

func assertEntryContent(t *testing.T, entries []chatEntry, id, want string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Message.ID == id {
			if entry.Chat.Content != want {
				t.Fatalf("expected entry %s content %q, got %q", id, want, entry.Chat.Content)
			}
			return
		}
	}
	t.Fatalf("expected entry %s to be present", id)
}

func assertEntryContains(t *testing.T, entries []chatEntry, id, want string) {
	t.Helper()
	for _, entry := range entries {
		if entry.Message.ID == id {
			if !strings.Contains(entry.Chat.Content, want) {
				t.Fatalf("expected entry %s to contain %q, got %q", id, want, entry.Chat.Content)
			}
			return
		}
	}
	t.Fatalf("expected entry %s to be present", id)
}

func TestTrySessionMemoryCompactionKeepsOnlyMessagesAfterMemoryCursor(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "before-memory", nil); err != nil {
		t.Fatalf("append before-memory: %v", err)
	}
	beforeMemoryID := session.transcript[len(session.transcript)-1].ID
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindNormal, "after-memory", nil); err != nil {
		t.Fatalf("append after-memory: %v", err)
	}
	if err := session.writeMemory("# Memory\nKnown context"); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	session.state.LastMemoryMessageID = beforeMemoryID
	if err := session.saveState(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	root := session.buildChatEntries()[0]
	entries := session.buildChatEntries()[1:]
	summary, kept, ok := session.trySessionMemoryCompaction(entries, root, 10_000, 10_000)
	if !ok {
		t.Fatal("expected session memory compaction to activate")
	}
	if !strings.Contains(summary, "SESSION MEMORY SNAPSHOT") {
		t.Fatalf("expected memory-based summary, got %q", summary)
	}
	if len(kept) != 1 || kept[0].Chat.Content != "after-memory" {
		t.Fatalf("expected only post-memory messages to remain, got %+v", kept)
	}
}

func TestTrySessionMemoryCompactionSkipsWhenTailExceedsLimit(t *testing.T) {
	useTestScannerConfig(t)
	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "before-memory", nil); err != nil {
		t.Fatalf("append before-memory: %v", err)
	}
	beforeMemoryID := session.transcript[len(session.transcript)-1].ID
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindNormal, strings.Repeat("x", 500), nil); err != nil {
		t.Fatalf("append large tail: %v", err)
	}
	if err := session.writeMemory("# Memory\nKnown context"); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	session.state.LastMemoryMessageID = beforeMemoryID
	if err := session.saveState(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	chatEntries := session.buildChatEntries()
	_, _, ok := session.trySessionMemoryCompaction(chatEntries[1:], chatEntries[0], 0, 100)
	if ok {
		t.Fatal("expected session memory compaction to skip oversized tail")
	}
}

func TestFormatCompactSummaryKeepsOnlySummaryTag(t *testing.T) {
	got := formatCompactSummary("<analysis>scratch</analysis>\n<summary>\nDurable handoff\n</summary>")
	if got != "Durable handoff" {
		t.Fatalf("expected summary content only, got %q", got)
	}
}

func TestMaybeUpdateSessionMemorySendsOnlyDeltaAndClearsFailureState(t *testing.T) {
	useTestScannerConfig(t)
	useTestAIRequestPolicy(t, 1)
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}
	config.Scanner.SessionMemory.FailureCooldownSeconds = 60

	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "before-memory", nil); err != nil {
		t.Fatalf("append before-memory: %v", err)
	}
	beforeMemoryID := session.transcript[len(session.transcript)-1].ID
	if err := session.writeMemory("# Memory\nKnown context"); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	session.state.LastMemoryMessageID = beforeMemoryID
	session.state.ConsecutiveMemoryFailures = 2
	session.state.LastMemoryFailureAt = time.Now().Add(-2 * time.Minute)
	if err := session.saveState(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	toolRound := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{
			{ID: "call-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "read_file", Arguments: `{"path":"delta.go"}`}},
		},
	}
	if err := session.appendChatMessage(toolRound); err != nil {
		t.Fatalf("append tool round: %v", err)
	}
	if err := session.appendToolMessage("call-1", "read_file", "delta result", ""); err != nil {
		t.Fatalf("append tool message: %v", err)
	}
	lastDeltaID := session.transcript[len(session.transcript)-1].ID

	var requestContent string
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		if len(payload.Messages) != 1 {
			t.Fatalf("expected one message in memory update request, got %d", len(payload.Messages))
		}
		requestContent = payload.Messages[0].Content
		return newChatCompletionHTTPResponse(t, req, "# Updated Memory"), nil
	}))

	session.maybeUpdateSessionMemory(context.Background(), client, nil)

	if strings.Contains(requestContent, "before-memory") {
		t.Fatalf("expected session memory request to omit pre-cursor content, got %q", requestContent)
	}
	if !strings.Contains(requestContent, "delta result") || !strings.Contains(requestContent, `read_file {"path":"delta.go"}`) {
		t.Fatalf("expected delta content in memory update request, got %q", requestContent)
	}
	if session.state.LastMemoryMessageID != lastDeltaID {
		t.Fatalf("expected cursor to advance to %q, got %q", lastDeltaID, session.state.LastMemoryMessageID)
	}
	if session.state.ConsecutiveMemoryFailures != 0 {
		t.Fatalf("expected failure count reset, got %d", session.state.ConsecutiveMemoryFailures)
	}
	if !session.state.LastMemoryFailureAt.IsZero() {
		t.Fatalf("expected last failure time to reset, got %v", session.state.LastMemoryFailureAt)
	}
	memory, err := session.readMemory()
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	if strings.TrimSpace(memory) != "# Updated Memory" {
		t.Fatalf("expected updated memory content, got %q", memory)
	}
}

func TestMaybeUpdateSessionMemoryRetriesAndCoolsDownAfterFailure(t *testing.T) {
	useTestScannerConfig(t)
	useTestAIRequestPolicy(t, 3)
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}
	config.Scanner.SessionMemory.FailureCooldownSeconds = 60

	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	toolRound := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
		ToolCalls: []openai.ToolCall{
			{ID: "call-1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "grep_files", Arguments: `{"pattern":"TODO"}`}},
		},
	}
	if err := session.appendChatMessage(toolRound); err != nil {
		t.Fatalf("append tool round: %v", err)
	}
	if err := session.appendToolMessage("call-1", "grep_files", "delta result", ""); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	callCount := 0
	logs := []string{}
	logFunc := func(msg string) {
		logs = append(logs, msg)
	}
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return nil, context.DeadlineExceeded
	}))

	session.maybeUpdateSessionMemory(context.Background(), client, logFunc)

	if callCount != 3 {
		t.Fatalf("expected 3 total memory update attempts, got %d", callCount)
	}
	if session.state.ConsecutiveMemoryFailures != 1 {
		t.Fatalf("expected one consecutive memory failure, got %d", session.state.ConsecutiveMemoryFailures)
	}
	if session.state.LastMemoryFailureAt.IsZero() {
		t.Fatal("expected last failure time to be recorded")
	}

	retryLogs := 0
	for _, line := range logs {
		if strings.Contains(line, "Increasing request timeout to") {
			retryLogs++
		}
	}
	if retryLogs != 2 {
		t.Fatalf("expected 2 retry logs for 3 attempts, got %d (%v)", retryLogs, logs)
	}

	session.maybeUpdateSessionMemory(context.Background(), client, logFunc)
	session.maybeUpdateSessionMemory(context.Background(), client, logFunc)

	if callCount != 3 {
		t.Fatalf("expected cooldown to suppress new HTTP calls, got %d", callCount)
	}

	cooldownLogs := 0
	for _, line := range logs {
		if strings.Contains(line, "cooling down") {
			cooldownLogs++
		}
	}
	if cooldownLogs != 1 {
		t.Fatalf("expected exactly one cooldown log, got %d (%v)", cooldownLogs, logs)
	}
}

func TestCompressHistoryRetriesTimeoutLikeErrors(t *testing.T) {
	useTestScannerConfig(t)
	useTestAIRequestPolicy(t, 3)
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}

	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "finding-1", nil); err != nil {
		t.Fatalf("append finding-1: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindNormal, "finding-2", nil); err != nil {
		t.Fatalf("append finding-2: %v", err)
	}

	callCount := 0
	logs := []string{}
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		if callCount < 3 {
			return nil, new524Error()
		}
		return newChatCompletionHTTPResponse(t, req, "retried summary"), nil
	}))

	err = session.compressHistory(context.Background(), client, func(msg string) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("compress history: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 compression attempts, got %d", callCount)
	}
	if session.state.RollingSummary != "retried summary" {
		t.Fatalf("expected retried summary to persist, got %q", session.state.RollingSummary)
	}

	retryLogs := 0
	for _, line := range logs {
		if strings.Contains(line, "Increasing request timeout to") {
			retryLogs++
		}
	}
	if retryLogs != 2 {
		t.Fatalf("expected 2 timeout-growth logs, got %d (%v)", retryLogs, logs)
	}
}

func TestCompressHistoryFallsBackAfterRetryExhaustion(t *testing.T) {
	useTestScannerConfig(t)
	useTestAIRequestPolicy(t, 3)
	config.AI = config.AIConfig{Model: "gpt-4o-mini"}

	task := newTestTask(t)
	session, err := newScanSession(task, "auth", "prompt", true)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleAssistant, runtimeKindNormal, "finding-1", nil); err != nil {
		t.Fatalf("append finding-1: %v", err)
	}
	if err := session.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindNormal, "finding-2", nil); err != nil {
		t.Fatalf("append finding-2: %v", err)
	}

	callCount := 0
	logs := []string{}
	client := newTestAIClient(testHTTPDoer(func(req *http.Request) (*http.Response, error) {
		callCount++
		return nil, new524Error()
	}))

	err = session.compressHistory(context.Background(), client, func(msg string) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("compress history: %v", err)
	}
	if callCount != 3 {
		t.Fatalf("expected 3 compression attempts before fallback, got %d", callCount)
	}
	if !strings.Contains(session.state.RollingSummary, "Compression failed before a fresh summary could be produced.") {
		t.Fatalf("expected fallback summary after retry exhaustion, got %q", session.state.RollingSummary)
	}
	if !strings.Contains(session.state.RollingSummary, "524") {
		t.Fatalf("expected fallback summary to record final 524 error, got %q", session.state.RollingSummary)
	}

	foundFallbackLog := false
	for _, line := range logs {
		if strings.Contains(line, "Context compression failed; using fallback summary") {
			foundFallbackLog = true
			break
		}
	}
	if !foundFallbackLog {
		t.Fatalf("expected fallback log entry, got %v", logs)
	}
}
