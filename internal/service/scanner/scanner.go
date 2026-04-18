package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"

	"github.com/sashabaranov/go-openai"
)

const (
	defaultAIRequestInitialTimeout = 180 * time.Second
	defaultAIRequestTimeoutStep    = 60 * time.Second
	defaultAIRequestMaxAttempts    = 10
)

var (
	aiClientFactory         = newAIClient
	aiRequestInitialTimeout = defaultAIRequestInitialTimeout
	aiRequestTimeoutStep    = defaultAIRequestTimeoutStep
	aiRequestMaxAttempts    = defaultAIRequestMaxAttempts
	aiRequestSleep          = sleepWithContext
	aiRequestTimeoutMemory  = newAIRequestTimeoutMemory()
)

type aiRequestTimeoutStore struct {
	mu        sync.RWMutex
	baselines map[string]time.Duration
}

func newAIRequestTimeoutMemory() *aiRequestTimeoutStore {
	return &aiRequestTimeoutStore{
		baselines: map[string]time.Duration{},
	}
}

func (s *aiRequestTimeoutStore) load(key string, fallback, max time.Duration) time.Duration {
	if s == nil {
		return clampAIRequestTimeout(fallback, fallback, max)
	}

	s.mu.RLock()
	timeout, ok := s.baselines[key]
	s.mu.RUnlock()
	if !ok {
		timeout = fallback
	}

	return clampAIRequestTimeout(timeout, fallback, max)
}

func (s *aiRequestTimeoutStore) store(key string, timeout, fallback, max time.Duration) {
	if s == nil || key == "" {
		return
	}

	timeout = clampAIRequestTimeout(timeout, fallback, max)

	s.mu.Lock()
	s.baselines[key] = timeout
	s.mu.Unlock()
}

func (s *aiRequestTimeoutStore) reset() {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.baselines = map[string]time.Duration{}
	s.mu.Unlock()
}

// newAIClient creates an OpenAI client from global config.
func newAIClient() *openai.Client {
	cfg := openai.DefaultConfig(config.AI.APIKey)
	cfg.BaseURL = config.AI.BaseURL
	return openai.NewClientWithConfig(cfg)
}

func extractJSON(input string) string {
	input = strings.TrimSpace(input)
	if json.Valid([]byte(input)) {
		return input
	}

	// 1. Try to find markdown code blocks first
	re := regexp.MustCompile("(?s)```(?:json)?\\s*(\\[.*?\\]|\\{.*?\\})\\s*```")
	matches := re.FindAllStringSubmatch(input, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(matches[i][1])
		if json.Valid([]byte(trimmed)) {
			return trimmed
		}
	}

	// 2. Adaptive Search: Iterate over all '[' to find the valid JSON array
	// This handles cases like: "Here is the result: [ ... ] Some footer text"
	// or "Ignore this [link] and look at this: [ ... ]"
	for i := 0; i < len(input); i++ {
		if input[i] == '[' {
			// Try to pair with the last ']' first (most likely case)
			lastClose := strings.LastIndex(input, "]")
			if lastClose > i {
				candidate := input[i : lastClose+1]
				if json.Valid([]byte(candidate)) {
					return candidate
				}
				// If strictly greedy fails, we might need to search backwards from end?
				// But usually the JSON is the largest block.
			}
		}
	}

	// 3. Fallback for Object if array not found (though prompt asks for array)
	for i := 0; i < len(input); i++ {
		if input[i] == '{' {
			lastClose := strings.LastIndex(input, "}")
			if lastClose > i {
				candidate := input[i : lastClose+1]
				if json.Valid([]byte(candidate)) {
					return candidate
				}
			}
		}
	}

	return input
}

func calculateContextBytes(messages []openai.ChatCompletionMessage) int {
	total := 0
	for _, msg := range messages {
		total += messageContextBytes(msg)
	}
	return total
}

func messageContextBytes(msg openai.ChatCompletionMessage) int {
	total := len(msg.Content)
	for _, toolCall := range msg.ToolCalls {
		total += len(toolCall.Function.Name)
		total += len(toolCall.Function.Arguments)
	}
	return total
}

func summarizeMessageTailBytes(messages []openai.ChatCompletionMessage, maxEntries int) string {
	if len(messages) == 0 {
		return "none"
	}
	start := len(messages) - maxEntries
	if start < 0 {
		start = 0
	}
	parts := make([]string, 0, len(messages)-start)
	for _, msg := range messages[start:] {
		parts = append(parts, fmt.Sprintf("%s=%dB", msg.Role, messageContextBytes(msg)))
	}
	return strings.Join(parts, ", ")
}

const (
	maxPreservedEvidenceBytes = 64 * 1024
	evidenceIndexLimit        = 8
)

type preservedEvidence struct {
	ID         string
	Path       string
	StartLine  int
	EndLine    int
	Payload    string
	Bytes      int
	Truncated  bool
	CapturedAt time.Time
}

type evidenceStore struct {
	records  map[string]preservedEvidence
	order    []string
	nextID   int
	maxBytes int
}

func newEvidenceStore(maxBytes int) *evidenceStore {
	if maxBytes <= 0 {
		maxBytes = maxPreservedEvidenceBytes
	}
	return &evidenceStore{
		records:  map[string]preservedEvidence{},
		order:    []string{},
		maxBytes: maxBytes,
	}
}

func (s *evidenceStore) addReadFileEvidence(path string, startLine, endLine int, payload string) preservedEvidence {
	s.nextID++
	id := fmt.Sprintf("ev-%d", s.nextID)
	record := preservedEvidence{
		ID:         id,
		Path:       path,
		StartLine:  startLine,
		EndLine:    endLine,
		Payload:    payload,
		Bytes:      len(payload),
		CapturedAt: time.Now(),
	}
	if len(record.Payload) > s.maxBytes {
		record.Payload = record.Payload[:s.maxBytes] + "\n... (Preserved evidence truncated) ..."
		record.Truncated = true
	}
	s.records[id] = record
	s.order = append(s.order, id)
	return record
}

func (s *evidenceStore) get(id string) (preservedEvidence, bool) {
	if s == nil {
		return preservedEvidence{}, false
	}
	record, ok := s.records[id]
	return record, ok
}

func (s *evidenceStore) compactIndex(limit int) string {
	if s == nil || len(s.order) == 0 {
		return ""
	}
	start := len(s.order) - limit
	if start < 0 {
		start = 0
	}
	lines := make([]string, 0, len(s.order)-start)
	for _, id := range s.order[start:] {
		record := s.records[id]
		line := fmt.Sprintf("- %s | %s | %s | %d bytes", record.ID, record.Path, formatLineRange(record.StartLine, record.EndLine), record.Bytes)
		if record.Truncated {
			line += " | truncated"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatLineRange(startLine, endLine int) string {
	switch {
	case startLine > 0 && endLine > 0:
		return fmt.Sprintf("lines %d-%d", startLine, endLine)
	case startLine > 0:
		return fmt.Sprintf("line %d+", startLine)
	default:
		return "lines unknown"
	}
}

func formatEvidencePayload(record preservedEvidence) string {
	return fmt.Sprintf(
		"EVIDENCE %s\nPath: %s\nRange: %s\nCaptured At: %s\nOriginal Bytes: %d\nTruncated: %t\n\n%s",
		record.ID,
		record.Path,
		formatLineRange(record.StartLine, record.EndLine),
		record.CapturedAt.Format(time.RFC3339),
		record.Bytes,
		record.Truncated,
		record.Payload,
	)
}

func normalizeReadFileRange(startLine, endLine int) (int, int) {
	if startLine <= 0 && endLine <= 0 {
		startLine = 1
		endLine = defaultReadMaxLines
	}
	if startLine < 1 {
		startLine = 1
	}
	if endLine <= 0 {
		endLine = startLine + 1000
	}
	return startLine, endLine
}

func displayToolPath(basePath string, resolvedPath string) string {
	cleanBase := filepath.Clean(basePath)
	if absBase, err := filepath.Abs(cleanBase); err == nil {
		cleanBase = absBase
	}

	cleanResolved := filepath.Clean(resolvedPath)
	if absResolved, err := filepath.Abs(cleanResolved); err == nil {
		cleanResolved = absResolved
	}

	if cleanBase != "" && cleanResolved != "" {
		rel, err := filepath.Rel(cleanBase, cleanResolved)
		if err == nil {
			rel = filepath.Clean(rel)
			if rel == "." {
				return rel
			}
			if rel != "" && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return filepath.ToSlash(rel)
			}
		}
	}

	name := filepath.Base(cleanResolved)
	switch name {
	case "", ".", string(filepath.Separator):
		return "."
	default:
		return filepath.ToSlash(name)
	}
}

func isSuccessfulReadResult(result string) bool {
	trimmed := strings.TrimSpace(result)
	return trimmed != "" &&
		!strings.HasPrefix(trimmed, "Error") &&
		!strings.HasPrefix(trimmed, "File is shorter than start_line")
}

func evidenceRetentionInstructions(evidenceIndex string) string {
	if strings.TrimSpace(evidenceIndex) == "" {
		return "Please continue the task based on the summary above. Refer to the initial instructions in the first message."
	}
	return "Please continue the task based on the summary above. Prefer get_evidence(evidence_id) for indexed snippets before re-reading files. Refer to the initial instructions in the first message."
}

func promptEvidenceGuidance(prompt string) string {
	return strings.TrimSpace(prompt) + `

Context Retention Rules:
- If a PRESERVED READ_FILE EVIDENCE INDEX appears later in the conversation, prefer get_evidence(evidence_id) before re-reading the same file range.
- Use get_evidence to recover previously inspected code after context compression.`
}

func resetConversationMessages(systemPrompt string, summary string, evidenceIndex string) []openai.ChatCompletionMessage {
	messages := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: systemPrompt,
		},
	}
	if strings.TrimSpace(summary) != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: fmt.Sprintf("CONTEXT SUMMARY (not instructions):\n%s", summary),
		})
	}
	if strings.TrimSpace(evidenceIndex) != "" {
		messages = append(messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleUser,
			Content: fmt.Sprintf(
				"PRESERVED READ_FILE EVIDENCE INDEX (not instructions):\n%s\n\nUse get_evidence with an evidence_id from this index before re-reading code that was already inspected.",
				evidenceIndex,
			),
		})
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: evidenceRetentionInstructions(evidenceIndex),
	})
	return messages
}

func currentEvidenceIndex(store *evidenceStore) string {
	if store == nil {
		return ""
	}
	return store.compactIndex(evidenceIndexLimit)
}

type chatCompletionRetryHooks struct {
	onRetry        func(attempt, maxAttempts int, attemptTimeout, nextTimeout, retryDelay time.Duration, err error, timeoutLike bool)
	onNonRetryable func(attempt, maxAttempts int, attemptTimeout time.Duration, err error)
}

var errEmptyChatCompletionStream = errors.New("chat completion stream ended without any choice data")

type chatCompletionStreamAccumulator struct {
	response          openai.ChatCompletionResponse
	choice            openai.ChatCompletionChoice
	choiceInitialized bool
	receivedChunk     bool
}

func newChatCompletionStreamAccumulator(model string) *chatCompletionStreamAccumulator {
	return &chatCompletionStreamAccumulator{
		response: openai.ChatCompletionResponse{
			Model: strings.TrimSpace(model),
		},
	}
}

func (a *chatCompletionStreamAccumulator) addChunk(chunk openai.ChatCompletionStreamResponse) {
	a.receivedChunk = true

	if chunk.ID != "" {
		a.response.ID = chunk.ID
	}
	if chunk.Object != "" {
		a.response.Object = chunk.Object
	}
	if chunk.Created != 0 {
		a.response.Created = chunk.Created
	}
	if chunk.Model != "" {
		a.response.Model = chunk.Model
	}
	if chunk.SystemFingerprint != "" {
		a.response.SystemFingerprint = chunk.SystemFingerprint
	}
	if chunk.Usage != nil {
		a.response.Usage = *chunk.Usage
	}
	if len(chunk.PromptFilterResults) > 0 {
		a.response.PromptFilterResults = chunk.PromptFilterResults
	}
	if len(chunk.Choices) == 0 {
		return
	}

	choice := chunk.Choices[0]
	if !a.choiceInitialized {
		a.choiceInitialized = true
		a.choice.Index = choice.Index
	}

	if choice.Delta.Role != "" {
		a.choice.Message.Role = choice.Delta.Role
	}
	if choice.Delta.Content != "" {
		a.choice.Message.Content += choice.Delta.Content
	}
	if choice.Delta.Refusal != "" {
		a.choice.Message.Refusal += choice.Delta.Refusal
	}
	if choice.Delta.ReasoningContent != "" {
		a.choice.Message.ReasoningContent += choice.Delta.ReasoningContent
	}
	if choice.Delta.FunctionCall != nil {
		if a.choice.Message.FunctionCall == nil {
			a.choice.Message.FunctionCall = &openai.FunctionCall{}
		}
		if choice.Delta.FunctionCall.Name != "" {
			a.choice.Message.FunctionCall.Name += choice.Delta.FunctionCall.Name
		}
		if choice.Delta.FunctionCall.Arguments != "" {
			a.choice.Message.FunctionCall.Arguments += choice.Delta.FunctionCall.Arguments
		}
	}
	if len(choice.Delta.ToolCalls) > 0 {
		a.choice.Message.ToolCalls = mergeChatCompletionToolCalls(a.choice.Message.ToolCalls, choice.Delta.ToolCalls)
	}
	if choice.FinishReason != "" {
		a.choice.FinishReason = choice.FinishReason
	}
}

