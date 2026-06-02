package scanner

import "time"

const (
	runtimeKindNormal              = "normal"
	runtimeKindCompactBoundary     = "compact_boundary"
	runtimeKindCompactSummary      = "compact_summary"
	runtimeKindAttachment          = "attachment"
	runtimeKindMicrocompact        = "microcompact_event"
	runtimeStatusRunning           = "running"
	runtimeStatusPaused            = "paused"
	runtimeStatusCompleted         = "completed"
	runtimeStatusFailed            = "failed"
	runtimeTranscriptFile          = "transcript.jsonl"
	runtimeStateFile               = "state.json"
	runtimeMemoryFile              = "memory.md"
	runtimeArtifactsDir            = "artifacts"
	runtimeRootPromptMessageID     = "root-prompt"
	defaultArtifactIndexLimit      = 8
	defaultMicrocompactPlaceholder = "[Older tool output cleared during context compression. Recover it with get_artifact(\"%s\").]"
)

type runtimeToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type compactBoundary struct {
	Source                 string `json:"source"`
	PreBytes               int    `json:"pre_bytes"`
	PostBytes              int    `json:"post_bytes"`
	SummarizedMessageCount int    `json:"summarized_message_count"`
	HeadID                 string `json:"head_id,omitempty"`
	AnchorID               string `json:"anchor_id,omitempty"`
	TailID                 string `json:"tail_id,omitempty"`
}

type transcriptMessage struct {
	ID               string            `json:"id"`
	ParentID         string            `json:"parent_id,omitempty"`
	Role             string            `json:"role"`
	Kind             string            `json:"kind"`
	Content          string            `json:"content,omitempty"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []runtimeToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
	ToolName         string            `json:"tool_name,omitempty"`
	ArtifactID       string            `json:"artifact_id,omitempty"`
	Boundary         *compactBoundary  `json:"boundary,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

type microcompactRecord struct {
	ArtifactID  string    `json:"artifact_id,omitempty"`
	Placeholder string    `json:"placeholder"`
	ToolName    string    `json:"tool_name,omitempty"`
	CompactedAt time.Time `json:"compacted_at"`
}

type runtimeMicrocompactState struct {
	ClearedMessages map[string]microcompactRecord `json:"cleared_messages,omitempty"`
	TotalCleared    int                           `json:"total_cleared"`
}

type runtimeState struct {
	Version                    int                      `json:"version"`
	TaskID                     string                   `json:"task_id"`
	Stage                      string                   `json:"stage"`
	Status                     string                   `json:"status"`
	ActiveBoundaryID           string                   `json:"active_boundary_id,omitempty"`
	LastCompactSummaryID       string                   `json:"last_compact_summary_id,omitempty"`
	LastMemoryMessageID        string                   `json:"last_memory_message_id,omitempty"`
	LastMessageID              string                   `json:"last_message_id,omitempty"`
	NextMessageID              int                      `json:"next_message_id"`
	NextArtifactID             int                      `json:"next_artifact_id"`
	NextEvidenceID             int                      `json:"next_evidence_id"`
	ArtifactOrder              []string                 `json:"artifact_order,omitempty"`
	RollingSummary             string                   `json:"rolling_summary,omitempty"`
	LastMemoryAttemptAt        time.Time                `json:"last_memory_attempt_at,omitempty"`
	LastMemoryFailureAt        time.Time                `json:"last_memory_failure_at,omitempty"`
	LastMemoryCooldownLogAt    time.Time                `json:"last_memory_cooldown_log_at,omitempty"`
	MemoryUpdatedAt            time.Time                `json:"memory_updated_at,omitempty"`
	ConsecutiveMemoryFailures  int                      `json:"consecutive_memory_failures,omitempty"`
	ConsecutiveCompactFailures int                      `json:"consecutive_compact_failures,omitempty"`
	Microcompact               runtimeMicrocompactState `json:"microcompact"`
	UpdatedAt                  time.Time                `json:"updated_at"`
}

type runtimeArtifact struct {
	ID            string    `json:"id"`
	Type          string    `json:"type"`
	ToolName      string    `json:"tool_name,omitempty"`
	Path          string    `json:"path,omitempty"`
	StartLine     int       `json:"start_line,omitempty"`
	EndLine       int       `json:"end_line,omitempty"`
	OriginalBytes int       `json:"original_bytes"`
	Truncated     bool      `json:"truncated"`
	CapturedAt    time.Time `json:"captured_at"`
	Content       string    `json:"content"`
}
