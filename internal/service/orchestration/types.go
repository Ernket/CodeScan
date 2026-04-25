package orchestration

import (
	"time"

	"codescan/internal/model"
)

const (
	runStatusRunning   = "running"
	runStatusPaused    = "paused"
	runStatusCompleted = "completed"
	runStatusFailed    = "failed"
)

const (
	rolePlanner     = "planner"
	roleWorker      = "worker"
	roleIntegrator  = "integrator"
	roleValidator   = "validator"
	rolePersistence = "persistence"
)

const (
	subtaskStatusBlocked   = "blocked"
	subtaskStatusReady     = "ready"
	subtaskStatusRunning   = "running"
	subtaskStatusPaused    = "paused"
	subtaskStatusCompleted = "completed"
	subtaskStatusFailed    = "failed"
)

const (
	roleStatusPending   = "pending"
	roleStatusReady     = "ready"
	roleStatusStarting  = "starting"
	roleStatusRunning   = "running"
	roleStatusPaused    = "paused"
	roleStatusSkipped   = "skipped"
	roleStatusCompleted = "completed"
	roleStatusFailed    = "failed"
)

const (
	eventRunStarted           = "run.started"
	eventRunPaused            = "run.paused"
	eventRunResumed           = "run.resumed"
	eventRunCompleted         = "run.completed"
	eventRunFailed            = "run.failed"
	eventPlannerRevised       = "planner.revised"
	eventAgentStarted         = "agent.started"
	eventAgentCompleted       = "agent.completed"
	eventAgentPaused          = "agent.paused"
	eventAgentFailed          = "agent.failed"
	eventSubtaskUpdated       = "subtask.updated"
	eventRoutesMaterialized   = "routes.materialized"
	eventFindingsMaterialized = "findings.materialized"
)

type StageProgress struct {
	Stage            string `json:"stage"`
	Label            string `json:"label"`
	Status           string `json:"status"`
	SubtaskCount     int    `json:"subtask_count"`
	CompletedCount   int    `json:"completed_count"`
	FailedCount      int    `json:"failed_count"`
	RunningCount     int    `json:"running_count"`
	ProvisionalCount int    `json:"provisional_count"`
	ValidatedCount   int    `json:"validated_count"`
}

type RunSummary struct {
	Run                model.TaskRun   `json:"run"`
	ActiveSubtaskCount int             `json:"active_subtask_count"`
	CompletedCount     int             `json:"completed_count"`
	FailedCount        int             `json:"failed_count"`
	PausedCount        int             `json:"paused_count"`
	PlannerRevision    int             `json:"planner_revision"`
	StageProgress      []StageProgress `json:"stage_progress"`
}

type SnapshotDiagnostics struct {
	FocusStatus        string      `json:"focus_status"`
	FocusReason        string      `json:"focus_reason,omitempty"`
	CurrentStage       string      `json:"current_stage,omitempty"`
	CurrentRole        string      `json:"current_role,omitempty"`
	FocusSubtaskID     string      `json:"focus_subtask_id,omitempty"`
	FocusSubtaskTitle  string      `json:"focus_subtask_title,omitempty"`
	BlockedReason      string      `json:"blocked_reason,omitempty"`
	ErrorMessage       string      `json:"error_message,omitempty"`
	LastProgressAt     *time.Time  `json:"last_progress_at,omitempty"`
	SilenceSeconds     int64       `json:"silence_seconds"`
	LatestEventType    string      `json:"latest_event_type,omitempty"`
	LatestEventMessage string      `json:"latest_event_message,omitempty"`
	LatestEventAt      *time.Time  `json:"latest_event_at,omitempty"`
	PlannerPending     bool        `json:"planner_pending"`
	LastReplanReason   string      `json:"last_replan_reason,omitempty"`
	Stalled            bool        `json:"stalled"`
	Parallelism        Parallelism `json:"parallelism"`
}

type Parallelism struct {
	Planner     int `json:"planner"`
	Worker      int `json:"worker"`
	Integrator  int `json:"integrator"`
	Validator   int `json:"validator"`
	Persistence int `json:"persistence"`
}

type Snapshot struct {
	Run         *RunSummary          `json:"run,omitempty"`
	Diagnostics *SnapshotDiagnostics `json:"diagnostics"`
	Subtasks    []model.TaskSubtask  `json:"subtasks"`
	Agents      []model.TaskAgentRun `json:"agents"`
	Routes      []model.TaskRoute    `json:"routes"`
	Findings    []model.TaskFinding  `json:"findings"`
	Events      []model.TaskEvent    `json:"events"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

type TaskSummary struct {
	ActiveRunID        string          `json:"active_run_id,omitempty"`
	PlannerRevision    int             `json:"planner_revision"`
	ActiveSubtaskCount int             `json:"active_subtask_count"`
	StageProgress      []StageProgress `json:"stage_progress"`
	LastRunStatus      string          `json:"last_run_status,omitempty"`
	LastReplanReason   string          `json:"last_replan_reason,omitempty"`
}

func auditStages() []string {
	return []string{"rce", "injection", "auth", "access", "xss", "config", "fileop", "logic"}
}