func (a *chatCompletionStreamAccumulator) finalize() (openai.ChatCompletionResponse, error) {
	if !a.choiceInitialized {
		return openai.ChatCompletionResponse{}, errEmptyChatCompletionStream
	}
	if a.choice.Message.Role == "" {
		a.choice.Message.Role = openai.ChatMessageRoleAssistant
	}
	if a.response.Object == "" {
		a.response.Object = "chat.completion"
	}
	a.response.Choices = []openai.ChatCompletionChoice{a.choice}
	return a.response, nil
}

func mergeChatCompletionToolCalls(existing []openai.ToolCall, deltas []openai.ToolCall) []openai.ToolCall {
	for _, delta := range deltas {
		idx := len(existing)
		if delta.Index != nil && *delta.Index >= 0 {
			idx = *delta.Index
		}
		for len(existing) <= idx {
			existing = append(existing, openai.ToolCall{
				Type: openai.ToolTypeFunction,
			})
		}

		target := &existing[idx]
		if delta.ID != "" {
			target.ID = delta.ID
		}
		if delta.Type != "" {
			target.Type = delta.Type
		} else if target.Type == "" {
			target.Type = openai.ToolTypeFunction
		}
		if delta.Function.Name != "" {
			target.Function.Name += delta.Function.Name
		}
		if delta.Function.Arguments != "" {
			target.Function.Arguments += delta.Function.Arguments
		}
	}

	return existing
}

func createChatCompletion(ctx context.Context, client *openai.Client, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, bool, error) {
	resp, receivedChunk, err := createChatCompletionStream(ctx, client, req)
	if err == nil {
		return resp, receivedChunk, nil
	}
	if receivedChunk || !isStreamUnsupportedAIError(err) {
		return openai.ChatCompletionResponse{}, receivedChunk, err
	}

	resp, err = client.CreateChatCompletion(ctx, req)
	if err != nil {
		return openai.ChatCompletionResponse{}, false, err
	}
	return resp, false, nil
}

func createChatCompletionStream(ctx context.Context, client *openai.Client, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, bool, error) {
	stream, err := client.CreateChatCompletionStream(ctx, req)
	if err != nil {
		return openai.ChatCompletionResponse{}, false, err
	}
	defer stream.Close()

	accumulator := newChatCompletionStreamAccumulator(req.Model)
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return openai.ChatCompletionResponse{}, accumulator.receivedChunk, recvErr
		}
		accumulator.addChunk(chunk)
	}

	resp, err := accumulator.finalize()
	return resp, accumulator.receivedChunk, err
}

func isStreamUnsupportedAIError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errEmptyChatCompletionStream) || errors.Is(err, openai.ErrTooManyEmptyStreamMessages) {
		return true
	}

	errText := strings.ToLower(err.Error())
	streamHints := []string{
		"stream",
		"streaming",
		"event-stream",
		"text/event-stream",
		"sse",
	}
	unsupportedHints := []string{
		"unsupported",
		"not supported",
		"not support",
		"not implemented",
		"disabled",
		"invalid",
	}
	statusHints := []string{
		"status code: 400",
		"status code: 404",
		"status code: 405",
		"status code: 415",
		"status code: 422",
	}

	return containsAny(errText, streamHints...) && (containsAny(errText, unsupportedHints...) || containsAny(errText, statusHints...))
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func createChatCompletionWithRetry(
	ctx context.Context,
	client *openai.Client,
	req openai.ChatCompletionRequest,
	hooks chatCompletionRetryHooks,
) (openai.ChatCompletionResponse, error) {
	attempts := aiRequestMaxAttempts
	if attempts <= 0 {
		attempts = defaultAIRequestMaxAttempts
	}
	initialTimeout := aiRequestInitialTimeout
	if initialTimeout <= 0 {
		initialTimeout = defaultAIRequestInitialTimeout
	}
	timeoutStep := aiRequestTimeoutStep
	if timeoutStep <= 0 {
		timeoutStep = defaultAIRequestTimeoutStep
	}
	sleepFn := aiRequestSleep
	if sleepFn == nil {
		sleepFn = sleepWithContext
	}
	maxTimeout := maxAIRequestTimeout(initialTimeout, timeoutStep, attempts)
	timeoutMemoryKey := aiRequestTimeoutMemoryKey(config.AI.BaseURL, req.Model)
	currentTimeout := aiRequestTimeoutMemory.load(timeoutMemoryKey, initialTimeout, maxTimeout)

	var resp openai.ChatCompletionResponse
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptTimeout := currentTimeout
		ctxReq, cancel := context.WithTimeout(ctx, attemptTimeout)
		var receivedChunk bool
		resp, receivedChunk, err = createChatCompletion(ctxReq, client, req)
		cancel()
		if err == nil {
			return resp, nil
		}
		if receivedChunk {
			if hooks.onNonRetryable != nil {
				hooks.onNonRetryable(attempt, attempts, attemptTimeout, err)
			}
			return openai.ChatCompletionResponse{}, err
		}
		if !isRetryableAIError(err) {
			if hooks.onNonRetryable != nil {
				hooks.onNonRetryable(attempt, attempts, attemptTimeout, err)
			}
			return openai.ChatCompletionResponse{}, err
		}
		if attempt == attempts {
			return openai.ChatCompletionResponse{}, err
		}

		retryDelay := time.Duration(attempt) * time.Second
		nextTimeout := attemptTimeout
		timeoutLike := isAIRequestTimeout(err)
		if timeoutLike {
			nextTimeout = clampAIRequestTimeout(attemptTimeout+timeoutStep, initialTimeout, maxTimeout)
			currentTimeout = nextTimeout
			aiRequestTimeoutMemory.store(timeoutMemoryKey, nextTimeout, initialTimeout, maxTimeout)
		}
		if hooks.onRetry != nil {
			hooks.onRetry(attempt, attempts, attemptTimeout, nextTimeout, retryDelay, err, timeoutLike)
		}
		if !sleepFn(ctx, retryDelay) {
			return openai.ChatCompletionResponse{}, ctx.Err()
		}
	}

	return openai.ChatCompletionResponse{}, err
}

func aiRequestTimeoutMemoryKey(baseURL, model string) string {
	baseURL = strings.TrimSpace(baseURL)
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(config.AI.Model)
	}
	return baseURL + "\n" + model
}

func maxAIRequestTimeout(initial, step time.Duration, attempts int) time.Duration {
	if initial <= 0 {
		initial = defaultAIRequestInitialTimeout
	}
	if step <= 0 {
		step = defaultAIRequestTimeoutStep
	}
	if attempts <= 1 {
		return initial
	}
	return initial + (time.Duration(attempts-1) * step)
}

func clampAIRequestTimeout(value, fallback, max time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = defaultAIRequestInitialTimeout
	}
	if max < fallback {
		max = fallback
	}
	if value < fallback {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func isAIRequestTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "context deadline exceeded") ||
		strings.Contains(errText, "timeout") ||
		strings.Contains(errText, "status code: 524")
}

func resolveToolPath(basePath string, path string) (string, error) {
	if path == "" {
		return filepath.Clean(basePath), nil
	}
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(basePath, resolved)
	}
	absResolved, err := filepath.Abs(resolved)
	if err == nil {
		resolved = absResolved
	}
	absBase, err := filepath.Abs(basePath)
	if err == nil {
		basePath = absBase
	}
	cleanBase := filepath.Clean(basePath)
	cleanResolved := filepath.Clean(resolved)
	rel, err := filepath.Rel(cleanBase, cleanResolved)
	if err != nil {
		return "", fmt.Errorf("Error: unable to resolve path '%s': %w", path, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("Error: path '%s' is outside the project directory", path)
	}
	if pathHasSkippedComponent(rel) {
		return "", fmt.Errorf("Error: path '%s' is inside an ignored directory", path)
	}
	return cleanResolved, nil
}

type messageHistoryStats struct {
	droppedToolMessages   int
	droppedIncompleteRuns int
}

type compressionWindowSelection struct {
	selectedMessages  []openai.ChatCompletionMessage
	candidateStart    int
	adjustedStart     int
	candidateRole     string
	tailSanitizeStats messageHistoryStats
	usedFullHistory   bool
}

func (s messageHistoryStats) changed() bool {
	return s.droppedToolMessages > 0 || s.droppedIncompleteRuns > 0
}

func (s messageHistoryStats) summary(context string) string {
	parts := []string{}
	if s.droppedToolMessages > 0 {
		parts = append(parts, fmt.Sprintf("%d orphan/mismatched tool messages", s.droppedToolMessages))
	}
	if s.droppedIncompleteRuns > 0 {
		parts = append(parts, fmt.Sprintf("%d incomplete assistant tool-call rounds", s.droppedIncompleteRuns))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: removed %s.", context, strings.Join(parts, " and "))
}

func sanitizeMessageHistory(messages []openai.ChatCompletionMessage) ([]openai.ChatCompletionMessage, messageHistoryStats) {
	sanitized := make([]openai.ChatCompletionMessage, 0, len(messages))
	stats := messageHistoryStats{}

	pendingAssistantIdx := -1
	pendingToolCalls := map[string]struct{}{}

	dropPendingRound := func() {
		if pendingAssistantIdx == -1 {
			return
		}
		if pendingAssistantIdx < len(sanitized) {
			sanitized = sanitized[:pendingAssistantIdx]
		}
		pendingAssistantIdx = -1
		pendingToolCalls = map[string]struct{}{}
		stats.droppedIncompleteRuns++
	}

	clearPendingRound := func() {
		pendingAssistantIdx = -1
		pendingToolCalls = map[string]struct{}{}
	}

	for _, msg := range messages {
		if msg.Role == openai.ChatMessageRoleTool {
			if pendingAssistantIdx == -1 || msg.ToolCallID == "" {
				stats.droppedToolMessages++
				continue
			}
			if _, ok := pendingToolCalls[msg.ToolCallID]; !ok {
				stats.droppedToolMessages++
				continue
			}
			sanitized = append(sanitized, msg)
			delete(pendingToolCalls, msg.ToolCallID)
			continue
		}

		if pendingAssistantIdx != -1 {
			if len(pendingToolCalls) > 0 {
				dropPendingRound()
			} else {
				clearPendingRound()
			}
		}

		sanitized = append(sanitized, msg)

		if len(msg.ToolCalls) > 0 {
			pendingAssistantIdx = len(sanitized) - 1
			pendingToolCalls = make(map[string]struct{}, len(msg.ToolCalls))
			for _, toolCall := range msg.ToolCalls {
				if toolCall.ID == "" {
					continue
				}
				pendingToolCalls[toolCall.ID] = struct{}{}
			}
			if len(pendingToolCalls) == 0 {
				clearPendingRound()
			}
		}
	}

	if pendingAssistantIdx != -1 {
		if len(pendingToolCalls) > 0 {
			dropPendingRound()
		} else {
			clearPendingRound()
		}
	}

	return sanitized, stats
}

func findToolRoundStart(messages []openai.ChatCompletionMessage, idx int) (int, bool) {
	if idx < 0 || idx >= len(messages) || messages[idx].Role != openai.ChatMessageRoleTool {
		return idx, false
	}
	for i := idx; i >= 0; i-- {
		if len(messages[i].ToolCalls) > 0 {
			return i, true
		}
	}
	return idx, false
}

func selectCompressionWindow(messages []openai.ChatCompletionMessage, summaryWindow int) compressionWindowSelection {
	selection := compressionWindowSelection{
		selectedMessages: messages,
		candidateRole:    "none",
	}
	if len(messages) == 0 {
		return selection
	}

	if summaryWindow > 0 && len(messages) > summaryWindow {
		selection.candidateStart = len(messages) - summaryWindow
	}
	selection.adjustedStart = selection.candidateStart
	selection.candidateRole = messages[selection.candidateStart].Role

	if start, ok := findToolRoundStart(messages, selection.candidateStart); ok {
		selection.adjustedStart = start
	}

	tail := messages[selection.adjustedStart:]
	sanitizedTail, tailStats := sanitizeMessageHistory(tail)
	selection.tailSanitizeStats = tailStats
	if len(sanitizedTail) == 0 || tailStats.changed() {
		safeMessages, _ := sanitizeMessageHistory(messages)
		selection.selectedMessages = safeMessages
		selection.usedFullHistory = true
		return selection
	}

	selection.selectedMessages = sanitizedTail
	return selection
}

func isRetryableAIError(err error) bool {
	if err == nil {
		return false
	}
	if isAIRequestTimeout(err) {
		return true
	}

	errText := strings.ToLower(err.Error())

	nonRetryableMarkers := []string{
		"status code: 400",
		"400 bad request",
		"invalid_request_error",
		"no tool call found",
	}
	for _, marker := range nonRetryableMarkers {
		if strings.Contains(errText, marker) {
			return false
		}
	}

	retryableMarkers := []string{
		"status code: 408",
		"status code: 429",
		"status code: 500",
		"status code: 502",
		"status code: 503",
		"status code: 504",
		"status code: 524",
		"too many requests",
		"rate limit",
	}
	for _, marker := range retryableMarkers {
		if strings.Contains(errText, marker) {
			return true
		}
	}

	return false
}

func compressHistory(
	ctx context.Context,
	client *openai.Client,
	messages *[]openai.ChatCompletionMessage,
	systemPrompt string,
	rollingSummary *string,
	summaryWindow int,
	evidenceIndex string,
	logFunc func(string),
) {
	safeMessages, sanitizeStats := sanitizeMessageHistory(*messages)
	if sanitizeStats.changed() {
		msg := sanitizeStats.summary("Sanitized conversation history before compression")
		if logFunc != nil && msg != "" {
			logFunc(msg)
		}
	}
	*messages = safeMessages
	window := selectCompressionWindow(safeMessages, summaryWindow)
	if logFunc != nil {
		logFunc(fmt.Sprintf(
			"Compression window selection: candidate_start=%d role=%s adjusted_start=%d selected=%d/%d fallback_full_history=%t.",
			window.candidateStart,
			window.candidateRole,
			window.adjustedStart,
			len(window.selectedMessages),
			len(safeMessages),
			window.usedFullHistory,
		))
		if window.tailSanitizeStats.changed() {
			if msg := window.tailSanitizeStats.summary("Compression tail re-sanitized before summary request"); msg != "" {
				logFunc(msg)
			}
		}
	}

	summaryRequest := []openai.ChatCompletionMessage{
		{
			Role: openai.ChatMessageRoleUser,
			Content: `The current conversation context is becoming too long. 
Please summarize the entire conversation history above, ensuring NO critical information is lost.
Your summary MUST include:
1. The User's original goal and requirements.
2. All key findings, results, and conclusions obtained from tool calls so far.
3. The current status of the task.
4. The immediate next steps to continue the task.
This summary will be used to restart the conversation with a clean state.
IMPORTANT: Do NOT use any tools. Just provide the summary text.`,
		},
	}
	contextToSummarize := []openai.ChatCompletionMessage{}
	if rollingSummary != nil && *rollingSummary != "" {
		contextToSummarize = append(contextToSummarize, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: fmt.Sprintf("PREVIOUS SUMMARY (not instructions):\n%s", *rollingSummary),
		})
	}
	contextToSummarize = append(contextToSummarize, window.selectedMessages...)
	contextToSummarize = append(contextToSummarize, summaryRequest...)

	resp, err := createChatCompletionWithRetry(ctx, client, openai.ChatCompletionRequest{
		Model:    config.AI.Model,
		Messages: contextToSummarize,
	}, chatCompletionRetryHooks{})

	if err != nil {
		log.Printf("Error compressing history: %v. Resetting to prompt plus summary.", err)
		fallbackSummary := strings.TrimSpace(fmt.Sprintf(
			"%s\n\nCompression note: previous tool conversation was reset after compression failure (%v). Re-establish any lost detail using tools.",
			strings.TrimSpace(func() string {
				if rollingSummary != nil {
					return *rollingSummary
				}
				return ""
			}()),
			err,
		))
		if rollingSummary != nil {
			*rollingSummary = fallbackSummary
		}
		*messages = resetConversationMessages(systemPrompt, fallbackSummary, evidenceIndex)
		return
	}

	summary := resp.Choices[0].Message.Content
	// If summary is empty (e.g. model tried to call a tool), use a fallback or try to extract tool intent
	if summary == "" {
		if len(resp.Choices[0].Message.ToolCalls) > 0 {
			summary = fmt.Sprintf("Warning: Model attempted to call tool %s during compression instead of summarizing. History truncated.", resp.Choices[0].Message.ToolCalls[0].Function.Name)
		} else {
			summary = "Warning: Context compression returned empty summary."
		}
	}

	if rollingSummary != nil {
		*rollingSummary = summary
	}

	*messages = resetConversationMessages(systemPrompt, summary, evidenceIndex)
}

