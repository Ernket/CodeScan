package scanner

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"codescan/internal/model"

	"github.com/sashabaranov/go-openai"
)

type scanSession struct {
	task        *model.Task
	stage       string
	prompt      string
	runtimePath string
	state       runtimeState
	transcript  []transcriptMessage
}

func newScanSession(task *model.Task, stage, prompt string, reset bool) (*scanSession, error) {
	session := &scanSession{
		task:        task,
		stage:       stage,
		prompt:      prompt,
		runtimePath: task.StageRuntimePath(stage),
	}
	if reset {
		if err := os.RemoveAll(session.runtimePath); err != nil {
			return nil, fmt.Errorf("reset runtime state: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(session.runtimePath, runtimeArtifactsDir), 0o755); err != nil {
		return nil, fmt.Errorf("create runtime directories: %w", err)
	}

	if err := session.load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		session.state = runtimeState{
			Version:       1,
			TaskID:        task.ID,
			Stage:         stage,
			Status:        runtimeStatusRunning,
			NextMessageID: 1,
			UpdatedAt:     time.Now(),
			Microcompact: runtimeMicrocompactState{
				ClearedMessages: map[string]microcompactRecord{},
			},
		}
		if err := session.saveState(); err != nil {
			return nil, err
		}
	}

	if session.state.Microcompact.ClearedMessages == nil {
		session.state.Microcompact.ClearedMessages = map[string]microcompactRecord{}
	}

	return session, nil
}

func EnsureBootstrapAnchor(task *model.Task, stage string) error {
	task.BasePath = task.GetBasePath()
	_, err := newScanSession(task, stage, "", false)
	return err
}

func HasRuntimeBootstrapAnchor(task *model.Task, stage string) bool {
	task.BasePath = task.GetBasePath()
	_, err := os.Stat(filepath.Join(task.StageRuntimePath(stage), runtimeStateFile))
	return err == nil
}

func loadScanSession(task *model.Task, stage, prompt string) (*scanSession, error) {
	session := &scanSession{
		task:        task,
		stage:       stage,
		prompt:      prompt,
		runtimePath: task.StageRuntimePath(stage),
	}
	if err := session.load(); err != nil {
		return nil, err
	}
	if session.state.Version == 0 {
		return nil, fmt.Errorf("runtime state for stage %q is missing or invalid", stage)
	}
	if session.state.Microcompact.ClearedMessages == nil {
		session.state.Microcompact.ClearedMessages = map[string]microcompactRecord{}
	}
	return session, nil
}

func (s *scanSession) load() error {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("parse runtime state: %w", err)
	}

	file, err := os.Open(s.transcriptPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.transcript = nil
			return nil
		}
		return fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	messages := []transcriptMessage{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg transcriptMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return fmt.Errorf("parse transcript line: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	s.transcript = messages
	return nil
}

func (s *scanSession) statePath() string {
	return filepath.Join(s.runtimePath, runtimeStateFile)
}

func (s *scanSession) transcriptPath() string {
	return filepath.Join(s.runtimePath, runtimeTranscriptFile)
}

func (s *scanSession) memoryPath() string {
	return filepath.Join(s.runtimePath, runtimeMemoryFile)
}

func (s *scanSession) artifactDir() string {
	return filepath.Join(s.runtimePath, runtimeArtifactsDir)
}

func (s *scanSession) artifactPath(id string) string {
	return filepath.Join(s.artifactDir(), id+".json")
}

func (s *scanSession) saveState() error {
	s.state.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal runtime state: %w", err)
	}
	return writeFileAtomic(s.statePath(), data)
}

func (s *scanSession) rewriteTranscript() error {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for _, msg := range s.transcript {
		if err := enc.Encode(msg); err != nil {
			return fmt.Errorf("encode transcript: %w", err)
		}
	}
	return writeFileAtomic(s.transcriptPath(), []byte(sb.String()))
}

func (s *scanSession) appendTranscript(msg transcriptMessage) error {
	file, err := os.OpenFile(s.transcriptPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open transcript append: %w", err)
	}
	defer file.Close()

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal transcript message: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append transcript message: %w", err)
	}
	s.transcript = append(s.transcript, msg)
	s.state.LastMessageID = msg.ID
	return nil
}

