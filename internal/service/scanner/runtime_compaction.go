package scanner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"codescan/internal/config"

	"github.com/sashabaranov/go-openai"
)

var compactableToolNames = map[string]struct{}{
	"read_file":          {},
	"list_files":         {},
	"list_dir_tree":      {},
	"search_files":       {},
	"grep_files":         {},
	"query_manifest":     {},
	"query_routes":       {},
	"query_stage_output": {},
}

func artifactIndex(records []runtimeArtifact) string {
	if len(records) == 0 {
		return ""
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		label := record.Type
		if label == "" {
			label = record.ToolName
		}
		if label == "" {
			label = "artifact"
		}
		rangePart := "range unknown"
		switch {
		case record.StartLine > 0 && record.EndLine > 0:
			rangePart = fmt.Sprintf("lines %d-%d", record.StartLine, record.EndLine)
		case record.StartLine > 0:
			rangePart = fmt.Sprintf("line %d+", record.StartLine)
		}
		pathPart := record.Path
		if pathPart == "" {
			pathPart = label
		}
		line := fmt.Sprintf("- %s | %s | %s | %d bytes", record.ID, pathPart, rangePart, record.OriginalBytes)
		if record.Truncated {
			line += " | truncated"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func formatArtifactPayload(record runtimeArtifact) string {
	rangePart := "range unknown"
	switch {
	case record.StartLine > 0 && record.EndLine > 0:
		rangePart = fmt.Sprintf("lines %d-%d", record.StartLine, record.EndLine)
	case record.StartLine > 0:
		rangePart = fmt.Sprintf("line %d+", record.StartLine)
	}
	return fmt.Sprintf(
		"ARTIFACT %s\nType: %s\nTool: %s\nPath: %s\nRange: %s\nCaptured At: %s\nOriginal Bytes: %d\nTruncated: %t\n\n%s",
		record.ID,
		record.Type,
		record.ToolName,
		record.Path,
		rangePart,
		record.CapturedAt.Format(time.RFC3339),
		record.OriginalBytes,
		record.Truncated,
		record.Content,
	)
}

func runtimeContinuationInstructions(hasArtifacts bool) string {
	if hasArtifacts {
		return "Please continue the task based on the summary above. Prefer get_artifact(artifact_id) before re-reading code or re-running older searches. get_evidence(evidence_id) remains available for read_file snippets."
	}
	return "Please continue the task based on the summary above. Refer to the initial instructions in the first message."
}

func promptArtifactGuidance(prompt string) string {
	return strings.TrimSpace(prompt) + `

Context Retention Rules:
- If a PRESERVED ARTIFACT INDEX appears later in the conversation, prefer get_artifact(artifact_id) before re-reading code or re-running older searches.
- get_evidence(evidence_id) remains available for preserved read_file snippets.`
}

func (s *scanSession) applyMicrocompact(entries []chatEntry, logFunc func(string)) ([]chatEntry, error) {
	keepRecent := config.Scanner.ContextCompression.MicrocompactKeepRecent
	if keepRecent <= 0 {
		keepRecent = 2
	}

	roundByMessage := map[string]int{}
	currentRound := -1
	for _, entry := range entries {
		if len(entry.Chat.ToolCalls) > 0 {
			currentRound++
		}
		if entry.Chat.Role == openai.ChatMessageRoleTool {
			roundByMessage[entry.Message.ID] = currentRound
		}
	}

	roundSeen := map[int]struct{}{}
	protectedRounds := map[int]struct{}{}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		round, ok := roundByMessage[entry.Message.ID]
		if !ok {
			continue
		}
		if _, seen := roundSeen[round]; !seen {
			roundSeen[round] = struct{}{}
			if len(protectedRounds) < keepRecent {
				protectedRounds[round] = struct{}{}
			}
		}
	}

	updatedTranscript := false
	clearedCount := 0
	clearedBytes := 0

	for i, entry := range entries {
		if entry.Message.ID == runtimeRootPromptMessageID || entry.Chat.Role != openai.ChatMessageRoleTool {
			continue
		}
		if _, ok := compactableToolNames[entry.Message.ToolName]; !ok {
			continue
		}
		if _, ok := protectedRounds[roundByMessage[entry.Message.ID]]; ok {
			continue
		}
		if _, ok := s.state.Microcompact.ClearedMessages[entry.Message.ID]; ok {
			continue
		}

		artifactID := entry.Message.ArtifactID
		if artifactID == "" {
			record, err := s.createArtifact("tool_output", entry.Message.ToolName, "", 0, 0, entry.Message.Content)
			if err != nil {
				return nil, err
			}
			artifactID = record.ID
			for idx := range s.transcript {
				if s.transcript[idx].ID == entry.Message.ID {
					s.transcript[idx].ArtifactID = artifactID
					updatedTranscript = true
					entry.Message.ArtifactID = artifactID
					entries[i].Message.ArtifactID = artifactID
					break
				}
			}
		}

		placeholder := fmt.Sprintf(defaultMicrocompactPlaceholder, artifactID)
		s.state.Microcompact.ClearedMessages[entry.Message.ID] = microcompactRecord{
			ArtifactID:  artifactID,
			Placeholder: placeholder,
			ToolName:    entry.Message.ToolName,
			CompactedAt: time.Now(),
		}
		s.state.Microcompact.TotalCleared++
		clearedCount++
		clearedBytes += len(entry.Message.Content) - len(placeholder)
		entries[i].Chat.Content = placeholder
	}

	if updatedTranscript {
		if err := s.rewriteTranscript(); err != nil {
			return nil, err
		}
	}
	if clearedCount == 0 {
		return entries, nil
	}
	if err := s.saveState(); err != nil {
		return nil, err
	}
	if err := s.appendSynthetic(openai.ChatMessageRoleSystem, runtimeKindMicrocompact, fmt.Sprintf("Microcompacted %d older tool results, reclaiming approximately %d bytes.", clearedCount, clearedBytes), nil); err != nil {
		return nil, err
	}
	if logFunc != nil {
		logFunc(fmt.Sprintf("Microcompact cleared %d older tool results, reclaiming approximately %d bytes.", clearedCount, clearedBytes))
	}
	return s.buildChatEntries(), nil
}

type sessionMemoryDelta struct {
	Messages    []transcriptMessage
	GrowthBytes int
	ToolCalls   int
	LastID      string
}

func pendingSessionMemoryDelta(messages []transcriptMessage, lastMemoryMessageID string) sessionMemoryDelta {
	start := 0
	if lastMemoryMessageID != "" {
		for i, msg := range messages {
			if msg.ID == lastMemoryMessageID {
				start = i + 1
				break
			}
		}
	}
	capHint := len(messages) - start
	if capHint < 0 {
		capHint = 0
	}
	delta := sessionMemoryDelta{
		Messages: make([]transcriptMessage, 0, capHint),
	}
	for _, msg := range messages[start:] {
		if msg.Kind == runtimeKindCompactBoundary || msg.Kind == runtimeKindMicrocompact {
			continue
		}
		delta.Messages = append(delta.Messages, msg)
		delta.GrowthBytes += len(msg.Content)
		delta.ToolCalls += len(msg.ToolCalls)
		delta.LastID = msg.ID
	}
	return delta
}

func messagesToTranscriptText(messages []transcriptMessage, maxBytes int) string {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Kind == runtimeKindCompactBoundary || msg.Kind == runtimeKindMicrocompact {
			continue
		}
		roleLabel := msg.Role
		if msg.ToolName != "" {
			roleLabel += ":" + msg.ToolName
		}
		line := fmt.Sprintf("[%s] %s\n", roleLabel, strings.TrimSpace(msg.Content))
		if sb.Len()+len(line) > maxBytes && maxBytes > 0 {
			remaining := maxBytes - sb.Len()
			if remaining > 0 {
				sb.WriteString(line[:remaining])
			}
			sb.WriteString("\n... (Transcript truncated for memory update) ...")
			break
		}
		sb.WriteString(line)
		for _, tc := range msg.ToolCalls {
			callLine := fmt.Sprintf("[assistant:tool_call] %s %s\n", tc.Name, tc.Arguments)
			if sb.Len()+len(callLine) > maxBytes && maxBytes > 0 {
				sb.WriteString("... (Transcript truncated for memory update) ...")
				return sb.String()
			}
			sb.WriteString(callLine)
		}
	}
	return strings.TrimSpace(sb.String())
}

func memoryUpdatePrompt(existingMemory, transcriptText string) string {
	return fmt.Sprintf(`You are maintaining a persistent markdown session memory for an autonomous code-scanning agent.
Update the memory so a future agent can resume work without losing key context.

Output markdown only with these sections:
- Session Title
- Current State
- Task Spec
- Files and Functions
- Workflow
- Errors and Corrections
- Findings
- Next Steps
- Worklog

Current memory:
%s

New transcript delta:
%s`, strings.TrimSpace(existingMemory), strings.TrimSpace(transcriptText))
}

func sessionMemoryRetryDelay(base time.Duration, retry int) time.Duration {
	if base <= 0 || retry <= 0 {
		return 0
	}
	delay := base
	for i := 1; i < retry; i++ {
		delay *= 2
	}
	return delay
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *scanSession) sessionMemoryCooldownRemaining(now time.Time) time.Duration {
	cooldown := time.Duration(config.Scanner.SessionMemory.FailureCooldownSeconds) * time.Second
	if cooldown <= 0 || s.state.ConsecutiveMemoryFailures == 0 || s.state.LastMemoryFailureAt.IsZero() {
		return 0
	}
	retryAt := s.state.LastMemoryFailureAt.Add(cooldown)
	if !retryAt.After(now) {
		return 0
	}
	return retryAt.Sub(now)
}

func (s *scanSession) requestSessionMemoryUpdate(
	ctx context.Context,
	client *openai.Client,
	existingMemory string,
	transcriptText string,
	logFunc func(string),
) (string, error) {
	resp, err := createChatCompletionWithRetry(ctx, client, openai.ChatCompletionRequest{
		Model: config.AI.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: memoryUpdatePrompt(existingMemory, transcriptText)},
		},
	}, chatCompletionRetryHooks{
		onRetry: func(attempt, maxAttempts int, attemptTimeout, nextTimeout, retryDelay time.Duration, err error, timeoutLike bool) {
			if logFunc == nil {
				return
			}
			if timeoutLike {
				logFunc(fmt.Sprintf(
					"Session memory update timeout (attempt %d/%d) after %s: %v. Increasing request timeout to %s and retrying in %s...",
					attempt,
					maxAttempts,
					attemptTimeout,
					err,
					nextTimeout,
					retryDelay,
				))
				return
			}
			logFunc(fmt.Sprintf(
				"Session memory update error (attempt %d/%d, timeout %s): %v. Retrying in %s...",
				attempt,
				maxAttempts,
				attemptTimeout,
				err,
				retryDelay,
			))
		},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

func (s *scanSession) maybeUpdateSessionMemory(ctx context.Context, client *openai.Client, logFunc func(string)) {
	if !config.Scanner.SessionMemory.Enabled || !config.Scanner.ContextCompression.SessionMemoryEnabled {
		return
	}
	delta := pendingSessionMemoryDelta(s.transcript, s.state.LastMemoryMessageID)
	if delta.LastID == "" {
		return
	}
	if delta.GrowthBytes < config.Scanner.SessionMemory.MinGrowthBytes {
		return
	}
	if delta.ToolCalls < config.Scanner.SessionMemory.MinToolCalls {
		return
	}
	now := time.Now()
	if remaining := s.sessionMemoryCooldownRemaining(now); remaining > 0 {
		if logFunc != nil && s.state.LastMemoryCooldownLogAt.Before(s.state.LastMemoryFailureAt) {
			logFunc(fmt.Sprintf(
				"Session memory update cooling down for %s after %d consecutive failures.",
				remaining.Round(time.Second),
				s.state.ConsecutiveMemoryFailures,
			))
			s.state.LastMemoryCooldownLogAt = now
			if err := s.saveState(); err != nil {
				logFunc(fmt.Sprintf("Persisting session memory cooldown state failed: %v", err))
			}
		}
		return
	}

	existingMemory, err := s.readMemory()
	if err != nil {
		if logFunc != nil {
			logFunc(fmt.Sprintf("Session memory read failed: %v", err))
		}
		return
	}
	transcriptText := messagesToTranscriptText(delta.Messages, config.Scanner.SessionMemory.MaxUpdateBytes)
	if strings.TrimSpace(transcriptText) == "" {
		return
	}

	s.state.LastMemoryAttemptAt = now
	memory, err := s.requestSessionMemoryUpdate(ctx, client, existingMemory, transcriptText, logFunc)
	if err != nil {
		s.state.ConsecutiveMemoryFailures++
		s.state.LastMemoryFailureAt = time.Now()
		s.state.LastMemoryCooldownLogAt = time.Time{}
		if saveErr := s.saveState(); saveErr != nil && logFunc != nil {
			logFunc(fmt.Sprintf("Persisting session memory failure state failed: %v", saveErr))
		}
		if logFunc != nil {
			logFunc(fmt.Sprintf("Session memory update failed: %v", err))
		}
		return
	}
	if memory == "" {
		if logFunc != nil {
			logFunc("Session memory update returned empty content.")
		}
		return
	}
	if err := s.writeMemory(memory); err != nil {
		if logFunc != nil {
			logFunc(fmt.Sprintf("Persisting session memory failed: %v", err))
		}
		return
	}
	s.state.LastMemoryMessageID = delta.LastID
	s.state.ConsecutiveMemoryFailures = 0
	s.state.LastMemoryFailureAt = time.Time{}
	s.state.LastMemoryCooldownLogAt = time.Time{}
	if err := s.saveState(); err != nil && logFunc != nil {
		logFunc(fmt.Sprintf("Persisting session memory cursor failed: %v", err))
	}
	if logFunc != nil {
		logFunc("Session memory updated.")
	}
}

func selectTailEntries(entries []chatEntry, minTail int) ([]chatEntry, []chatEntry) {
	if minTail <= 0 || len(entries) <= minTail {
		return entries, nil
	}
	keepStart := len(entries) - minTail
	if keepStart < 0 {
		keepStart = 0
	}
	if keepStart < len(entries) && entries[keepStart].Chat.Role == openai.ChatMessageRoleTool {
		for i := keepStart; i >= 0; i-- {
			if len(entries[i].Chat.ToolCalls) > 0 {
				keepStart = i
				break
			}
		}
	}
	if keepStart <= 0 {
		return entries, nil
	}
	return entries[:keepStart], entries[keepStart:]
}

func compactPrompt() string {
	return `The current conversation context is becoming too long.
Summarize the conversation so another autonomous coding agent can continue immediately.

Include:
1. The user goal and the current stage objective.
2. Important files, code paths, and evidence already inspected.
3. Key findings from tool calls.
4. Errors, dead ends, and fixes already attempted.
5. The current status.
6. The immediate next steps.

Do not call any tools.
Output plain text only.`
}

func (s *scanSession) trySessionMemoryCompaction(entries []chatEntry) (string, []chatEntry, bool) {
	if !config.Scanner.SessionMemory.Enabled || !config.Scanner.ContextCompression.SessionMemoryEnabled {
		return "", nil, false
	}
	memory, err := s.readMemory()
	if err != nil || strings.TrimSpace(memory) == "" {
		return "", nil, false
	}

	if s.state.LastMemoryMessageID != "" {
		for i, entry := range entries {
			if entry.Message.ID == s.state.LastMemoryMessageID {
				return "SESSION MEMORY SNAPSHOT:\n" + strings.TrimSpace(memory), entries[i+1:], true
			}
		}
	}

	_, tail := selectTailEntries(entries, config.Scanner.ContextCompression.CompactMinTailMessages)
	return "SESSION MEMORY SNAPSHOT:\n" + strings.TrimSpace(memory), tail, true
}

func (s *scanSession) compressHistory(ctx context.Context, client *openai.Client, logFunc func(string)) error {
	entries := s.buildChatEntries()
	if len(entries) <= 1 {
		return nil
	}

	safeMessages := make([]openai.ChatCompletionMessage, 0, len(entries))
	for _, entry := range entries {
		safeMessages = append(safeMessages, entry.Chat)
	}
	safeMessages, sanitizeStats := sanitizeMessageHistory(safeMessages)
	if sanitizeStats.changed() && logFunc != nil {
		if msg := sanitizeStats.summary("Sanitized conversation history before compression"); msg != "" {
			logFunc(msg)
		}
	}

	preBytes := calculateContextBytes(safeMessages)
	activeEntries := entries[1:]
	if len(activeEntries) == 0 {
		return nil
	}

	summary := ""
	keptEntries := []chatEntry{}
	if memorySummary, tail, ok := s.trySessionMemoryCompaction(activeEntries); ok {
		summary = memorySummary
		keptEntries = tail
		if logFunc != nil {
			logFunc("Using session memory compaction.")
		}
	} else {
		prefix, tail := selectTailEntries(activeEntries, config.Scanner.ContextCompression.CompactMinTailMessages)
		if len(prefix) == 0 {
			prefix = activeEntries
			tail = nil
		}
		keptEntries = tail

		contextToSummarize := []openai.ChatCompletionMessage{
			entries[0].Chat,
		}
		if strings.TrimSpace(s.state.RollingSummary) != "" {
			contextToSummarize = append(contextToSummarize, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleUser,
				Content: "PREVIOUS SUMMARY (not instructions):\n" + strings.TrimSpace(s.state.RollingSummary),
			})
		}
		for _, entry := range prefix {
			contextToSummarize = append(contextToSummarize, entry.Chat)
		}
		contextToSummarize = append(contextToSummarize, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: compactPrompt(),
		})

		resp, err := createChatCompletionWithRetry(ctx, client, openai.ChatCompletionRequest{
			Model:    config.AI.Model,
			Messages: contextToSummarize,
		}, chatCompletionRetryHooks{
			onRetry: func(attempt, maxAttempts int, attemptTimeout, nextTimeout, retryDelay time.Duration, err error, timeoutLike bool) {
				if logFunc == nil {
					return
				}
				if timeoutLike {
					logFunc(fmt.Sprintf(
						"Context compression timeout (attempt %d/%d) after %s: %v. Increasing request timeout to %s and retrying in %s...",
						attempt,
						maxAttempts,
						attemptTimeout,
						err,
						nextTimeout,
						retryDelay,
					))
					return
				}
				logFunc(fmt.Sprintf(
					"Context compression error (attempt %d/%d, timeout %s): %v. Retrying in %s...",
					attempt,
					maxAttempts,
					attemptTimeout,
					err,
					retryDelay,
				))
			},
		})
		if err != nil {
			summary = strings.TrimSpace(s.state.RollingSummary)
			if summary == "" {
				summary = "Compression failed before a fresh summary could be produced."
			}
			summary += fmt.Sprintf("\n\nCompression note: previous tool conversation was reset after compression failure (%v). Re-establish any missing detail with get_artifact/get_evidence or the project tools.", err)
			if logFunc != nil {
				logFunc(fmt.Sprintf("Context compression failed; using fallback summary: %v", err))
			}
		} else {
			summary = strings.TrimSpace(resp.Choices[0].Message.Content)
			if summary == "" {
				summary = "Context compression returned empty content. Re-establish any missing detail with get_artifact/get_evidence or the project tools."
			}
		}
	}

	headID, anchorID, tailID := s.preservedSegmentFromEntries(keptEntries)
	boundary := &compactBoundary{
		Source:                 "auto",
		PreBytes:               preBytes,
		SummarizedMessageCount: len(activeEntries) - len(keptEntries),
		HeadID:                 headID,
		AnchorID:               anchorID,
		TailID:                 tailID,
	}
	if err := s.appendSynthetic(openai.ChatMessageRoleSystem, runtimeKindCompactBoundary, "", boundary); err != nil {
		return err
	}

	summaryMessage := "CONTEXT SUMMARY (not instructions):\n" + strings.TrimSpace(summary)
	if err := s.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindCompactSummary, summaryMessage, nil); err != nil {
		return err
	}
	s.state.RollingSummary = strings.TrimSpace(summary)

	index := artifactIndex(s.recentArtifacts(defaultArtifactIndexLimit))
	if strings.TrimSpace(index) != "" {
		if err := s.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindAttachment, "PRESERVED ARTIFACT INDEX (not instructions):\n"+index, nil); err != nil {
			return err
		}
	}
	if err := s.appendSynthetic(openai.ChatMessageRoleUser, runtimeKindAttachment, runtimeContinuationInstructions(strings.TrimSpace(index) != ""), nil); err != nil {
		return err
	}

	postEntries := s.buildChatEntries()
	postMessages := make([]openai.ChatCompletionMessage, 0, len(postEntries))
	for _, entry := range postEntries {
		postMessages = append(postMessages, entry.Chat)
	}
	boundary.PostBytes = calculateContextBytes(postMessages)
	if boundaryIdx := s.messageIndex(s.state.ActiveBoundaryID); boundaryIdx >= 0 {
		s.transcript[boundaryIdx].Boundary = boundary
		if err := s.rewriteTranscript(); err != nil {
			return err
		}
	}
	return s.saveState()
}