func appendChineseNarrativeRules(prompt string, stage string) string {
	return strings.TrimSpace(prompt) + fmt.Sprintf(`

LANGUAGE REQUIREMENTS:
- Keep the required final output format exactly unchanged.
- Do NOT translate JSON keys.
- Do NOT translate constrained categorical values or schema-required enums. Keep canonical English values such as type, severity, reviewed_severity, verification_status, origin, proof_type, authentication_state, file_operation, method, path, and any subtype values that the stage schema explicitly constrains.
- All explanatory or narrative fields MUST be written in Simplified Chinese.
- For the init stage, keep method, path, and source as discovered, but write description in Simplified Chinese.
- For review, repair, or explanatory fields such as description, execution_logic, verification_reason, trigger_steps, impact, preconditions, state_transition, bypass_vector, upgrade_recommendation, reproduction_steps, expected_execution, summary-style text, and similar free-text content, use Simplified Chinese only.
- If a field is a route, file path, identifier, payload, code snippet, HTTP request, version, or other technical artifact, preserve the exact technical content and only write surrounding explanations in Simplified Chinese.

CURRENT STAGE: %s`, stage)
}

func repairChineseNarrativeRules(stage string) string {
	return fmt.Sprintf(`
LANGUAGE REQUIREMENTS:
1. Keep JSON keys unchanged.
2. Keep constrained categorical values and schema-required enums in their canonical English form.
3. Convert all explanatory or narrative fields to Simplified Chinese.
4. For the init stage, keep method, path, and source unchanged, but write description in Simplified Chinese.
5. Preserve exact technical artifacts such as file paths, payloads, code snippets, versions, URLs, HTTP requests, and identifiers without translation.

CURRENT STAGE: %s`, stage)
}

func RepairJSON(content string, stage string) (string, error) {
	client := aiClientFactory()

	schemaInstruction := ""
	if stage == "injection" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "Injection",
      "subtype": "SQL Injection" (or other subtype),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc": "..."
   }
   CRITICAL: The "type" field MUST be "Injection".
`
	} else if stage == "rce" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "RCE",
      "subtype": "Command Injection" (or other subtype),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc": "..."
   }
   CRITICAL: The "type" field MUST be "RCE".
`
	} else if stage == "auth" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "Authentication",
      "subtype": "Weak Password Policy" (or "Session Fixation" or "Unauthorized Access" or "Brute Force" or "JWT Security"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "auth_mechanism": "JWT / Session Cookie / Bearer Token / API Key / Mixed",
      "affected_endpoints": [],
      "session_artifact": "",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "...",
      "trigger_steps": "...",
      "impact": "..."
   }
   CRITICAL: The "type" field MUST be "Authentication".
   CRITICAL: "affected_endpoints" MUST be a JSON array.
   CRITICAL: "session_artifact" may be an empty string when not applicable.
`
	} else if stage == "access" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "Authorization",
      "subtype": "Horizontal Privilege Escalation" (or "Vertical Privilege Escalation" or "Privilege Escalation" or "Missing Function-Level Access Control"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "authentication_state": "Authenticated" (or "Unauthenticated"),
      "attacker_profile": "",
      "target_profile": "",
      "required_privilege": "",
      "target_resource": "",
      "access_boundary": "",
      "affected_endpoints": [],
      "authorization_logic": "...",
      "bypass_vector": "...",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "...",
      "trigger_steps": "...",
      "impact": "..."
   }
   CRITICAL: The "type" field MUST be "Authorization".
   CRITICAL: "subtype" MUST be one of the four allowed access-control subtypes.
   CRITICAL: "authentication_state" MUST be either "Authenticated" or "Unauthenticated".
   CRITICAL: "affected_endpoints" MUST be a JSON array.
   CRITICAL: "attacker_profile", "target_profile", "required_privilege", "target_resource", and "access_boundary" may be empty strings when not applicable.
`
	} else if stage == "xss" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "XSS",
      "subtype": "Reflected XSS" (or "Stored XSS" or "DOM XSS"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "sink_type": "innerHTML / template render / v-html / dangerouslySetInnerHTML / attribute write / script context",
      "render_context": "HTML body / attribute / script string / DOM sink / Vue template / React render",
      "storage_point": "",
      "payload_hint": "<script>alert(1)</script>",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "...",
      "trigger_steps": "...",
      "expected_execution": "..."
   }
   CRITICAL: The "type" field MUST be "XSS".
   CRITICAL: "storage_point" may be an empty string when not applicable.
`
	} else if stage == "config" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
       "type": "Configuration",
      "subtype": "Sensitive Information Exposure" (or "CORS Misconfiguration" or "Debug Mode Enabled" or "Unsafe Deserialization" or "Vulnerable Third-Party Component"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "", "path": "", "parameter": "" },
      "proof_type": "HTTP" (or "Static"),
      "configuration_item": "",
      "component_name": "",
      "component_version": "",
      "reference_id": "",
      "affected_endpoints": [],
      "exposure_mechanism": "...",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "",
      "reproduction_steps": "...",
      "upgrade_recommendation": "",
      "impact": "..."
   }
   CRITICAL: The "type" field MUST be "Configuration".
   CRITICAL: "proof_type" MUST be either "HTTP" or "Static".
   CRITICAL: "affected_endpoints" MUST be a JSON array.
   CRITICAL: "trigger" may contain empty strings when the issue is static-only.
   CRITICAL: "poc_http" may be an empty string for static-only issues.
   CRITICAL: "component_name", "component_version", "reference_id", and "upgrade_recommendation" may be empty strings when not applicable.
`
	} else if stage == "fileop" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "FileOperation",
      "subtype": "Arbitrary File Upload" (or "Arbitrary File Download/Read" or "Path Traversal" or "File Inclusion"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "GET/POST", "path": "/api/...", "parameter": "param" },
      "file_operation": "upload" (or "download" or "read" or "include"),
      "input_vector": "",
      "target_path": "",
      "validation_logic": "",
      "payload_hint": "",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "...",
      "trigger_steps": "...",
      "impact": "..."
   }
   CRITICAL: The "type" field MUST be "FileOperation".
   CRITICAL: "subtype" MUST be one of the four allowed file-operation subtypes.
   CRITICAL: "file_operation" MUST be one of: "upload", "download", "read", "include".
   CRITICAL: "input_vector", "target_path", "validation_logic", and "payload_hint" may be empty strings when the exact detail is not applicable, but they must still be present.
   CRITICAL: "poc_http", "trigger_steps", and "impact" must always be present and non-empty.
`
	} else if stage == "logic" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure (fix or infer missing fields):
   {
      "type": "BusinessLogic",
      "subtype": "Workflow Bypass" (or "Race Condition" or "Amount/Quantity Tampering" or "Business Rule Bypass"),
      "severity": "High",
      "location": { "file": "filepath", "line": 0, "function": "funcName" },
      "trigger": { "method": "", "path": "", "parameter": "" },
      "proof_type": "HTTP" (or "Static"),
      "workflow_name": "",
      "business_action": "",
      "affected_endpoints": [],
      "preconditions": "",
      "state_transition": "",
      "manipulated_fields": [],
      "race_window": "",
      "bypass_vector": "",
      "description": "...",
      "execution_logic": "...",
      "vulnerable_code": "...",
      "poc_http": "",
      "trigger_steps": "...",
      "impact": "..."
   }
   CRITICAL: The "type" field MUST be "BusinessLogic".
   CRITICAL: "subtype" MUST be one of the four allowed business-logic subtypes.
   CRITICAL: "proof_type" MUST be either "HTTP" or "Static".
   CRITICAL: "affected_endpoints" and "manipulated_fields" MUST be JSON arrays.
   CRITICAL: "trigger" may contain empty strings when the issue is static-only.
   CRITICAL: "workflow_name", "business_action", "preconditions", "state_transition", "race_window", and "bypass_vector" may be empty strings when not applicable, but they must still be present.
   CRITICAL: "poc_http" may be an empty string for static-only issues.
   CRITICAL: "trigger_steps" and "impact" must always be present and non-empty.
`
	} else if stage == "init" {
		schemaInstruction = `
5. Ensure EACH item in the JSON array has the following structure:
   {
      "method": "GET/POST/...",
      "path": "/api/...",
      "source": "path/to/file",
      "description": "..."
   }
`
	}

	prompt := fmt.Sprintf(`You are a JSON repair expert. 
The following text contains a JSON object or array that may be malformed, incomplete, or surrounded by other text.
Your task is to extract and repair it into a VALID JSON string.

RULES:
1. Return ONLY the JSON. 
2. Do not add any markdown formatting (no code blocks).
3. Do not add any explanation.
4. If the JSON is incomplete, try to close it logically.
%s
%s

You MUST use the following raw data to generate the JSON:
<<<<RAW_DATA
%s
RAW_DATA>>>>
`, schemaInstruction, repairChineseNarrativeRules(stage), content)

	ctx := context.Background()
	resp, err := createChatCompletionWithRetry(ctx, client, openai.ChatCompletionRequest{
		Model: config.AI.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	}, chatCompletionRetryHooks{})

	if err != nil {
		return "", err
	}

	result := resp.Choices[0].Message.Content
	return extractJSON(result), nil
}