func (s *scanSession) nextMessageID() string {
	id := fmt.Sprintf("msg-%06d", s.state.NextMessageID)
	s.state.NextMessageID++
	return id
}

func (s *scanSession) appendSynthetic(role, kind, content string, boundary *compactBoundary) error {
	parentID := s.state.LastMessageID
	msg := transcriptMessage{
		ID:        s.nextMessageID(),
		ParentID:  parentID,
		Role:      role,
		Kind:      kind,
		Content:   content,
		Boundary:  boundary,
		CreatedAt: time.Now(),
	}
	if err := s.appendTranscript(msg); err != nil {
		return err
	}
	if kind == runtimeKindCompactBoundary {
		s.state.ActiveBoundaryID = msg.ID
	}
	if kind == runtimeKindCompactSummary {
		s.state.LastCompactSummaryID = msg.ID
	}
	return s.saveState()
}

func (s *scanSession) appendChatMessage(msg openai.ChatCompletionMessage) error {
	parentID := s.state.LastMessageID
	tmsg := transcriptMessage{
		ID:         s.nextMessageID(),
		ParentID:   parentID,
		Role:       msg.Role,
		Kind:       runtimeKindNormal,
		Content:    msg.Content,
		ToolCallID: msg.ToolCallID,
		CreatedAt:  time.Now(),
	}
	if len(msg.ToolCalls) > 0 {
		tmsg.ToolCalls = make([]runtimeToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			tmsg.ToolCalls = append(tmsg.ToolCalls, runtimeToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	if err := s.appendTranscript(tmsg); err != nil {
		return err
	}
	return s.saveState()
}

func (s *scanSession) appendToolMessage(toolCallID, toolName, content, artifactID string) error {
	parentID := s.state.LastMessageID
	msg := transcriptMessage{
		ID:         s.nextMessageID(),
		ParentID:   parentID,
		Role:       openai.ChatMessageRoleTool,
		Kind:       runtimeKindNormal,
		Content:    content,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		ArtifactID: artifactID,
		CreatedAt:  time.Now(),
	}
	if err := s.appendTranscript(msg); err != nil {
		return err
	}
	return s.saveState()
}

func (s *scanSession) messageIndex(id string) int {
	for i, msg := range s.transcript {
		if msg.ID == id {
			return i
		}
	}
	return -1
}

func (s *scanSession) activeMessages() []transcriptMessage {
	if s.state.ActiveBoundaryID == "" {
		return append([]transcriptMessage(nil), s.transcript...)
	}
	boundaryIdx := s.messageIndex(s.state.ActiveBoundaryID)
	if boundaryIdx == -1 {
		return append([]transcriptMessage(nil), s.transcript...)
	}

	active := make([]transcriptMessage, 0, len(s.transcript)-boundaryIdx)
	boundary := s.transcript[boundaryIdx]
	if boundary.Boundary != nil && boundary.Boundary.HeadID != "" && boundary.Boundary.TailID != "" {
		headIdx := s.messageIndex(boundary.Boundary.HeadID)
		tailIdx := s.messageIndex(boundary.Boundary.TailID)
		if headIdx >= 0 && tailIdx >= headIdx {
			active = append(active, s.transcript[headIdx:tailIdx+1]...)
		}
	}
	active = append(active, s.transcript[boundaryIdx+1:]...)
	return active
}

type chatEntry struct {
	Message transcriptMessage
	Chat    openai.ChatCompletionMessage
}

func (s *scanSession) buildChatEntries() []chatEntry {
	entries := make([]chatEntry, 0, len(s.activeMessages())+1)
	entries = append(entries, chatEntry{
		Message: transcriptMessage{
			ID:        runtimeRootPromptMessageID,
			Role:      openai.ChatMessageRoleUser,
			Kind:      runtimeKindNormal,
			Content:   s.prompt,
			CreatedAt: time.Now(),
		},
		Chat: openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: s.prompt,
		},
	})

	for _, msg := range s.activeMessages() {
		if msg.ParentID == "" && msg.Role == openai.ChatMessageRoleUser && msg.Content == s.prompt {
			continue
		}
		chat, ok := s.toChatMessage(msg)
		if !ok {
			continue
		}
		entries = append(entries, chatEntry{Message: msg, Chat: chat})
	}
	return entries
}

func (s *scanSession) toChatMessage(msg transcriptMessage) (openai.ChatCompletionMessage, bool) {
	switch msg.Kind {
	case runtimeKindCompactBoundary, runtimeKindMicrocompact:
		return openai.ChatCompletionMessage{}, false
	}

	content := msg.Content
	if cleared, ok := s.state.Microcompact.ClearedMessages[msg.ID]; ok {
		content = cleared.Placeholder
	}

	out := openai.ChatCompletionMessage{
		Role:       msg.Role,
		Content:    content,
		ToolCallID: msg.ToolCallID,
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = make([]openai.ToolCall, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, openai.ToolCall{
				ID:   tc.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
	}
	return out, true
}

func (s *scanSession) createArtifact(kind, toolName, path string, startLine, endLine int, content string) (runtimeArtifact, error) {
	id := ""
	if kind == "read_file" {
		s.state.NextEvidenceID++
		id = fmt.Sprintf("ev-%d", s.state.NextEvidenceID)
	} else {
		s.state.NextArtifactID++
		id = fmt.Sprintf("art-%d", s.state.NextArtifactID)
	}
	artifact := runtimeArtifact{
		ID:            id,
		Type:          kind,
		ToolName:      toolName,
		Path:          path,
		StartLine:     startLine,
		EndLine:       endLine,
		OriginalBytes: len(content),
		CapturedAt:    time.Now(),
		Content:       content,
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return runtimeArtifact{}, fmt.Errorf("marshal artifact: %w", err)
	}
	if err := writeFileAtomic(s.artifactPath(id), data); err != nil {
		return runtimeArtifact{}, err
	}
	s.state.ArtifactOrder = append(s.state.ArtifactOrder, id)
	if err := s.saveState(); err != nil {
		return runtimeArtifact{}, err
	}
	return artifact, nil
}

const toolTranscriptSafeBytes = 40 * 1024

func prepareToolMessageForTranscript(session *scanSession, toolName, content, artifactID string) (string, string, error) {
	if len(content) <= toolTranscriptSafeBytes {
		return content, artifactID, nil
	}

	if artifactID == "" {
		record, err := session.createArtifact("tool_output", toolName, "", 0, 0, content)
		if err != nil {
			return "", "", err
		}
		artifactID = record.ID
	}

	truncated := content[:toolTranscriptSafeBytes] + fmt.Sprintf(
		"\n... (Tool output moved to artifact %s; use get_artifact to recover full content) ...",
		artifactID,
	)
	return truncated, artifactID, nil
}

func (s *scanSession) loadArtifact(id string) (runtimeArtifact, bool) {
	data, err := os.ReadFile(s.artifactPath(id))
	if err != nil {
		return runtimeArtifact{}, false
	}
	var artifact runtimeArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return runtimeArtifact{}, false
	}
	return artifact, true
}

func (s *scanSession) recentArtifacts(limit int) []runtimeArtifact {
	if limit <= 0 {
		limit = defaultArtifactIndexLimit
	}
	start := len(s.state.ArtifactOrder) - limit
	if start < 0 {
		start = 0
	}
	records := make([]runtimeArtifact, 0, len(s.state.ArtifactOrder)-start)
	for _, id := range s.state.ArtifactOrder[start:] {
		if record, ok := s.loadArtifact(id); ok {
			records = append(records, record)
		}
	}
	return records
}

func (s *scanSession) writeMemory(content string) error {
	if err := writeFileAtomic(s.memoryPath(), []byte(content)); err != nil {
		return err
	}
	s.state.MemoryUpdatedAt = time.Now()
	return s.saveState()
}

func (s *scanSession) readMemory() (string, error) {
	data, err := os.ReadFile(s.memoryPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

func (s *scanSession) markStatus(status string) error {
	s.state.Status = status
	return s.saveState()
}

func (s *scanSession) rootPromptMessage() transcriptMessage {
	for _, msg := range s.transcript {
		if msg.ParentID == "" && msg.Role == openai.ChatMessageRoleUser {
			return msg
		}
	}
	return transcriptMessage{
		ID:        runtimeRootPromptMessageID,
		Role:      openai.ChatMessageRoleUser,
		Kind:      runtimeKindNormal,
		Content:   s.prompt,
		CreatedAt: time.Now(),
	}
}

func (s *scanSession) preservedSegmentFromEntries(entries []chatEntry) (headID, anchorID, tailID string) {
	usable := make([]chatEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Message.ID == runtimeRootPromptMessageID {
			continue
		}
		usable = append(usable, entry)
	}
	if len(usable) == 0 {
		return "", "", ""
	}
	headID = usable[0].Message.ID
	tailID = usable[len(usable)-1].Message.ID
	if len(usable) > 1 {
		anchorID = usable[len(usable)-2].Message.ID
	}
	return headID, anchorID, tailID
}

func (s *scanSession) clearMicrocompactRecordsForMessages(ids []string) {
	if len(ids) == 0 {
		return
	}
	for _, id := range ids {
		delete(s.state.Microcompact.ClearedMessages, id)
	}
}

func (s *scanSession) truncateLastToolMessage(maxBytes int) (originalBytes int, updatedBytes int, changed bool, err error) {
	if maxBytes <= 0 || len(s.transcript) == 0 {
		return 0, 0, false, nil
	}
	last := &s.transcript[len(s.transcript)-1]
	if last.Role != openai.ChatMessageRoleTool || len(last.Content) <= maxBytes {
		return 0, 0, false, nil
	}
	if last.ArtifactID == "" {
		record, artifactErr := s.createArtifact("tool_output", last.ToolName, "", 0, 0, last.Content)
		if artifactErr != nil {
			return 0, 0, false, artifactErr
		}
		last.ArtifactID = record.ID
	}
	originalBytes = len(last.Content)
	last.Content = last.Content[:maxBytes] + "\n... (Soft truncated tool output; recover with get_artifact if needed) ..."
	updatedBytes = len(last.Content)
	if err := s.rewriteTranscript(); err != nil {
		return 0, 0, false, err
	}
	if err := s.saveState(); err != nil {
		return 0, 0, false, err
	}
	return originalBytes, updatedBytes, true, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	tmpPath := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.tmp-%d", filepath.Base(path), time.Now().UnixNano()))
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func selectResumableRuntimeStage(task *model.Task) (string, error) {
	root := task.RuntimeRootPath()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", os.ErrNotExist
		}
		return "", err
	}

	type candidate struct {
		stage string
		state runtimeState
	}
	candidates := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), runtimeStateFile))
		if err != nil {
			continue
		}
		var state runtimeState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		if state.Status == runtimeStatusPaused || state.Status == runtimeStatusRunning {
			candidates = append(candidates, candidate{stage: entry.Name(), state: state})
		}
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	slices.SortFunc(candidates, func(a, b candidate) int {
		if a.state.UpdatedAt.Equal(b.state.UpdatedAt) {
			return strings.Compare(b.stage, a.stage)
		}
		if a.state.UpdatedAt.After(b.state.UpdatedAt) {
			return -1
		}
		return 1
	})
	return candidates[0].stage, nil
}

func RuntimeStatus(task *model.Task, stage string) (string, error) {
	data, err := os.ReadFile(filepath.Join(task.StageRuntimePath(stage), runtimeStateFile))
	if err != nil {
		return "", err
	}

	var state runtimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return "", err
	}

	return state.Status, nil
}
