package scanner

import (
	"errors"
	"fmt"
	"os"

	"codescan/internal/database"
	"codescan/internal/model"

	"gorm.io/gorm"
)

func RunAIScan(task *model.Task, stage string) {
	runAIScan(task, stage, StageRunInitial, false)
}

func RunGapCheck(task *model.Task, stage string) {
	runAIScan(task, stage, StageRunGapCheck, false)
}

func RunRevalidate(task *model.Task, stage string) {
	runAIScan(task, stage, StageRunRevalidate, false)
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
	go runAIScan(task, stage, kind, true)
	return stage, nil
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