func runAIScan(task *model.Task, stage string, kind StageRunKind, resume bool) {
	// Panic Recovery
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("CRITICAL PANIC in runAIScan: %v\n", r)
			task.Status = "failed"
			task.Result = fmt.Sprintf("Internal Server Error (Panic): %v", r)
			saveTaskRecord(task)
		}
	}()

	// Reconstruct BasePath if missing (e.g. resumed task)
	task.BasePath = task.GetBasePath()

	// Identify Stage and Setup Logging
	var currentStage *model.TaskStage
	if stage != "init" {
		var s model.TaskStage
		res := database.DB.Where("task_id = ? AND name = ?", task.ID, stage).First(&s)
		if res.Error != nil {
			s = model.TaskStage{
				TaskID:     task.ID,
				Name:       stage,
				Status:     "running",
				CreatedAt:  time.Now(),
				Logs:       []string{},
				OutputJSON: json.RawMessage("{}"), // Initialize with empty JSON object
				Meta: model.TaskStageMeta{
					LastRunKind: string(kind),
				},
			}
			database.DB.Create(&s)
		} else {
			s.Status = "running"
			s.Meta.LastRunKind = string(kind)
			if !resume {
				s.Logs = []string{} // Reset logs for a fresh run only.
			}
			saveTaskStageRecord(&s)
		}
		currentStage = &s
	}

	// Logging Helper - batched writes with time-based flush for real-time frontend updates
	logBatchSize := 2
	pendingLogs := 0
	lastFlushTime := time.Now()
	flushInterval := 3 * time.Second
	doFlush := func() {
		// Must use Save() instead of Update() because the Logs field uses
		// gorm:"serializer:json" — Update() bypasses the serializer and
		// silently fails to write []string to a JSON column.
		if currentStage != nil {
			saveTaskStageRecord(currentStage)
		} else {
			saveTaskRecord(task)
		}
		pendingLogs = 0
		lastFlushTime = time.Now()
	}
	logFunc := func(msg string) {
		timestamp := time.Now().Format("15:04:05")
		entry := fmt.Sprintf("[%s] %s", timestamp, msg)

		// Terminal output: truncate long AI messages to keep console readable
		consoleMsg := entry
		if strings.HasPrefix(msg, "AI: ") && len(msg) > 204 {
			consoleMsg = fmt.Sprintf("[%s] AI: %s ... (truncated, %d chars total)", timestamp, msg[4:204], len(msg)-4)
		}
		fmt.Println(consoleMsg)

		// Database: store full content
		if currentStage != nil {
			currentStage.Logs = append(currentStage.Logs, entry)
		} else {
			task.Logs = append(task.Logs, entry)
		}
		pendingLogs++
		if pendingLogs >= logBatchSize || time.Since(lastFlushTime) >= flushInterval {
			doFlush()
		}
	}
	flushLogs := func() {
		if pendingLogs > 0 {
			doFlush()
		}
	}

	task.Status = "running"
	updateTaskStatus(task, "running")

	if resume {
		logFunc("Task resumed. Restoring AI runtime...")
	} else {
		logFunc(fmt.Sprintf("Task started (%s). Initializing AI...", kind))
	}

	defer func() {
		// Flush any pending logs and final save
		flushLogs()
		saveTaskRecord(task)
		if currentStage != nil {
			saveTaskStageRecord(currentStage)
		}
	}()

	client := aiClientFactory()
	toolCache := map[string]string{}
	toolCacheOrder := []string{}
	maxToolCacheEntries := 200

	// --- PROMPT SELECTION ---
	var prompt string
	if stage == "init" {
		prompt = fmt.Sprintf(`You are a Code Scanner specialized in extracting API Routes and URLs.
Your SINGLE goal is to identify and list ALL API routes, endpoints, and URLs defined in the codebase.

Base Path: %s

**MISSION CRITICAL: EXHAUSTIVE SEARCH**
- You MUST exhaustively check ALL files that match your search patterns. 
- Do NOT stop after finding just a few examples. 
- Your goal is to map the ENTIRE API surface. 
- If 'grep' returns 50 files, you MUST process all 50 files.

Strategies for Success:
- **CORE STRATEGY: DETECT & ADAPT**
  1. **Identify the Framework**: Start by listing files in the root or checking dependency files (go.mod, pom.xml, package.json, requirements.txt) to determine the web framework used (e.g., Gin, Echo, Fiber, Spring Boot, Flask, Express).
  2. **Construct Targeted Regex**: Do NOT rely solely on generic examples. Use grep_files with patterns specific to the detected framework.

- **Framework-Specific Regex Cheat Sheet (Use as reference, but adapt as needed):**
  - **Go**:
    - **Gin/Echo/Fiber**: grep_files(pattern="\\.(GET|POST|PUT|DELETE|PATCH|Any|Group|Static)\\(")
    - **Chi**: grep_files(pattern="\\.(Get|Post|Put|Delete|Patch|Route|Mount)\\(")
    - **Gorilla Mux**: grep_files(pattern="\\.(HandleFunc|Handle|PathPrefix)\\(")
    - **Standard Lib**: grep_files(pattern="http\\.(HandleFunc|Handle)\\(")
    - **Beego**: grep_files(pattern="beego\\.Router\\(")
  - **Java (Spring)**: grep_files(pattern="(@(GetMapping|PostMapping|PutMapping|DeleteMapping|RequestMapping|PatchMapping)|@Path)")
  - **Python**:
    - **Flask**: grep_files(pattern="@app\\.route\\(")
    - **Django**: grep_files(pattern="(path|url)\\(")
    - **FastAPI**: grep_files(pattern="@app\\.(get|post|put|delete)\\(")
  - **Node.js**:
    - **Express/Koa**: grep_files(pattern="\\.(get|post|put|delete|patch|use|route)\\(")
    - **NestJS**: grep_files(pattern="@(Get|Post|Put|Delete|Patch|Controller)\\(")

- Be flexible! If standard patterns don't work, try searching for "http", "api", or specific handler function names.
- Verify findings by reading *snippets* of the file around the match, rather than the whole file.

Constraints & Limits:
- You must ONLY use the provided tools to extract information. Never guess.
- **CRITICAL**: Do NOT read entire files. You MUST use start_line and end_line in read_file to read only the relevant function or block of code.
- Only if you absolutely cannot understand the context from snippets, are you allowed to read a larger chunk, but avoid reading files > 300 lines.
- Focus ONLY on finding URLs/Routes. Do not perform security analysis or other tasks yet.

Output Format:
- **Iterative Workflow**:
  1. **Think**: Briefly explain your analysis of the current state.
  2. **Act**: Call the appropriate tool.
  3. **Loop**: Review the tool output and repeat. Do not plan too far ahead; adapt to what you find.
- **Final Output**: When finished, output the result as a strict JSON array wrapped in a Markdown code block.
  
  Example Final Output (FORMAT ONLY - Real output must contain ALL findings):
  `+"```json"+`
  [
    {"method": "GET", "path": "/api/users", "source": "routes/user.go", "description": "Get all users"},
    {"method": "POST", "path": "/api/login", "source": "routes/auth.go", "description": "User login"},
    ... (and all other routes found)
  ]
  `+"```"+`

  - The JSON array must contain objects with: "method", "path", "source" (relative path), "description".
  - **CRITICAL**: Do NOT include any text outside the JSON block in your final response.
`, task.BasePath)
	} else if stage == "rce" {
		// Load previous stage result (Route Map)
		// We use task.OutputJSON which contains the structured route list from the 'init' stage.
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 2: RCE (Remote Code Execution) Deep Audit.
Your goal is to find Remote Code Execution vulnerabilities, specifically focusing on command injection, code injection, and unsafe deserialization.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE VULNERABILITY SEARCH**
- You MUST exhaustively check ALL potential RCE sinks in the codebase.
- Do NOT stop after finding just one vulnerability.
- If you find a potential dangerous function (e.g., exec.Command), you MUST verify if it is reachable from ANY user input.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** security vulnerabilities (RCE) in the code.

**AUDIT TARGETS (Phase 2)**:
1. **Command Injection**: exec, system, popen, Runtime.exec, ProcessBuilder
2. **Code Injection**: eval, assert, Function(), scripting engine calls
3. **Unsafe Deserialization**: ObjectInputStream, gob.Decoder, pickle.load, yaml.load
4. **Template Injection**: Unsafe template rendering (SSTI)

**EXECUTION RULES**:
1. **Method-Level Tracking**: If a vulnerable function is found, you MUST trace its arguments back to the entry point (API Route).
2. **Evidence Required**: You MUST provide the chain of evidence (Source File -> Line -> Function -> Vulnerability).
3. **POC Construction**: For every confirmed vulnerability, you MUST construct a raw HTTP request POC.
4. **No Guessing**: Only report vulnerabilities where you can trace the data flow from user input to the sink.
5. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files/line ranges already read. Do NOT re-read the same file/range.
6. **Progress Memory**: Maintain a running list of checked sinks and files within your own reasoning, and avoid repeating the same tool calls.

**Output Format**:
- **Iterative Workflow**:
  1. **Think**: Analyze the current context (routes/sinks).
  2. **Act**: Call tools to verify specific targets.
  3. **Loop**: Review findings and repeat. Trace data flow step-by-step.
- **Final Output**: When finished, output the result as a strict JSON array wrapped in a Markdown code block.
  
  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "RCE",
      "subtype": "Command Injection",
      "severity": "High",
      "location": { "file": "path/to/file.go", "line": 123, "function": "HandleRequest" },
      "trigger": { "method": "POST", "path": "/api/v1/cmd", "parameter": "command" },
      "description": "Brief explanation of the vulnerability.",
      "execution_logic": "Step-by-step description of how the code executes the vulnerability (e.g., 'The user input 'command' is passed to 'exec.Command' without sanitization...').",
      "vulnerable_code": "The actual source code snippet demonstrating the vulnerability (e.g., 'cmd := exec.Command(\"sh\", \"-c\", input)')",
      "poc": "POST /api/v1/cmd HTTP/1.1\n..."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`
  
  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "injection" {
		// --- 阶段三：注入类漏洞深度审计 ---

		// 1. 获取上下文（复用路由表）
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		// 2. 构造 Prompt
		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 3: Injection Vulnerability Deep Audit.
Your goal is to find Injection vulnerabilities, where untrusted data is sent to an interpreter as part of a command or query.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE VULNERABILITY SEARCH**
- You MUST exhaustively check ALL potential Injection sinks in the codebase.
- Do NOT stop after finding just one vulnerability.
- If you find a potential dangerous function (e.g., Exec, Query, fmt.Sprintf in SQL context), you MUST verify if it is reachable from ANY user input.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** security vulnerabilities (Injection) in the code.

**AUDIT TARGETS (Phase 3)**:
1. **SQL Injection**: 
   - Unsafe string concatenation in SQL queries (e.g., fmt.Sprintf("SELECT * FROM users WHERE name = '%%s'", input)).
   - Improper use of ORM raw queries.
2. **NoSQL Injection**: 
   - Injection in MongoDB, Redis, or other NoSQL databases.
3. **Command Injection**: 
   - OS Command Injection (re-verify if missed in RCE phase).
4. **LDAP Injection**: 
   - Unsafe LDAP filter construction.
5. **Template Injection (SSTI)**: 
   - User input reflected in server-side templates.

**EXECUTION RULES**:
1. **Trace Data Flow**: You MUST trace user input (from API Routes) to the dangerous sink (Database, Shell, LDAP, Template).
2. **Evidence Required**: Provide file path, line number, and function name.
3. **POC Construction**: Construct a raw HTTP request POC that demonstrates the injection.
4. **Severity Assessment**: Rate severity (High/Critical) based on impact.
5. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files/line ranges already read. Do NOT re-read the same file/range.
6. **Progress Memory**: Maintain a running list of checked sinks and files within your own reasoning, and avoid repeating the same tool calls.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "Injection",
      "subtype": "SQL Injection",
      "severity": "Critical",
      "location": { "file": "internal/db/user.go", "line": 45, "function": "GetUser" },
      "trigger": { "method": "GET", "path": "/api/user", "parameter": "id" },
      "description": "User input is directly concatenated into SQL query string.",
      "execution_logic": "Step-by-step description of how the code executes the vulnerability (e.g., 'The user input 'id' is concatenated directly into the SQL string...').",
      "vulnerable_code": "The actual source code snippet demonstrating the vulnerability (e.g., 'query := fmt.Sprintf(\"SELECT * FROM users WHERE id = %%s\", id)')",
      "poc": "GET /api/user?id=' OR '1'='1 HTTP/1.1\\n..."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`
  
  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "auth" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 5: Authentication and Session Security Audit.
Your goal is to find confirmed authentication and session security vulnerabilities.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE VULNERABILITY SEARCH**
- You MUST exhaustively inspect the authentication flow, session lifecycle, login handling, token issuance, token verification, and route protection mechanisms.
- Do NOT stop after finding one issue.
- If you find a potentially risky auth pattern, you MUST verify whether it is actually reachable from a real route or request path.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** security vulnerabilities related to authentication and session security.
4. Stay within Phase 5 scope. Do NOT report IDOR, horizontal privilege escalation, vertical privilege escalation, or generic authorization matrix issues unless the endpoint is completely missing authentication or the authentication can be bypassed.
5. Do NOT report generic CORS, CSRF, rate limiting, or configuration issues unless they directly prove one of the Phase 5 vulnerability types below.

**AUDIT TARGETS (Phase 5)**:
1. **Weak Password Policy**:
   - Missing password complexity or minimum length checks in registration, password reset, or password change flows.
   - Hardcoded default credentials or insecure fallback passwords.
2. **Session Fixation**:
   - Session identifiers not rotated after login, privilege change, or re-authentication.
   - Reuse of attacker-controlled session or token artifacts across auth state transitions.
3. **Unauthorized Access**:
   - Endpoints or actions that should require authentication but can be reached without valid auth.
   - Routes where authentication checks are missing, incorrectly skipped, or trivially bypassed.
4. **Brute Force**:
   - Login or verification flows without meaningful throttling, lockout, delay, challenge, or attempt tracking.
5. **JWT Security**:
   - JWT verification flaws such as unsigned tokens, algorithm confusion, missing expiration checks, missing signature validation, weak secret usage, or unsafe claim trust.

**EXECUTION RULES**:
1. **Trace the Auth Flow**: You MUST trace login, token/session issuance, middleware verification, and protected endpoint access.
2. **Evidence Required**: Provide file path, line number, and function name for the core vulnerable logic.
3. **Scope Accuracy**: Only report issues that clearly belong to Phase 5.
4. **Unauthorized Access Precision**: Report `+"`"+`Unauthorized Access`+"`"+` only when you can show that a route lacks effective authentication or auth can be bypassed.
5. **JWT Precision**: For JWT findings, explain exactly which validation step is missing or unsafe.
6. **POC Construction**: For every confirmed issue, provide BOTH:
   - `+"`"+`poc_http`+"`"+`: a raw HTTP request demonstrating the vulnerable entry point.
   - `+"`"+`trigger_steps`+"`"+`: concise steps needed to exploit or observe the issue.
7. **Affected Endpoints**: List all directly affected routes you can confirm from code.
8. **No Guessing**: Only report vulnerabilities where the source-to-check-to-bypass chain is evidenced by code.
9. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
10. **Progress Memory**: Maintain a running list of checked auth components and files within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- `+"`"+`type`+"`"+` must be `+"`"+`Authentication`+"`"+`.
- `+"`"+`subtype`+"`"+` must be exactly one of: `+"`"+`Weak Password Policy`+"`"+`, `+"`"+`Session Fixation`+"`"+`, `+"`"+`Unauthorized Access`+"`"+`, `+"`"+`Brute Force`+"`"+`, `+"`"+`JWT Security`+"`"+`.
- `+"`"+`auth_mechanism`+"`"+` should describe the mechanism in use, such as JWT, session cookie, bearer token, API key, or mixed.
- `+"`"+`affected_endpoints`+"`"+` must be a JSON array of strings like `+"`"+`GET /api/users`+"`"+`.
- `+"`"+`session_artifact`+"`"+` should contain the relevant token, cookie, session ID, or auth header name when applicable; otherwise use an empty string.
- `+"`"+`poc_http`+"`"+` must contain a raw HTTP request.
- `+"`"+`trigger_steps`+"`"+` must describe how to observe or exploit the issue.
- `+"`"+`impact`+"`"+` must summarize the security consequence.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "Authentication",
      "subtype": "JWT Security",
      "severity": "Critical",
      "location": { "file": "internal/middleware/auth.go", "line": 42, "function": "ValidateJWT" },
      "trigger": { "method": "GET", "path": "/api/admin", "parameter": "Authorization" },
      "auth_mechanism": "JWT Bearer Token",
      "affected_endpoints": ["GET /api/admin", "POST /api/admin/users"],
      "session_artifact": "Authorization: Bearer <jwt>",
      "description": "JWTs are accepted without verifying the signature algorithm safely.",
      "execution_logic": "The middleware parses the token and trusts claims before enforcing a strict signing method check, allowing a forged token to reach protected handlers.",
      "vulnerable_code": "token, _ := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) { return []byte(secret), nil })",
      "poc_http": "GET /api/admin HTTP/1.1\nHost: target\nAuthorization: Bearer <forged-token>\n...",
      "trigger_steps": "1. Forge a token with attacker-controlled claims. 2. Send it to the protected endpoint. 3. Observe that access is granted.",
      "impact": "Attackers can bypass authentication and access protected resources."
    }
  ]
  `+"```"+`

	If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

	**CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "access" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 6: Authorization and Access Control Audit.
