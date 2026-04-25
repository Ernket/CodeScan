package scanner

import (
	"errors"
	"fmt"
	"os"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"

	"gorm.io/gorm"
)

func RunAIScan(task *model.Task, stage string) {
	runAIScanAsync(task, stage, StageRunInitial, false, legacyScanExecutionOptions())
}

func RunGapCheck(task *model.Task, stage string) {
	runAIScanAsync(task, stage, StageRunGapCheck, false, legacyScanExecutionOptions())
}

func RunRevalidate(task *model.Task, stage string) {
	runAIScanAsync(task, stage, StageRunRevalidate, false, legacyScanExecutionOptions())
}

func ResumeAIScan(task *model.Task) (string, error) {
	task.BasePath = task.GetBasePath()
	stage, err := selectResumableRuntimeStage(task)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("no resumable runtime state found for task %s; re-run the stage to continue", task.ID)
		}
		return "", err
	}
	kind := StageRunInitial
	if stage != "init" {
		var current model.TaskStage
		if err := database.DB.Select("meta").Where("task_id = ? AND name = ?", task.ID, stage).First(&current).Error; err == nil {
			kind = stageRunKindFromMeta(current)
		}
	}
	runAIScanAsync(task, stage, kind, true, legacyScanExecutionOptions())
	return stage, nil
}

func ExecuteAIScan(task *model.Task, stage string, kind StageRunKind, resume bool, options ScanExecutionOptions) {
	runAIScan(task, stage, kind, resume, options)
}

func OrchestratedExecutionOptions(stage string, kind StageRunKind) ScanExecutionOptions {
	if stage == "init" {
		return orchestratedInitExecutionOptions()
	}
	options := orchestratedStageExecutionOptions()
	switch kind {
	case StageRunInitial:
		options.Profile.Model = config.Orchestration.Worker.Model
	case StageRunRevalidate:
		options.Profile.Model = config.Orchestration.Validator.Model
	}
	return options
}

func runAIScanAsync(task *model.Task, stage string, kind StageRunKind, resume bool, options ScanExecutionOptions) {
	go runAIScan(task, stage, kind, resume, options)
}

func pauseRequested(taskID, stage string) bool {
	var task model.Task
	if err := database.DB.Select("status").First(&task, "id = ?", taskID).Error; err == nil {
		if task.Status == "paused" {
			return true
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		return true
	}

	if stage != "init" {
		var current model.TaskStage
		if err := database.DB.Select("status").Where("task_id = ? AND name = ?", taskID, stage).First(&current).Error; err == nil {
			if current.Status == "paused" {
				return true
			}
		}
	}

	return false
}
