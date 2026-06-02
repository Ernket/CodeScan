package orchestration

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"codescan/internal/database"
	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
)

const (
	runModeFull          = "full"
	runModeRerunSelected = "rerun_selected"
	replanReasonRerun    = "rerun_selected"
)

type runScope struct {
	Mode                 string   `json:"mode,omitempty"`
	SelectedStages       []string `json:"selected_stages,omitempty"`
	CarriedOverStages    []string `json:"carried_over_stages,omitempty"`
	ReusedRouteInventory bool     `json:"reused_route_inventory,omitempty"`
}

func orchestrationStageOrder() []string {
	return append([]string{"init"}, auditStages()...)
}

func normalizeStageSelection(selected []string) ([]string, error) {
	order := orchestrationStageOrder()
	seen := make(map[string]struct{}, len(order))
	normalized := make([]string, 0, len(selected))

	for _, stage := range selected {
		stage = strings.ToLower(strings.TrimSpace(stage))
		if stage == "" {
			continue
		}
		if !slices.Contains(order, stage) {
			return nil, fmt.Errorf("unsupported stage %q", stage)
		}
		if _, ok := seen[stage]; ok {
			continue
		}
		seen[stage] = struct{}{}
		normalized = append(normalized, stage)
	}

	slices.SortStableFunc(normalized, func(left, right string) int {
		return stagePriority(left) - stagePriority(right)
	})
	return normalized, nil
}

func buildFullRunScope() runScope {
	return runScope{
		Mode:           runModeFull,
		SelectedStages: orchestrationStageOrder(),
	}
}

func normalizeRunScope(scope runScope) runScope {
	if normalized, err := normalizeStageSelection(scope.SelectedStages); err == nil {
		scope.SelectedStages = normalized
	} else {
		scope.SelectedStages = buildFullRunScope().SelectedStages
	}
	if normalized, err := normalizeStageSelection(scope.CarriedOverStages); err == nil {
		scope.CarriedOverStages = normalized
	} else {
		scope.CarriedOverStages = nil
	}
	scope.Mode = strings.TrimSpace(scope.Mode)
	if scope.Mode == "" {
		scope.Mode = runModeFull
	}
	return scope
}

func decodeRunScope(run *model.TaskRun) runScope {
	if run == nil {
		return buildFullRunScope()
	}
	trimmed := strings.TrimSpace(string(run.SummaryJSON))
	if trimmed == "" || trimmed == "null" {
		return buildFullRunScope()
	}

	var scope runScope
	if err := json.Unmarshal(run.SummaryJSON, &scope); err != nil {
		return buildFullRunScope()
	}
	return normalizeRunScope(scope)
}

func marshalRunScope(scope runScope) json.RawMessage {
	return marshalJSON(normalizeRunScope(scope))
}

func stageSet(stages []string) map[string]struct{} {
	set := make(map[string]struct{}, len(stages))
	for _, stage := range stages {
		set[stage] = struct{}{}
	}
	return set
}

func (s runScope) selectedSet() map[string]struct{} {
	return stageSet(s.SelectedStages)
}

func (s runScope) managedStages() []string {
	order := orchestrationStageOrder()
	selected := s.selectedSet()
	carried := stageSet(s.CarriedOverStages)
	managed := make([]string, 0, len(order))
	for _, stage := range order {
		if _, ok := selected[stage]; ok {
			managed = append(managed, stage)
			continue
		}
		if _, ok := carried[stage]; ok {
			managed = append(managed, stage)
		}
	}
	return managed
}

func (s runScope) managesStage(stage string) bool {
	for _, managed := range s.managedStages() {
		if managed == stage {
			return true
		}
	}
	return false
}

func (s runScope) selectsStage(stage string) bool {
	_, ok := s.selectedSet()[stage]
	return ok
}

func (s runScope) selectedAuditStages() []string {
	selected := s.selectedSet()
	stages := make([]string, 0, len(auditStages()))
	for _, stage := range auditStages() {
		if _, ok := selected[stage]; ok {
			stages = append(stages, stage)
		}
	}
	return stages
}

func buildCarryOverSubtask(task model.Task, runID string, stage string, stageRecord *model.TaskStage) model.TaskSubtask {
	now := time.Now()
	subtask := model.TaskSubtask{
		ID:                 hashKey("carry_over", task.ID, runID, stage),
		TaskID:             task.ID,
		RunID:              runID,
		Stage:              stage,
		Title:              stageLabel(stage),
		Priority:           stagePriority(stage),
		Status:             subtaskStatusCompleted,
		WorkerStatus:       roleStatusCompleted,
		IntegratorStatus:   roleStatusCompleted,
		ValidatorStatus:    roleStatusCompleted,
		PersistenceStatus:  roleStatusCompleted,
		VerificationStatus: effectiveSubtaskVerification(stage, false),
		CreatedAt:          now,
		UpdatedAt:          now,
		StartedAt:          &now,
		CompletedAt:        &now,
	}

	if stage == "init" {
		count := summarysvc.ParseRouteCount(task.OutputJSON, task.Result)
		subtask.ProvisionalCount = count
		subtask.ValidatedCount = count
		subtask.ValidatorStatus = roleStatusSkipped
		subtask.VerificationStatus = "confirmed"
		return subtask
	}

	if stageRecord != nil {
		startedAt := stageRecord.CreatedAt
		completedAt := stageRecord.UpdatedAt
		if !startedAt.IsZero() {
			subtask.StartedAt = &startedAt
		}
		if !completedAt.IsZero() {
			subtask.CompletedAt = &completedAt
		}
		if findings, _, ok := summarysvc.ParseFindings(stageRecord.OutputJSON, stageRecord.Result); ok {
			subtask.ProvisionalCount = len(findings)
			subtask.ValidatedCount = len(findings)
		}
	}

	return subtask
}

func loadCompletedStageMap(taskID string) (map[string]model.TaskStage, error) {
	var stages []model.TaskStage
	if err := database.DB.Where("task_id = ? AND status = ?", taskID, "completed").Find(&stages).Error; err != nil {
		return nil, err
	}

	stageMap := make(map[string]model.TaskStage, len(stages))
	for _, stage := range stages {
		stageMap[stage.Name] = stage
	}
	return stageMap, nil
}

func taskHasOutstandingFailedStages(taskID string) (bool, error) {
	var count int64
	if err := database.DB.Model(&model.TaskStage{}).
		Where("task_id = ? AND status = ?", taskID, "failed").
		Where("name IN ?", auditStages()).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}