Your goal is to find confirmed authorization and access control vulnerabilities, including IDOR-style flaws, broken role checks, privilege escalation paths, and missing function-level access control.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE ACCESS CONTROL REVIEW**
- You MUST inspect all routes, handlers, controller actions, service-layer authorization checks, ownership checks, tenant boundaries, and role-based restrictions.
- You MUST compare what an unauthenticated user, a normal user, a different resource owner, a different tenant, and a higher-privileged user can do.
- Do NOT stop after finding one issue.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** security vulnerabilities related to authorization and access control.
4. You MAY report unauthenticated access in Phase 6 when the code shows that the route or action should be protected but lacks effective access control.
5. Do NOT report password policy, brute force, JWT validation, session lifecycle, or other pure Phase 5 issues unless they directly create a confirmed Phase 6 access control impact.

**AUDIT TARGETS (Phase 6)**:
1. **Horizontal Privilege Escalation**:
   - One user can access or modify another user’s resources by changing identifiers, owner IDs, tenant IDs, or foreign keys.
2. **Vertical Privilege Escalation**:
   - Lower-privileged users can perform admin or privileged functions due to missing or weak role checks.
3. **Privilege Escalation**:
   - Attackers can grant themselves extra roles, change permission flags, switch tenants, or update authorization-sensitive fields.
4. **Missing Function-Level Access Control**:
   - Sensitive routes or actions are reachable without the required role, ownership check, tenant check, or policy enforcement.

**EXECUTION RULES**:
1. **Map Protected Endpoints**: Analyze all routes that should require authentication, ownership, tenant isolation, or role-based access.
2. **Compare Access Differences**: Trace how different user states or roles are checked, skipped, or bypassed.
3. **Trace Data Operations**: Focus on read, update, delete, export, admin, billing, configuration, and permission-management operations.
4. **Evidence Required**: Provide file path, line number, function name, and the exact missing or broken authorization logic.
5. **No Guessing**: Only report vulnerabilities where the route-to-check-to-bypass chain is evidenced by code.
6. **POC Construction**: For every confirmed issue, provide BOTH:
   - "poc_http": a raw HTTP request demonstrating the vulnerable entry point.
   - "trigger_steps": concise steps needed to exploit or observe the issue.
7. **Minimum Privilege**: State the minimum attacker privilege needed to trigger the issue.
8. **Authentication State**: Mark whether the vulnerable request is "Authenticated" or "Unauthenticated".
9. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
10. **Progress Memory**: Maintain a running list of checked authz components and files within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- "type" must be "Authorization".
- "subtype" must be exactly one of: "Horizontal Privilege Escalation", "Vertical Privilege Escalation", "Privilege Escalation", "Missing Function-Level Access Control".
- "authentication_state" must be exactly "Authenticated" or "Unauthenticated".
- "attacker_profile" should describe the attacker identity or role, such as guest, normal user, different tenant user, or low-privilege staff.
- "target_profile" should describe the victim or required role, such as another user, admin, resource owner, or different tenant.
- "required_privilege" must describe the minimum legitimate privilege needed for the action.
- "target_resource" should identify the protected object, action, or record being accessed.
- "access_boundary" should describe the broken boundary, such as resource ownership, role, tenant, organization, or function boundary.
- "affected_endpoints" must be a JSON array of strings like "GET /api/users/:id".
- "authorization_logic" must explain the intended check and why it is missing or ineffective.
- "bypass_vector" must summarize how the attacker crosses the boundary.
- "poc_http" must contain a raw HTTP request.
- "trigger_steps" must describe how to reproduce the unauthorized access.
- "impact" must summarize the consequence of the access control failure.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "Authorization",
      "subtype": "Horizontal Privilege Escalation",
      "severity": "High",
      "location": { "file": "internal/handler/order.go", "line": 77, "function": "GetOrderDetail" },
      "trigger": { "method": "GET", "path": "/api/orders/{id}", "parameter": "id" },
      "authentication_state": "Authenticated",
      "attacker_profile": "Normal user from account A",
      "target_profile": "Different user from account B",
      "required_privilege": "Resource owner of the requested order",
      "target_resource": "Order record identified by id",
      "access_boundary": "Resource ownership boundary",
      "affected_endpoints": ["GET /api/orders/{id}"],
      "authorization_logic": "The handler loads the order by ID but never verifies that order.UserID matches the authenticated user.",
      "bypass_vector": "Change the order ID to another user's order ID while keeping a valid low-privilege session.",
      "description": "Any authenticated user can read another user's order by modifying the order identifier.",
      "execution_logic": "The route accepts an order ID, fetches the record directly, and returns it without enforcing ownership checks.",
      "vulnerable_code": "orderID := c.Param(\"id\")\ndb.First(&order, orderID)\nc.JSON(200, order)",
      "poc_http": "GET /api/orders/2002 HTTP/1.1\nHost: target\nAuthorization: Bearer <user-a-token>\n...",
      "trigger_steps": "1. Authenticate as user A. 2. Request an order ID owned by user B. 3. Observe that the order details are returned.",
      "impact": "Attackers can access other users' protected data and potentially chain the flaw into broader account compromise."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "xss" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 4: XSS (Cross-Site Scripting) Comprehensive Audit.
Your goal is to find confirmed Reflected XSS, Stored XSS, and DOM XSS vulnerabilities.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE VULNERABILITY SEARCH**
- You MUST exhaustively inspect ALL relevant server-side outputs and frontend rendering sinks.
- Do NOT stop after finding one XSS issue.
- You MUST distinguish Reflected XSS, Stored XSS, and DOM XSS precisely.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** security vulnerabilities (XSS) in the code.
4. Do NOT report SSTI, template injection, or plain HTML injection unless you can confirm JavaScript execution or browser-executable XSS.

**AUDIT TARGETS (Phase 4)**:
1. **Reflected XSS**:
   - User-controlled data reflected into HTML/template responses without proper contextual escaping.
   - Unsafe render contexts such as HTML body, HTML attribute, inline event handler, script string, and URL/script-adjacent contexts.
2. **Stored XSS**:
   - User input stored in database, files, cache, or persisted content and later rendered into a browser-executable context.
   - You MUST identify both the write path and the read/render path.
3. **DOM XSS**:
   - Frontend code that reads from `+"`"+`location.search`+"`"+`, `+"`"+`location.hash`+"`"+`, `+"`"+`document.URL`+"`"+`, `+"`"+`window.name`+"`"+`, `+"`"+`postMessage`+"`"+`, `+"`"+`localStorage`+"`"+`, `+"`"+`sessionStorage`+"`"+`, or API responses and writes into dangerous sinks.
   - Dangerous sinks include `+"`"+`innerHTML`+"`"+`, `+"`"+`outerHTML`+"`"+`, `+"`"+`insertAdjacentHTML`+"`"+`, `+"`"+`document.write`+"`"+`, `+"`"+`srcdoc`+"`"+`, `+"`"+`v-html`+"`"+`, `+"`"+`dangerouslySetInnerHTML`+"`"+`, jQuery `+"`"+`.html()`+"`"+`, or equivalent template bypasses.

**EXECUTION RULES**:
1. **Trace Source to Sink**: You MUST trace user input from its source to the exact browser-executable sink.
2. **Evidence Required**: Provide file path, line number, and function/component name.
3. **Subtype Accuracy**: Each finding MUST be labeled as exactly one of: Reflected XSS, Stored XSS, DOM XSS.
4. **Stored XSS Proof**: For stored XSS, you MUST identify the storage point and the later render path.
5. **Context Accuracy**: You MUST state the render context (for example: HTML body, attribute, script string, innerHTML, v-html).
6. **POC Construction**: For every confirmed vulnerability, provide BOTH:
   - `+"`"+`poc_http`+"`"+`: a raw HTTP request for the server entry point or initial page/API request.
   - `+"`"+`trigger_steps`+"`"+`: precise browser or user steps needed to trigger execution.
