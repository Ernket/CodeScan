package orchestration

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	summarysvc "codescan/internal/service/summary"
	"codescan/internal/utils"
)

func (m *Manager) RerunSelected(taskID string, selectedStages []string) (*Snapshot, error) {
	if !config.Orchestration.Enabled {
		return nil, fmt.Errorf("orchestration is disabled")
	}

	normalizedStages, err := normalizeStageSelection(selectedStages)
	if err != nil {
		return nil, err
	}
	if len(normalizedStages) == 0 {
		return nil, fmt.Errorf("at least one stage must be selected")
	}

	var task model.Task
	if err := database.DB.First(&task, "id = ?", taskID).Error; err != nil {
		return nil, err
	}

	switch strings.ToLower(strings.TrimSpace(task.Status)) {
	case "failed", "completed":
	case "running":
		return nil, fmt.Errorf("task %s is already running", taskID)
	case "paused":
		return nil, fmt.Errorf("task %s must be resumed instead of rerun", taskID)
	default:
		return nil, fmt.Errorf("task %s must complete or fail before rerun", taskID)
	}

	if run, _ := m.activeRun(taskID); run != nil {
		return nil, fmt.Errorf("task %s already has an active orchestration run", taskID)
	}

	selectedSet := stageSet(normalizedStages)
	hasRoutes := summarysvc.ParseRouteCount(task.OutputJSON, task.Result) > 0
	if _, selectedInit := selectedSet["init"]; !selectedInit {
		for _, stage := range auditStages() {
			if _, ok := selectedSet[stage]; ok && !hasRoutes {
				return nil, fmt.Errorf("route inventory is unavailable; select init to rerun route discovery first")
			}
		}
	}

	completedStages, err := loadCompletedStageMap(task.ID)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	run := model.TaskRun{
		ID:               utils.NewOpaqueID(),
		TaskID:           taskID,
		Status:           runStatusRunning,
		PlannerPending:   true,
		LastReplanReason: replanReasonRerun,
		StartedAt:        now,
	}

	scope := runScope{
		Mode:           runModeRerunSelected,
		SelectedStages: normalizedStages,
	}

	subtasks := make([]model.TaskSubtask, 0, len(orchestrationStageOrder()))
	for _, stage := range orchestrationStageOrder() {
		switch {
		case stage == "init" && scope.selectsStage("init"):
			subtasks = append(subtasks, buildStageSubtask(run.ID, task.ID, stage, false))
		case stage == "init" && hasRoutes:
			scope.CarriedOverStages = append(scope.CarriedOverStages, stage)
			scope.ReusedRouteInventory = true
			subtasks = append(subtasks, buildCarryOverSubtask(task, run.ID, stage, nil))
		case scope.selectsStage(stage):
			subtasks = append(subtasks, buildStageSubtask(run.ID, task.ID, stage, hasRoutes && !scope.selectsStage("init")))
		default:
			stageRecord, ok := completedStages[stage]
			if !ok {
				continue
			}
			scope.CarriedOverStages = append(scope.CarriedOverStages, stage)
			subtasks = append(subtasks, buildCarryOverSubtask(task, run.ID, stage, &stageRecord))
		}
	}
	run.SummaryJSON = marshalRunScope(scope)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if len(subtasks) > 0 {
			if err := tx.Create(&subtasks).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.Task{}).Where("id = ?", taskID).Update("status", "running").Error
	}); err != nil {
		return nil, err
	}

	if err := m.materializeCarryOverProjection(&task, &run, subtasks, scope); err != nil {
		m.failRun(&task, &run, fmt.Sprintf("Carry-over materialization failed: %v", err))
		return nil, err
	}

	if _, err := m.emit(taskID, run.ID, "", "", eventRunStarted, "info", "Orchestration rerun started.", map[string]any{
		"run_id":              run.ID,
		"subtask_ids":         extractSubtaskIDs(subtasks),
		"selected_stages":     scope.SelectedStages,
		"carried_over_stages": scope.CarriedOverStages,
		"mode":                scope.Mode,
	}); err != nil {
		log.Printf("warning: failed to emit orchestration rerun start event: %v", err)
	}

	m.ensureController(run.ID)
	return m.Snapshot(taskID)
}

func (m *Manager) materializeCarryOverProjection(task *model.Task, run *model.TaskRun, subtasks []model.TaskSubtask, scope runScope) error {
	carried := stageSet(scope.CarriedOverStages)
	if len(carried) == 0 {
		return nil
	}

	subtasksByStage := make(map[string]*model.TaskSubtask, len(subtasks))
	for i := range subtasks {
		subtasksByStage[subtasks[i].Stage] = &subtasks[i]
	}

	for _, stage := range scope.CarriedOverStages {
		subtask := subtasksByStage[stage]
		if subtask == nil {
			continue
		}
		if _, ok := carried[stage]; !ok {
			continue
		}

		switch stage {
		case "init":
			if _, err := m.materializeRoutes(task, run, subtask, "", true); err != nil {
				return err
			}
		default:
			if _, err := m.materializeFindings(task, run, subtask, "", false); err != nil {
				return err
			}
		}
	}
	return nil
}
