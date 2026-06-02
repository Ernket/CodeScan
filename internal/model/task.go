package model

import (
	"encoding/json"
	"path/filepath"
	"time"

	"codescan/internal/config"
)

type Task struct {
	ID             string          `json:"id" gorm:"type:varchar(64);primaryKey"`
	Name           string          `json:"name"`
	Remark         string          `json:"remark"`
	Status         string          `json:"status"` // pending, running, completed, failed, paused
	OrganizationID *uint           `json:"organization_id,omitempty" gorm:"index"`
	Organization   *Organization   `json:"organization,omitempty" gorm:"foreignKey:OrganizationID"`
	Permissions    TaskPermissions `json:"permissions" gorm:"-"`
	CreatedAt      time.Time       `json:"created_at"`
	BasePath       string          `json:"-" gorm:"-"` // Runtime only, not persisted
	Result         string          `json:"result" gorm:"type:longtext"`
	OutputJSON     json.RawMessage `json:"output_json" gorm:"type:longtext"`          // Structured output
	Logs           []string        `json:"logs" gorm:"type:longtext;serializer:json"` // Activity logs
	Stages         []TaskStage     `json:"stages" gorm:"foreignKey:TaskID"`
}

type TaskPermissions struct {
	CanRead  bool `json:"can_read"`
	CanWrite bool `json:"can_write"`
}

type TaskStageMeta struct {
	LastRunKind    string     `json:"last_run_kind,omitempty"`
	GapCheckedAt   *time.Time `json:"gap_checked_at,omitempty"`
	RevalidatedAt  *time.Time `json:"revalidated_at,omitempty"`
	ReviewSummary  string     `json:"review_summary,omitempty"`
	RejectedCount  int        `json:"rejected_count,omitempty"`
	ConfirmedCount int        `json:"confirmed_count,omitempty"`
	UncertainCount int        `json:"uncertain_count,omitempty"`
}

// GetBasePath returns the project directory path for this task.
func (t *Task) GetBasePath() string {
	if t.BasePath != "" {
		return t.BasePath
	}
	return filepath.Join(config.ProjectsDir, t.ID)
}

func (t *Task) RuntimeRootPath() string {
	return filepath.Join(t.GetBasePath(), ".codescan", "runtime")
}

func (t *Task) ProjectManifestPath() string {
	return filepath.Join(t.RuntimeRootPath(), "project_manifest.json")
}

func (t *Task) StageRuntimePath(stage string) string {
	return filepath.Join(t.RuntimeRootPath(), stage)
}

type TaskStage struct {
	ID         uint            `json:"id" gorm:"primaryKey"`
	TaskID     string          `json:"task_id" gorm:"type:varchar(64);index"`
	Name       string          `json:"name"`   // e.g., "Scan", "Audit", "Fix"
	Status     string          `json:"status"` // pending, running, completed, failed
	Result     string          `json:"result" gorm:"type:longtext"`
	OutputJSON json.RawMessage `json:"output_json" gorm:"type:longtext"`          // Structured output for frontend
	Logs       []string        `json:"logs" gorm:"type:longtext;serializer:json"` // Stage specific logs
	Meta       TaskStageMeta   `json:"meta" gorm:"type:longtext;serializer:json"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}