7. **Expected Execution**: Explain where the payload executes (for example: admin comment page, search results page, profile detail page, browser hash route).
8. **No Guessing**: Only report vulnerabilities where the source-to-sink chain is evidenced by code.
9. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
10. **Progress Memory**: Maintain a running list of checked sinks and files within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- `+"`"+`type`+"`"+` must be `+"`"+`XSS`+"`"+`.
- `+"`"+`sink_type`+"`"+` should name the dangerous sink (for example `+"`"+`innerHTML`+"`"+`, `+"`"+`v-html`+"`"+`, `+"`"+`template render`+"`"+`).
- `+"`"+`render_context`+"`"+` should describe the browser execution context.
- `+"`"+`storage_point`+"`"+` must be filled for Stored XSS; otherwise use an empty string.
- `+"`"+`payload_hint`+"`"+` should contain a representative XSS payload string.
- `+"`"+`poc_http`+"`"+` must contain a raw HTTP request.
- `+"`"+`trigger_steps`+"`"+` must describe how to make the payload execute in browser.
- `+"`"+`expected_execution`+"`"+` must describe the page or component where execution occurs.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "XSS",
      "subtype": "Stored XSS",
      "severity": "High",
      "location": { "file": "frontend/src/views/comments.vue", "line": 88, "function": "renderComment" },
      "trigger": { "method": "POST", "path": "/api/comments", "parameter": "content" },
      "sink_type": "v-html",
      "render_context": "HTML body",
      "storage_point": "comments.content",
      "payload_hint": "<img src=x onerror=alert(1)>",
      "description": "User-controlled comment content is stored and later rendered with v-html.",
      "execution_logic": "The comment content is submitted through /api/comments, stored in comments.content, returned by /api/comments/list, and rendered with v-html.",
      "vulnerable_code": "<div v-html=\"comment.content\"></div>",
      "poc_http": "POST /api/comments HTTP/1.1\nHost: target\n...",
      "trigger_steps": "1. Submit payload. 2. Open the comments page. 3. Browser renders the stored content and executes the payload.",
      "expected_execution": "Payload executes in the comments page when the stored record is rendered."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "config" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 7: Configuration and Component Security Audit.
Your goal is to find confirmed vulnerabilities caused by insecure configuration, unsafe defaults, sensitive information exposure, unsafe framework/component settings, and vulnerable third-party components.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE CONFIGURATION REVIEW**
- You MUST exhaustively inspect configuration files, environment files, dependency manifests, lock files, deployment descriptors, container definitions, framework settings, middleware configuration, and component initialization code.
- Do NOT stop after finding one issue.
- You MUST verify that each reported issue is real and evidenced by repository code or configuration. Do NOT guess.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY when mapping a configuration or component issue to reachable HTTP endpoints.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** Phase 7 vulnerabilities in the code and configuration.
4. Do NOT report pure RCE, Injection, XSS, authentication, or authorization issues unless the vulnerable configuration, framework setting, serializer setting, or component choice is the core root cause or critical enabler.

**AUDIT TARGETS (Phase 7)**:
1. **Sensitive Information Exposure**:
   - Hardcoded secrets, API keys, tokens, signing secrets, database passwords, encryption keys, default credentials, or sensitive internal endpoints exposed in config files or runtime settings.
   - Verbose error/debug configuration, stack traces, actuator/debug endpoints, or admin tooling enabled in production-facing configuration.
   - Weak or empty security configuration such as blank passwords, test secrets, weak encryption modes, or unsafe fallback credentials.
2. **CORS Misconfiguration**:
   - Wildcard origins with credentials, overly broad origin reflection, insecure regex/substring matching, or cross-origin exposure of sensitive endpoints.
   - CORS settings that allow untrusted origins to read authenticated or sensitive responses.
3. **Debug Mode Enabled**:
   - Production-reachable debug flags, developer consoles, profiling endpoints, hot reload tooling, verbose exception pages, or unsafe diagnostics enabled by configuration.
4. **Unsafe Deserialization**:
   - Framework, library, or serializer settings that enable unsafe deserialization, polymorphic type handling, object binders, YAML unsafe load behavior, or equivalent dangerous parser modes.
   - Report this Phase 7 subtype when the risk is materially created or enabled by configuration, library choice, component setup, or insecure parser settings.
5. **Vulnerable Third-Party Component**:
   - Dependency versions, plugins, middleware, parsers, template engines, upload libraries, auth libraries, or frontend packages that are clearly insecure, unsupported, or known-vulnerable based on version and repository evidence.
   - You MUST tie component findings to actual project usage, reachable code paths, or exposed features. If you know a specific CVE or GHSA with high confidence, include it; otherwise leave `+"`"+`reference_id`+"`"+` empty.

**EXECUTION RULES**:
1. **Check Real Config Sources**: Inspect actual files such as ".env", ".env.*", "application*.yml", "application*.properties", "config*.json", YAML files, Docker/K8s manifests, server configs, "package.json", lock files, "go.mod", and framework initialization code.
2. **Map Exposure**: If a configuration issue affects real routes, list the affected endpoints and provide a raw HTTP POC.
3. **Static Issues Are Allowed**: If an issue is static-only and has no direct HTTP trigger, set `+"`"+`proof_type`+"`"+` to `+"`"+`Static`+"`"+`, leave trigger fields and `+"`"+`poc_http`+"`"+` empty if needed, and provide precise `+"`"+`reproduction_steps`+"`"+`.
4. **Component Precision**: For third-party components, you MUST identify the package/component name, version when available, the relevant usage point, and why the version or configuration is unsafe.
5. **Evidence Required**: Provide file path, line number, function/component name, and the exact vulnerable setting, component initialization, or dependency evidence.
6. **Upgrade Guidance**: When a component or config should be changed, provide a concrete `+"`"+`upgrade_recommendation`+"`"+`.
7. **No Guessing**: Only report issues where repository evidence supports the claim. If advisory IDs are uncertain, leave `+"`"+`reference_id`+"`"+` empty.
8. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
9. **Progress Memory**: Maintain a running list of checked config files, manifests, and components within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- `+"`"+`type`+"`"+` must be `+"`"+`Configuration`+"`"+`.
- `+"`"+`subtype`+"`"+` must be exactly one of: `+"`"+`Sensitive Information Exposure`+"`"+`, `+"`"+`CORS Misconfiguration`+"`"+`, `+"`"+`Debug Mode Enabled`+"`"+`, `+"`"+`Unsafe Deserialization`+"`"+`, `+"`"+`Vulnerable Third-Party Component`+"`"+`.
- `+"`"+`proof_type`+"`"+` must be exactly `+"`"+`HTTP`+"`"+` or `+"`"+`Static`+"`"+`.
- `+"`"+`configuration_item`+"`"+` should identify the insecure setting, config key, secret name, serializer option, or package.
- `+"`"+`component_name`+"`"+` and `+"`"+`component_version`+"`"+` should be populated for dependency/component findings; otherwise use empty strings.
- `+"`"+`reference_id`+"`"+` may contain `+"`"+`CVE-...`+"`"+` or `+"`"+`GHSA-...`+"`"+` when confidently known; otherwise use an empty string.
- `+"`"+`affected_endpoints`+"`"+` must be a JSON array of strings like `+"`"+`GET /api/users`+"`"+`.
- `+"`"+`exposure_mechanism`+"`"+` must explain how the configuration or component creates exploitable exposure.
- `+"`"+`poc_http`+"`"+` must contain a raw HTTP request for HTTP-reachable issues; otherwise it may be an empty string.
- `+"`"+`reproduction_steps`+"`"+` must always explain how to verify or exploit the issue.
- `+"`"+`upgrade_recommendation`+"`"+` should contain a concrete remediation or upgrade path when applicable.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "Configuration",
      "subtype": "Sensitive Information Exposure",
      "severity": "High",
      "location": { "file": "config/app.yml", "line": 12, "function": "loadConfig" },
      "trigger": { "method": "", "path": "", "parameter": "" },
      "proof_type": "Static",
      "configuration_item": "jwt.secret",
      "component_name": "",
      "component_version": "",
      "reference_id": "",
      "affected_endpoints": ["GET /api/admin", "POST /api/login"],
      "exposure_mechanism": "A production JWT signing secret is committed in plaintext, allowing offline token forgery once the repository or deployment artifact is exposed.",
      "description": "The application ships with a hardcoded JWT secret in a checked-in configuration file.",
      "execution_logic": "The config loader reads the plaintext secret and passes it directly into the JWT signing and verification path, making all token-protected routes dependent on a leaked static secret.",
      "vulnerable_code": "jwt:\n  secret: my-dev-secret",
      "poc_http": "",
      "reproduction_steps": "1. Read the committed config value. 2. Forge a JWT signed with the leaked secret. 3. Send the forged token to an affected endpoint and observe successful authorization.",
      "upgrade_recommendation": "Move the secret to a protected environment variable or secret manager and rotate the compromised signing key immediately.",
      "impact": "Attackers who obtain the codebase or deployed config can forge valid tokens and compromise all routes that trust this secret."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

   **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "fileop" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 8: File Operation Security Audit.
Your goal is to find confirmed file operation vulnerabilities, including arbitrary file upload, arbitrary file download/read, path traversal, and file inclusion.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE FILE-OPERATION REVIEW**
- You MUST exhaustively inspect all upload handlers, download handlers, file readers, archive extractors, import/export flows, static file serving code, template/file include logic, and any path concatenation or filesystem access helpers.
- Do NOT stop after finding one issue.
- Every reported issue MUST be tied to a real HTTP entry point and evidenced by repository code. Do NOT guess.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand entry points.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** Phase 8 vulnerabilities in the code.
4. Stay within file-operation scope. Do NOT report pure RCE, generic injection, XSS, auth, or authorization issues unless the file operation flaw itself is the core root cause.

**AUDIT TARGETS (Phase 8)**:
1. **Arbitrary File Upload**:
   - Missing or ineffective extension, MIME type, content signature, size, storage path, or executable file validation.
   - User-controlled filenames or upload destinations that allow overwriting sensitive files or placing dangerous files in executable/web-accessible locations.
2. **Arbitrary File Download/Read**:
   - Endpoints that let attackers read or download arbitrary local files by controlling filenames, IDs, aliases, keys, or paths.
   - Export or attachment endpoints that resolve attacker-controlled values into filesystem paths without strict allowlisting.
3. **Path Traversal**:
   - `+"`"+`../`+"`"+`, absolute path, symlink, archive entry, or path normalization bypasses that escape the intended directory boundary.
   - Unsafe use of `+"`"+`filepath.Join`+"`"+`, string concatenation, archive extraction, or path cleaning without final boundary enforcement.
4. **File Inclusion**:
   - User-controlled template names, include paths, local file inclusion, dynamic theme/plugin loading, or file-backed render paths that let attackers select arbitrary files.
   - Report this subtype only when code shows that attacker input can influence an include/render/read target.

**EXECUTION RULES**:
1. **Trace Input to Filesystem Action**: You MUST trace attacker-controlled input from the HTTP route to the exact file operation sink.
2. **Evidence Required**: Provide file path, line number, function name, and the exact filesystem or include logic.
3. **Trigger Precision**: Each finding MUST identify the exact route, method, and parameter used to reach the vulnerable file operation.
4. **Validation Analysis**: Explain what validation or normalization exists and why it is missing, bypassable, or ineffective.
5. **Path Impact**: State the affected target path, path pattern, storage location, or include target the attacker can influence.
6. **POC Construction**: For every confirmed issue, provide BOTH:
   - `+"`"+`poc_http`+"`"+`: a raw HTTP request demonstrating the vulnerable file operation.
   - `+"`"+`trigger_steps`+"`"+`: concise steps needed to exploit or verify the issue.
