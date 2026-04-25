package model

import (
	"encoding/json"
	"time"
)

type TaskRun struct {
	ID               string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	TaskID           string          `json:"task_id" gorm:"type:varchar(64);index"`
	Status           string          `json:"status" gorm:"type:varchar(32);index"`
	PlannerRevision  int             `json:"planner_revision"`
	PlannerPending   bool            `json:"planner_pending"`
	LastReplanReason string          `json:"last_replan_reason" gorm:"type:varchar(128)"`
	SummaryJSON      json.RawMessage `json:"summary_json" gorm:"type:json"`
	ErrorMessage     string          `json:"error_message" gorm:"type:longtext"`
	StartedAt        time.Time       `json:"started_at"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	PausedAt         *time.Time      `json:"paused_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type TaskSubtask struct {
	ID                 string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	TaskID             string          `json:"task_id" gorm:"type:varchar(64);index"`
	RunID              string          `json:"run_id" gorm:"type:varchar(64);index"`
	Stage              string          `json:"stage" gorm:"type:varchar(32);index"`
	Title              string          `json:"title" gorm:"type:varchar(255)"`
	Status             string          `json:"status" gorm:"type:varchar(32);index"`
	Priority           int             `json:"priority"`
	PlanRevision       int             `json:"plan_revision"`
	WorkerStatus       string          `json:"worker_status" gorm:"type:varchar(32)"`
	IntegratorStatus   string          `json:"integrator_status" gorm:"type:varchar(32)"`
	ValidatorStatus    string          `json:"validator_status" gorm:"type:varchar(32)"`
	PersistenceStatus  string          `json:"persistence_status" gorm:"type:varchar(32)"`
	BlockedReason      string          `json:"blocked_reason" gorm:"type:varchar(255)"`
	ErrorMessage       string          `json:"error_message" gorm:"type:longtext"`
	ProvisionalCount   int             `json:"provisional_count"`
	ValidatedCount     int             `json:"validated_count"`
	VerificationStatus string          `json:"verification_status" gorm:"type:varchar(32)"`
	PayloadJSON        json.RawMessage `json:"payload_json" gorm:"type:json"`
	StartedAt          *time.Time      `json:"started_at,omitempty"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type TaskAgentRun struct {
	ID            string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	TaskID        string          `json:"task_id" gorm:"type:varchar(64);index"`
	RunID         string          `json:"run_id" gorm:"type:varchar(64);index"`
	SubtaskID     string          `json:"subtask_id" gorm:"type:varchar(64);index"`
	Role          string          `json:"role" gorm:"type:varchar(32);index"`
	Stage         string          `json:"stage" gorm:"type:varchar(32);index"`
	Status        string          `json:"status" gorm:"type:varchar(32);index"`
	Model         string          `json:"model" gorm:"type:varchar(128)"`
	Temperature   float64         `json:"temperature"`
	MaxIterations int             `json:"max_iterations"`
	ResumeCount   int             `json:"resume_count"`
	SessionPath   string          `json:"session_path" gorm:"type:longtext"`
	InputJSON     json.RawMessage `json:"input_json" gorm:"type:json"`
	OutputJSON    json.RawMessage `json:"output_json" gorm:"type:json"`
	ErrorMessage  string          `json:"error_message" gorm:"type:longtext"`
	StartedAt     *time.Time      `json:"started_at,omitempty"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type TaskEvent struct {
	Sequence    uint64          `json:"sequence" gorm:"primaryKey;autoIncrement"`
	ID          string          `json:"id" gorm:"type:varchar(64);uniqueIndex"`
	TaskID      string          `json:"task_id" gorm:"type:varchar(64);index"`
	RunID       string          `json:"run_id" gorm:"type:varchar(64);index"`
	SubtaskID   string          `json:"subtask_id" gorm:"type:varchar(64);index"`
	AgentRunID  string          `json:"agent_run_id" gorm:"type:varchar(64);index"`
	EventType   string          `json:"event_type" gorm:"type:varchar(64);index"`
	Level       string          `json:"level" gorm:"type:varchar(16)"`
	Message     string          `json:"message" gorm:"type:longtext"`
	PayloadJSON json.RawMessage `json:"payload_json" gorm:"type:json"`
	CreatedAt   time.Time       `json:"created_at"`
}

type TaskRoute struct {
	ID                 string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	TaskID             string          `json:"task_id" gorm:"type:varchar(64);index"`
	RunID              string          `json:"run_id" gorm:"type:varchar(64);index"`
	SubtaskID          string          `json:"subtask_id" gorm:"type:varchar(64);index"`
	PlanRevision       int             `json:"plan_revision"`
	AgentRunID         string          `json:"agent_run_id" gorm:"type:varchar(64);index"`
	OriginStage        string          `json:"origin_stage" gorm:"type:varchar(32);index"`
	Method             string          `json:"method" gorm:"type:varchar(16)"`
	Path               string          `json:"path" gorm:"type:longtext"`
	Handler            string          `json:"handler" gorm:"type:varchar(255)"`
	SourceFile         string          `json:"source_file" gorm:"type:longtext"`
	VerificationStatus string          `json:"verification_status" gorm:"type:varchar(32)"`
	ReviewedSeverity   string          `json:"reviewed_severity" gorm:"type:varchar(32)"`
	VerificationReason string          `json:"verification_reason" gorm:"type:longtext"`
	EvidenceRefs       json.RawMessage `json:"evidence_refs" gorm:"type:json"`
	PayloadJSON        json.RawMessage `json:"payload_json" gorm:"type:json"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type TaskFinding struct {
	ID                 string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	TaskID             string          `json:"task_id" gorm:"type:varchar(64);index"`
	RunID              string          `json:"run_id" gorm:"type:varchar(64);index"`
	SubtaskID          string          `json:"subtask_id" gorm:"type:varchar(64);index"`
	PlanRevision       int             `json:"plan_revision"`
	AgentRunID         string          `json:"agent_run_id" gorm:"type:varchar(64);index"`
	OriginStage        string          `json:"origin_stage" gorm:"type:varchar(32);index"`
	Type               string          `json:"type" gorm:"type:varchar(64);index"`
	Subtype            string          `json:"subtype" gorm:"type:varchar(128)"`
	Severity           string          `json:"severity" gorm:"type:varchar(32)"`
	VerificationStatus string          `json:"verification_status" gorm:"type:varchar(32);index"`
	ReviewedSeverity   string          `json:"reviewed_severity" gorm:"type:varchar(32)"`
	VerificationReason string          `json:"verification_reason" gorm:"type:longtext"`
	EvidenceRefs       json.RawMessage `json:"evidence_refs" gorm:"type:json"`
	PayloadJSON        json.RawMessage `json:"payload_json" gorm:"type:json"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}
