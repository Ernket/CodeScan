package scanner

import (
	"log"

	"codescan/internal/database"
	"codescan/internal/model"
)

func saveTaskRecord(task *model.Task) bool {
	if task == nil {
		return false
	}

	tx := database.DB.Select("*").Save(task)
	if tx.Error != nil {
		log.Printf("failed to save task %s: %v", task.ID, tx.Error)
		return false
	}

	return tx.RowsAffected > 0
}

func saveTaskProjection(task *model.Task) bool {
	if task == nil {
		return false
	}

	var current model.Task
	if err := database.DB.First(&current, "id = ?", task.ID).Error; err != nil {
		log.Printf("failed to load task %s before saving projection: %v", task.ID, err)
		return false
	}

	current.Result = task.Result
	current.OutputJSON = task.OutputJSON
	current.Logs = task.Logs

	tx := database.DB.Select("*").Save(&current)
	if tx.Error != nil {
		log.Printf("failed to save task projection %s: %v", task.ID, tx.Error)
		return false
	}

	return tx.RowsAffected > 0
}

func saveTaskStageRecord(stage *model.TaskStage) bool {
	if stage == nil {
		return false
	}

	tx := database.DB.Select("*").Save(stage)
	if tx.Error != nil {
		log.Printf("failed to save task stage %d for task %s: %v", stage.ID, stage.TaskID, tx.Error)
		return false
	}

	return tx.RowsAffected > 0
}

func updateTaskStatus(task *model.Task, status string) bool {
	if task == nil {
		return false
	}

	task.Status = status
	tx := database.DB.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", status)
	if tx.Error != nil {
		log.Printf("failed to update task %s status to %s: %v", task.ID, status, tx.Error)
		return false
	}

	return tx.RowsAffected > 0
}