7. **No Static-Only Findings**: Only report issues that are reachable from a real HTTP endpoint and can be exercised through a request path.
8. **No Guessing**: Only report vulnerabilities where the route-to-input-to-file-operation chain is evidenced by code.
9. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
10. **Progress Memory**: Maintain a running list of checked file-operation sinks and files within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- `+"`"+`type`+"`"+` must be `+"`"+`FileOperation`+"`"+`.
- `+"`"+`subtype`+"`"+` must be exactly one of: `+"`"+`Arbitrary File Upload`+"`"+`, `+"`"+`Arbitrary File Download/Read`+"`"+`, `+"`"+`Path Traversal`+"`"+`, `+"`"+`File Inclusion`+"`"+`.
- `+"`"+`file_operation`+"`"+` must be exactly one of: `+"`"+`upload`+"`"+`, `+"`"+`download`+"`"+`, `+"`"+`read`+"`"+`, `+"`"+`include`+"`"+`.
- `+"`"+`input_vector`+"`"+` should describe the attacker-controlled field, filename, form part, query parameter, path parameter, header, or body key.
- `+"`"+`target_path`+"`"+` should describe the sensitive file path, path pattern, storage directory, include target, or resolved filesystem location.
- `+"`"+`validation_logic`+"`"+` must explain the existing validation/path normalization logic and why it fails.
- `+"`"+`payload_hint`+"`"+` should contain a representative filename, traversal string, multipart filename, include target, or path payload.
- `+"`"+`poc_http`+"`"+` must contain a raw HTTP request.
- `+"`"+`trigger_steps`+"`"+` must describe how to exploit or verify the issue.
- `+"`"+`impact`+"`"+` must summarize the security consequence.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "FileOperation",
      "subtype": "Path Traversal",
      "severity": "High",
      "location": { "file": "internal/handler/download.go", "line": 61, "function": "DownloadReport" },
      "trigger": { "method": "GET", "path": "/api/reports/download", "parameter": "name" },
      "file_operation": "download",
      "input_vector": "Query parameter: name",
      "target_path": "reports directory joined with attacker-controlled filename",
      "validation_logic": "The handler joins the base reports directory with the user-supplied name but never verifies that the resolved path stays inside the reports root.",
      "payload_hint": "..\\\\..\\\\windows\\\\win.ini",
      "description": "An attacker can traverse out of the reports directory and download arbitrary local files.",
      "execution_logic": "The handler reads the `+"`"+`name`+"`"+` query parameter, joins it with the reports directory, and serves the resolved file without enforcing a post-normalization boundary check.",
      "vulnerable_code": "target := filepath.Join(reportDir, c.Query(\"name\"))\nc.File(target)",
      "poc_http": "GET /api/reports/download?name=../../../../etc/passwd HTTP/1.1\nHost: target\nAuthorization: Bearer <token>\n...",
      "trigger_steps": "1. Send a crafted traversal payload in the `+"`"+`name`+"`"+` parameter. 2. Observe that the server returns a file outside the intended reports directory.",
      "impact": "Attackers can read sensitive local files and use the disclosed data to expand compromise."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else if stage == "logic" {
		routesContext := "[]"
		if len(task.OutputJSON) > 0 && string(task.OutputJSON) != "{}" {
			routesContext = string(task.OutputJSON)
		}

		prompt = fmt.Sprintf(`You are a Professional Security Auditor performing Phase 9: Business Logic Vulnerability Audit.
Your goal is to find confirmed business logic vulnerabilities, including workflow bypasses, race conditions, amount or quantity tampering, and business rule bypasses.

Base Path: %s

<known_routes_context>
%s
</known_routes_context>

**MISSION CRITICAL: EXHAUSTIVE BUSINESS-FLOW REVIEW**
- You MUST exhaustively inspect the core business workflows, state transitions, approval/order/payment/refund/inventory/reward/coupon flows, and all multi-step actions that change money, quantity, eligibility, ownership, or fulfillment state.
- Do NOT stop after finding one issue.
- Every reported issue MUST be backed by repository evidence. Do NOT guess.

**INSTRUCTIONS**:
1. The <known_routes_context> above lists API routes found in Phase 1. Use this ONLY to understand reachable entry points and workflow steps.
2. **DO NOT** output, repeat, or copy the content from <known_routes_context> in your final answer.
3. Your job is to find **NEW** Phase 9 vulnerabilities in the code.
4. Prefer findings that are tied to real HTTP routes and end-to-end business actions.
5. You MAY report a static-only business logic flaw when the vulnerable workflow is strongly evidenced by code but no direct HTTP trigger can be mapped confidently. In that case, set `+"`"+`proof_type`+"`"+` to `+"`"+`Static`+"`"+`, leave `+"`"+`trigger`+"`"+` fields and `+"`"+`poc_http`+"`"+` empty if needed, and explain the business flow precisely.
6. Do NOT report pure authentication, authorization, configuration, XSS, RCE, injection, or file-operation issues unless the primary security impact is a confirmed business logic flaw.

**AUDIT TARGETS (Phase 9)**:
1. **Workflow Bypass**:
   - Skipping required steps, approvals, confirmations, payment gates, status checks, or review gates.
   - Replaying or reordering requests to reach a forbidden state transition.
   - Missing idempotency or step-order validation that lets attackers complete a workflow incorrectly.
2. **Race Condition**:
   - Concurrent actions causing duplicate redemption, overselling, double-withdrawal, double-refund, repeated coupon use, repeated reward issuance, or inconsistent state.
   - Check-then-act windows where concurrent requests can bypass stock, balance, quota, or uniqueness constraints.
3. **Amount/Quantity Tampering**:
   - Client-controlled or attacker-influenced prices, totals, discounts, tax, shipping, credits, points, quantities, refund amounts, or settlement values.
   - Server trust in client-submitted calculation results without authoritative recomputation.
4. **Business Rule Bypass**:
   - Bypassing eligibility checks, one-time-use rules, usage quotas, frequency limits, minimum/maximum thresholds, first-user/new-user restrictions, tenant/business constraints, or redemption prerequisites.
   - Missing validation on business-critical conditions even when authentication and authorization are otherwise correct.

**EXECUTION RULES**:
1. **Map the Workflow**: Identify the core business object, the intended action, and the expected state transitions before deciding whether a logic flaw exists.
2. **Trace Multi-Step Chains**: For each confirmed issue, enumerate the involved endpoints or code path steps, not just the final sink.
3. **Protect Business-Critical Fields**: Inspect how the code handles amount, quantity, price, balance, inventory, discount, refund, quota, and status fields.
4. **Concurrency Review**: Check for non-atomic read-modify-write logic, duplicate processing windows, missing locking, missing transaction isolation, or repeated submissions.
5. **Evidence Required**: Provide file path, line number, function name, and the exact business check, sequencing rule, or state transition that is missing or ineffective.
6. **HTTP Preferred, Static Allowed**: Prefer HTTP-reachable findings. Static-only findings are allowed only when the business flaw is still clearly evidenced by code.
7. **POC Construction**: For every confirmed issue, provide:
   - `+"`"+`poc_http`+"`"+`: a raw HTTP request when the issue is HTTP-reachable; otherwise use an empty string.
   - `+"`"+`trigger_steps`+"`"+`: concise steps to exploit or verify the issue. This field must always be present.
8. **No Guessing**: Only report vulnerabilities where the workflow-to-check-to-bypass chain is evidenced by code.
9. **Read Discipline**: You MUST use read_file with start_line/end_line and keep a checklist of files or line ranges already read. Do NOT re-read the same file/range.
10. **Progress Memory**: Maintain a running list of checked workflows, state transitions, and files within your own reasoning, and avoid repeating the same tool calls.

**OUTPUT FIELD REQUIREMENTS**:
- `+"`"+`type`+"`"+` must be `+"`"+`BusinessLogic`+"`"+`.
- `+"`"+`subtype`+"`"+` must be exactly one of: `+"`"+`Workflow Bypass`+"`"+`, `+"`"+`Race Condition`+"`"+`, `+"`"+`Amount/Quantity Tampering`+"`"+`, `+"`"+`Business Rule Bypass`+"`"+`.
- `+"`"+`proof_type`+"`"+` must be exactly `+"`"+`HTTP`+"`"+` or `+"`"+`Static`+"`"+`.
- `+"`"+`workflow_name`+"`"+` should identify the affected workflow, such as checkout, coupon redemption, refund approval, or reward claim.
- `+"`"+`business_action`+"`"+` should identify the attacker-controlled action, such as submit order, redeem coupon, approve payout, or retry refund.
- `+"`"+`affected_endpoints`+"`"+` must be a JSON array of strings like `+"`"+`POST /api/orders/checkout`+"`"+`.
- `+"`"+`preconditions`+"`"+` should summarize the state, role, prior step, or data condition required before exploitation.
- `+"`"+`state_transition`+"`"+` must explain the intended state flow and the improper state change the attacker can trigger.
- `+"`"+`manipulated_fields`+"`"+` must be a JSON array naming the business-critical fields or variables that can be altered or raced.
- `+"`"+`race_window`+"`"+` should describe the concurrency window for race conditions; otherwise it may be an empty string.
- `+"`"+`bypass_vector`+"`"+` must summarize how the attacker skips or defeats the business rule.
- `+"`"+`poc_http`+"`"+` must contain a raw HTTP request for HTTP-reachable issues; otherwise it may be an empty string.
- `+"`"+`trigger_steps`+"`"+` must describe how to reproduce or verify the issue.
- `+"`"+`impact`+"`"+` must summarize the business and security consequence.

**Output Format**:
- **Iterative Workflow**: Same as before (Think -> Act -> Loop).
- **Final Output**: A strict JSON array wrapped in a Markdown code block.

  Example Final Output (FORMAT ONLY - Real output must contain ALL confirmed vulnerabilities):
  `+"```json"+`
  [
    {
      "type": "BusinessLogic",
      "subtype": "Amount/Quantity Tampering",
      "severity": "High",
      "location": { "file": "internal/order/checkout.go", "line": 118, "function": "CreateOrder" },
      "trigger": { "method": "POST", "path": "/api/orders/checkout", "parameter": "total_amount" },
      "proof_type": "HTTP",
      "workflow_name": "Order checkout",
      "business_action": "Submit order with attacker-controlled total",
      "affected_endpoints": ["POST /api/orders/checkout", "POST /api/orders/pay"],
      "preconditions": "Attacker can create a normal order.",
      "state_transition": "The workflow should recompute payable amount from server-side cart and pricing rules before moving from draft to payable, but it accepts a client-supplied total and creates a payable order with the tampered amount.",
      "manipulated_fields": ["total_amount", "discount_amount", "quantity"],
      "race_window": "",
      "bypass_vector": "The server trusts client-supplied pricing fields instead of recalculating them from authoritative product and coupon data.",
      "description": "An attacker can reduce the order total by tampering with business-critical amount fields during checkout.",
      "execution_logic": "The checkout handler binds the client request, copies the submitted total directly into the order model, and persists it without recomputing the total from server-side cart items and discount rules.",
      "vulnerable_code": "order.TotalAmount = req.TotalAmount\norder.DiscountAmount = req.DiscountAmount",
      "poc_http": "POST /api/orders/checkout HTTP/1.1\nHost: target\nAuthorization: Bearer <token>\nContent-Type: application/json\n\n{\"cart_id\":123,\"total_amount\":0.01,\"discount_amount\":9999}",
      "trigger_steps": "1. Create a valid cart. 2. Submit checkout with a forged total_amount or discount_amount. 3. Observe that the order is created with the tampered payable value.",
      "impact": "Attackers can purchase goods or obtain services at an unauthorized price, causing direct financial loss and downstream accounting inconsistencies."
    }
  ]
  `+"```"+`

  If no vulnerabilities are found, output an empty JSON array: `+"```json"+` [] `+"```"+`

  **CRITICAL**: Do NOT include any conversational text, explanations, or summaries in your final response. Output ONLY the JSON code block.
`, task.BasePath, routesContext)
	} else {
		logFunc(fmt.Sprintf("Unknown stage: '%s' (len=%d)", stage, len(stage)))
		return
	}
	prompt, promptErr := adaptPromptForRunKind(prompt, task, stage, currentStage, kind)
	if promptErr != nil {
		failMessage := "Prompt preparation failed: " + promptErr.Error()
		task.Status = "failed"
		task.Result = failMessage
		saveTaskRecord(task)
		if currentStage != nil {
			currentStage.Status = "failed"
			currentStage.Result = failMessage
			saveTaskStageRecord(currentStage)
		}
		logFunc(failMessage)
		return
	}
	prompt = appendChineseNarrativeRules(prompt, stage)
	prompt = promptArtifactGuidance(prompt)

	var session *scanSession
	var err error
	if resume {
		session, err = loadScanSession(task, stage, prompt)
	} else {
		session, err = newScanSession(task, stage, prompt, true)
	}
	if err != nil {
		task.Status = "failed"
		task.Result = "Runtime initialization failed: " + err.Error()
		saveTaskRecord(task)
		if currentStage != nil {
			currentStage.Status = "failed"
			currentStage.Result = task.Result
			saveTaskStageRecord(currentStage)
		}
		logFunc(task.Result)
		return
	}
	if err := session.markStatus(runtimeStatusRunning); err != nil {
		task.Status = "failed"
		task.Result = "Runtime initialization failed: " + err.Error()
		saveTaskRecord(task)
		if currentStage != nil {
			currentStage.Status = "failed"
			currentStage.Result = task.Result
			saveTaskStageRecord(currentStage)
		}
		logFunc(task.Result)
		return
	}

	ctx := context.Background()
	logFunc(fmt.Sprintf("AI session initialized for stage %s. Starting analysis loop...", stage))

	failRun := func(prefix string, runErr error) {
		message := prefix + runErr.Error()
		task.Status = "failed"
		task.Result = message
		saveTaskRecord(task)
		if currentStage != nil {
			currentStage.Status = "failed"
			currentStage.Result = message
			saveTaskStageRecord(currentStage)
		}
		_ = session.markStatus(runtimeStatusFailed)
		logFunc(message)
	}

	// Chat Loop
	const maxIterations = 200
	softLimitBytes := config.Scanner.ContextCompression.SoftLimitBytes
	hardLimitBytes := config.Scanner.ContextCompression.HardLimitBytes
	logFunc(fmt.Sprintf(
		"Context limits configured: soft=%d bytes, hard=%d bytes, summary_window=%d messages.",
		softLimitBytes,
		hardLimitBytes,
		config.Scanner.ContextCompression.SummaryWindowMessages,
	))
	for i := 0; i < maxIterations; i++ {
		if pauseRequested(task.ID, stage) {
			task.Status = "paused"
			updateTaskStatus(task, "paused")
			if currentStage != nil {
				currentStage.Status = "paused"
				saveTaskStageRecord(currentStage)
			}
			_ = session.markStatus(runtimeStatusPaused)
			logFunc("Pause requested. Runtime state saved.")
			return
		}

		session.maybeUpdateSessionMemory(ctx, client, logFunc)

		entries, sessionErr := session.applyMicrocompact(session.buildChatEntries(), logFunc)
		if sessionErr != nil {
			failRun("Runtime error: ", sessionErr)
			return
		}

		messages := make([]openai.ChatCompletionMessage, 0, len(entries))
		for _, entry := range entries {
			messages = append(messages, entry.Chat)
		}

		var resp openai.ChatCompletionResponse
		var callErr error
		nonRetryableAbort := false

		safeMessages, sanitizeStats := sanitizeMessageHistory(messages)
		if sanitizeStats.changed() {
			if summary := sanitizeStats.summary("Sanitized conversation history before AI request"); summary != "" {
				logFunc(summary)
			}
		}
		messages = safeMessages

		flushLogs()

		resp, callErr = createChatCompletionWithRetry(ctx, client, openai.ChatCompletionRequest{
			Model:    config.AI.Model,
			Messages: messages,
			Tools:    Tools,
		}, chatCompletionRetryHooks{
			onRetry: func(attempt, maxAttempts int, attemptTimeout, nextTimeout, retryDelay time.Duration, err error, timeoutLike bool) {
				if timeoutLike {
					logFunc(fmt.Sprintf(
						"AI API timeout (attempt %d/%d) after %s: %v. Increasing request timeout to %s and retrying in %d seconds...",
						attempt,
						maxAttempts,
						attemptTimeout,
						err,
						nextTimeout,
						int(retryDelay/time.Second),
					))
					return
				}
				logFunc(fmt.Sprintf(
					"AI API Error (attempt %d/%d, timeout %s): %v. Retrying in %d seconds...",
					attempt,
					maxAttempts,
					attemptTimeout,
					err,
					int(retryDelay/time.Second),
				))
			},
			onNonRetryable: func(attempt, maxAttempts int, attemptTimeout time.Duration, err error) {
				nonRetryableAbort = true
				logFunc(fmt.Sprintf(
					"AI API Error (attempt %d/%d, timeout %s): %v. Error is not retryable; aborting request loop.",
					attempt,
					maxAttempts,
					attemptTimeout,
					err,
				))
			},
		})

		if callErr != nil {
			errPrefix := "AI Error after retries: "
			if nonRetryableAbort {
				errPrefix = "AI Error: "
			}
			failRun(errPrefix, callErr)
			return
		}

		msg := resp.Choices[0].Message
		if err := session.appendChatMessage(msg); err != nil {
			failRun("Runtime error: ", err)
			return
		}

		if msg.Content != "" {
			logFunc(fmt.Sprintf("AI: %s", msg.Content))
		}

		if len(msg.ToolCalls) == 0 {
			session.maybeUpdateSessionMemory(ctx, client, logFunc)
			_ = session.markStatus(runtimeStatusCompleted)
			task.Status = "completed"
			outputJSON, meta, finalizeErr := finalizeRunOutput(task, stage, currentStage, kind, msg.Content)
			if finalizeErr != nil {
				failRun("Result processing error: ", finalizeErr)
				return
			}

			if currentStage != nil {
				currentStage.Status = "completed"
				currentStage.Result = msg.Content
				currentStage.UpdatedAt = time.Now()
				currentStage.OutputJSON = outputJSON
				currentStage.Meta = meta
				saveTaskStageRecord(currentStage)
			} else {
				task.Result = msg.Content
				task.OutputJSON = outputJSON
			}

			saveTaskRecord(task)
			logFunc("Analysis completed.")
			return
		}

		for _, toolCall := range msg.ToolCalls {
			var toolResult string
			var artifactID string
			toolName := toolCall.Function.Name
			var args map[string]interface{}
			startTs := time.Now().UnixMilli()

			argStr := strings.TrimSpace(toolCall.Function.Arguments)
			if !strings.HasPrefix(argStr, "{") {
				if idx := strings.Index(argStr, "{"); idx != -1 {
					argStr = argStr[idx:]
				}
			}
			if !strings.HasSuffix(argStr, "}") {
				if idx := strings.LastIndex(argStr, "}"); idx != -1 {
					argStr = argStr[:idx+1]
				}
			}

			if err := json.Unmarshal([]byte(argStr), &args); err != nil {
				toolResult = fmt.Sprintf("Error parsing JSON arguments: %v", err)
			} else {
				cacheKey := toolName + "|" + argStr
				if cached, ok := toolCache[cacheKey]; ok {
					toolResult = cached
					logFunc(fmt.Sprintf("Tool cache hit: %s (%s)", toolName, argStr))
				} else {
					logFunc(fmt.Sprintf("Executing tool: %s (%s)", toolName, argStr))
					switch toolName {
					case "read_file":
						path := GetStringArg(args, "path")
						startLine := int(GetFloatArg(args, "start_line"))
						endLine := int(GetFloatArg(args, "end_line"))
						maxOutputBytes := int(GetFloatArg(args, "max_output_bytes"))
						resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
						if resolveErr != nil {
							toolResult = resolveErr.Error()
						} else {
							toolResult = ExecuteReadFile(resolvedPath, startLine, endLine, maxOutputBytes)
						}

					case "get_evidence":
						evidenceID := GetStringArg(args, "evidence_id")
						if evidenceID == "" {
							toolResult = "Error: evidence_id is required"
							break
						}
						record, ok := session.loadArtifact(evidenceID)
						if !ok {
							toolResult = fmt.Sprintf("Error: evidence_id %q not found in the current run.", evidenceID)
							break
						}
						toolResult = formatArtifactPayload(record)

					case "get_artifact":
						artifactIDArg := GetStringArg(args, "artifact_id")
						if artifactIDArg == "" {
							toolResult = "Error: artifact_id is required"
							break
						}
						record, ok := session.loadArtifact(artifactIDArg)
						if !ok {
							toolResult = fmt.Sprintf("Error: artifact_id %q not found in the current run.", artifactIDArg)
							break
						}
						toolResult = formatArtifactPayload(record)

					case "list_files":
						path := GetStringArg(args, "path")
						maxEntries := int(GetFloatArg(args, "max_entries"))
						resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
						if resolveErr != nil {
							toolResult = resolveErr.Error()
						} else {
							toolResult = ExecuteListFiles(resolvedPath, maxEntries)
						}

					case "list_dir_tree":
						path := GetStringArg(args, "path")
						maxDepth := int(GetFloatArg(args, "max_depth"))
						if maxDepth == 0 {
							maxDepth = 2
						}
						maxEntries := int(GetFloatArg(args, "max_entries"))
						resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
						if resolveErr != nil {
							toolResult = resolveErr.Error()
						} else {
							toolResult = ExecuteListDirTree(resolvedPath, maxDepth, maxEntries)
						}

					case "search_files":
						path := GetStringArg(args, "path")
						pattern := GetStringArg(args, "pattern")
						maxResults := int(GetFloatArg(args, "max_results"))
						offset := int(GetFloatArg(args, "offset"))
						resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
						if resolveErr != nil {
							toolResult = resolveErr.Error()
						} else {
							toolResult = ExecuteSearchFiles(resolvedPath, pattern, maxResults, offset)
						}

					case "grep_files":
						path := GetStringArg(args, "path")
						pattern := GetStringArg(args, "pattern")
						caseInsensitive := false
						if v, ok := args["case_insensitive"]; ok {
							if b, ok := v.(bool); ok {
								caseInsensitive = b
							}
						}
						maxResults := int(GetFloatArg(args, "max_results"))
						offset := int(GetFloatArg(args, "offset"))
						maxFiles := int(GetFloatArg(args, "max_files"))
						maxOutputBytes := int(GetFloatArg(args, "max_output_bytes"))
						resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
						if resolveErr != nil {
							toolResult = resolveErr.Error()
						} else {
							toolResult = ExecuteGrepFiles(resolvedPath, pattern, caseInsensitive, maxResults, offset, maxFiles, maxOutputBytes)
						}

					default:
						toolResult = fmt.Sprintf("Error: Unknown tool %s", toolName)
					}
					if _, ok := toolCache[cacheKey]; !ok {
						toolCache[cacheKey] = toolResult
						toolCacheOrder = append(toolCacheOrder, cacheKey)
						if len(toolCacheOrder) > maxToolCacheEntries {
							oldest := toolCacheOrder[0]
							toolCacheOrder = toolCacheOrder[1:]
							delete(toolCache, oldest)
						}
					}
				}

				if toolName == "read_file" && isSuccessfulReadResult(toolResult) {
					path := GetStringArg(args, "path")
					startLine := int(GetFloatArg(args, "start_line"))
					endLine := int(GetFloatArg(args, "end_line"))
					resolvedPath, resolveErr := resolveToolPath(task.BasePath, path)
					if resolveErr == nil {
						effectiveStartLine, effectiveEndLine := normalizeReadFileRange(startLine, endLine)
						record, recordErr := session.createArtifact(
							"read_file",
							toolName,
							displayToolPath(task.BasePath, resolvedPath),
							effectiveStartLine,
							effectiveEndLine,
							toolResult,
						)
						if recordErr == nil {
							artifactID = record.ID
							logFunc(fmt.Sprintf("Preserved read_file artifact %s for %s (%s).", record.ID, record.Path, formatLineRange(record.StartLine, record.EndLine)))
						}
					}
				}
			}

			elapsed := time.Now().UnixMilli() - startTs
			logFunc(fmt.Sprintf("Tool %s finished in %d ms, output %d bytes", toolName, elapsed, len(toolResult)))

			maxToolContent := 40000
			toolContent := toolResult
			if len(toolResult) > maxToolContent {
				toolContent = toolResult[:maxToolContent] + "\n... (Tool output truncated) ..."
			}
			if err := session.appendToolMessage(toolCall.ID, toolName, toolContent, artifactID); err != nil {
				failRun("Runtime error: ", err)
				return
			}

			if pauseRequested(task.ID, stage) {
				task.Status = "paused"
				updateTaskStatus(task, "paused")
				if currentStage != nil {
					currentStage.Status = "paused"
					saveTaskStageRecord(currentStage)
				}
				_ = session.markStatus(runtimeStatusPaused)
				logFunc("Pause requested. Runtime state saved.")
				return
			}
		}

		session.maybeUpdateSessionMemory(ctx, client, logFunc)

		entries = session.buildChatEntries()
		messages = make([]openai.ChatCompletionMessage, 0, len(entries))
		for _, entry := range entries {
			messages = append(messages, entry.Chat)
		}

		totalBytes := calculateContextBytes(messages)
		if totalBytes > hardLimitBytes {
			logFunc(fmt.Sprintf(
				"Hard context limit reached at %d bytes (soft=%d, hard=%d). Tail: %s",
				totalBytes,
				softLimitBytes,
				hardLimitBytes,
				summarizeMessageTailBytes(messages, 4),
			))
			if err := session.compressHistory(ctx, client, logFunc); err != nil {
				failRun("Compression error: ", err)
				return
			}
			continue
		}
		if totalBytes > softLimitBytes {
			originalBytes, updatedBytes, changed, truncateErr := session.truncateLastToolMessage(20000)
			if truncateErr != nil {
				failRun("Runtime error: ", truncateErr)
				return
			}
			if changed {
				logFunc(fmt.Sprintf(
					"Soft context limit reached at %d bytes (soft=%d, hard=%d). Truncated last tool output from %d to %d bytes.",
					totalBytes,
					softLimitBytes,
					hardLimitBytes,
					originalBytes,
					updatedBytes,
				))
			}
			entries = session.buildChatEntries()
			messages = make([]openai.ChatCompletionMessage, 0, len(entries))
			for _, entry := range entries {
				messages = append(messages, entry.Chat)
			}
			totalBytes = calculateContextBytes(messages)
			if totalBytes > hardLimitBytes {
				logFunc(fmt.Sprintf(
					"Soft truncation insufficient; still at %d bytes. Tail: %s",
					totalBytes,
					summarizeMessageTailBytes(messages, 4),
				))
				if err := session.compressHistory(ctx, client, logFunc); err != nil {
					failRun("Compression error: ", err)
					return
				}
				continue
			}
		}
	}

	_ = session.markStatus(runtimeStatusCompleted)
	task.Status = "completed"
	activeEntries := session.buildChatEntries()
	lastContent := ""
	if len(activeEntries) > 0 {
		lastContent = activeEntries[len(activeEntries)-1].Chat.Content
	}
	task.Result = "Analysis finished (limit reached). Last output: " + lastContent

	jsonPart := extractJSON(lastContent)
	var outputJSON json.RawMessage
	if json.Valid([]byte(jsonPart)) {
		outputJSON = json.RawMessage(jsonPart)
	} else {
		outputJSON = json.RawMessage("[]")
	}

	if currentStage != nil {
		currentStage.Status = "completed"
		currentStage.Result = lastContent
		currentStage.UpdatedAt = time.Now()
		currentStage.OutputJSON = outputJSON
		saveTaskStageRecord(currentStage)
	} else {
		task.OutputJSON = outputJSON
	}

	saveTaskRecord(task)
	logFunc("Analysis finished (limit reached).")
}
