package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codescan/internal/model"
	"codescan/internal/service/orchestration"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestPauseTaskHandlerRejectsNonRunningStates(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, status := range []string{"pending", "completed", "failed", "paused"} {
		status := status
		t.Run(status, func(t *testing.T) {
			restore := setTaskControlTestDeps(t, taskControlTestDeps{
				loadWithStages: func(taskID string) (model.Task, error) {
					return model.Task{ID: taskID, Status: status}, nil
				},
			})
			defer restore()

			w := performTaskControlRequest(http.MethodPost, "/api/tasks/task-1/pause", "task-1", PauseTaskHandler)
			if w.Code != http.StatusConflict {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, w.Code, w.Body.String())
			}
		})
	}
}

func TestPauseTaskHandlerPausesRunningTaskAndStages(t *testing.T) {
	gin.SetMode(gin.TestMode)

	task := model.Task{
		ID:     "task-1",
		Status: "running",
		Stages: []model.TaskStage{
			{ID: 1, TaskID: "task-1", Name: "init", Status: "running"},
			{ID: 2, TaskID: "task-1", Name: "auth", Status: "completed"},
		},
	}

	savedTaskStatus := ""
	savedStageStatuses := map[string]string{}
	markedPaused := false
	restore := setTaskControlTestDeps(t, taskControlTestDeps{
		loadWithStages: func(string) (model.Task, error) {
			return task, nil
		},
		saveTask: func(saved *model.Task) error {
			savedTaskStatus = saved.Status
			return nil
		},
		saveStage: func(stage *model.TaskStage) error {
			savedStageStatuses[stage.Name] = stage.Status
			return nil
		},
		markPaused: func(taskID string) error {
			if taskID != "task-1" {
				t.Fatalf("expected markPaused for task-1, got %s", taskID)
			}
			markedPaused = true
			return nil
		},
	})
	defer restore()

	w := performTaskControlRequest(http.MethodPost, "/api/tasks/task-1/pause", "task-1", PauseTaskHandler)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if savedTaskStatus != "paused" {
		t.Fatalf("expected task status saved as paused, got %s", savedTaskStatus)
	}
	if savedStageStatuses["init"] != "paused" {
		t.Fatalf("expected running init stage to be paused, got %+v", savedStageStatuses)
	}
	if _, ok := savedStageStatuses["auth"]; ok {
		t.Fatalf("expected completed stage not to be re-saved, got %+v", savedStageStatuses)
	}
	if !markedPaused {
		t.Fatalf("expected orchestration pause marker to run")
	}
}

func TestResumeTaskHandlerRejectsNonPausedStates(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, status := range []string{"pending", "running", "completed", "failed"} {
		status := status
		t.Run(status, func(t *testing.T) {
			restore := setTaskControlTestDeps(t, taskControlTestDeps{
				loadTask: func(taskID string) (model.Task, error) {
					return model.Task{ID: taskID, Status: status}, nil
				},
			})
			defer restore()

			w := performTaskControlRequest(http.MethodPost, "/api/tasks/task-1/resume", "task-1", ResumeTaskHandler)
			if w.Code != http.StatusConflict {
				t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, w.Code, w.Body.String())
			}
		})
	}
}

func TestResumeTaskHandlerResumesPausedOrchestrationTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedTaskStatus := ""
	restore := setTaskControlTestDeps(t, taskControlTestDeps{
		loadTask: func(taskID string) (model.Task, error) {
			return model.Task{ID: taskID, Status: "paused"}, nil
		},
		loadSummary: func(taskID string) (*orchestration.TaskSummary, error) {
			return &orchestration.TaskSummary{LastRunStatus: "paused"}, nil
		},
		resumeOrchestration: func(taskID string) (*orchestration.Snapshot, error) {
			return &orchestration.Snapshot{UpdatedAt: time.Unix(10, 0)}, nil
		},
		saveTask: func(task *model.Task) error {
			savedTaskStatus = task.Status
			return nil
		},
	})
	defer restore()

	w := performTaskControlRequest(http.MethodPost, "/api/tasks/task-1/resume", "task-1", ResumeTaskHandler)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if savedTaskStatus != "running" {
		t.Fatalf("expected task status saved as running, got %s", savedTaskStatus)
	}
	if !strings.Contains(w.Body.String(), `"mode":"orchestration"`) {
		t.Fatalf("expected orchestration resume response, got %s", w.Body.String())
	}
}

func TestResumeTaskHandlerResumesPausedLegacyTask(t *testing.T) {
	gin.SetMode(gin.TestMode)

	savedTaskStatus := ""
	restore := setTaskControlTestDeps(t, taskControlTestDeps{
		loadTask: func(taskID string) (model.Task, error) {
			return model.Task{ID: taskID, Status: "paused"}, nil
		},
		loadSummary: func(string) (*orchestration.TaskSummary, error) {
			return nil, nil
		},
		resumeLegacy: func(task *model.Task) (string, error) {
			return "auth", nil
		},
		saveTask: func(task *model.Task) error {
			savedTaskStatus = task.Status
			return nil
		},
	})
	defer restore()

	w := performTaskControlRequest(http.MethodPost, "/api/tasks/task-1/resume", "task-1", ResumeTaskHandler)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}
	if savedTaskStatus != "running" {
		t.Fatalf("expected task status saved as running, got %s", savedTaskStatus)
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload["stage"] != "auth" {
		t.Fatalf("expected resumed stage auth, got %v", payload["stage"])
	}
}

type taskControlTestDeps struct {
	loadWithStages      func(taskID string) (model.Task, error)
	loadTask            func(taskID string) (model.Task, error)
	saveTask            func(task *model.Task) error
	saveStage           func(stage *model.TaskStage) error
	markPaused          func(taskID string) error
	loadSummary         func(taskID string) (*orchestration.TaskSummary, error)
	resumeOrchestration func(taskID string) (*orchestration.Snapshot, error)
	resumeLegacy        func(task *model.Task) (string, error)
}

func setTaskControlTestDeps(t *testing.T, deps taskControlTestDeps) func() {
	t.Helper()

	oldLoadWithStages := loadTaskWithStagesForControl
	oldLoadTask := loadTaskForControl
	oldSaveTask := saveTaskForControl
	oldSaveStage := saveTaskStageForControl
	oldMarkPaused := markTaskOrchestrationPaused
	oldLoadSummary := loadTaskOrchestrationSummary
	oldResumeOrchestration := resumeTaskOrchestration
	oldResumeLegacy := resumeLegacyTaskScan

	loadTaskWithStagesForControl = func(taskID string) (model.Task, error) {
		if deps.loadWithStages != nil {
			return deps.loadWithStages(taskID)
		}
		return model.Task{}, gorm.ErrRecordNotFound
	}
	loadTaskForControl = func(taskID string) (model.Task, error) {
		if deps.loadTask != nil {
			return deps.loadTask(taskID)
		}
		return model.Task{}, gorm.ErrRecordNotFound
	}
	saveTaskForControl = func(task *model.Task) error {
		if deps.saveTask != nil {
			return deps.saveTask(task)
		}
		return nil
	}
	saveTaskStageForControl = func(stage *model.TaskStage) error {
		if deps.saveStage != nil {
			return deps.saveStage(stage)
		}
		return nil
	}
	markTaskOrchestrationPaused = func(taskID string) error {
		if deps.markPaused != nil {
			return deps.markPaused(taskID)
		}
		return nil
	}
	loadTaskOrchestrationSummary = func(taskID string) (*orchestration.TaskSummary, error) {
		if deps.loadSummary != nil {
			return deps.loadSummary(taskID)
		}
		return nil, nil
	}
	resumeTaskOrchestration = func(taskID string) (*orchestration.Snapshot, error) {
		if deps.resumeOrchestration != nil {
			return deps.resumeOrchestration(taskID)
		}
		return &orchestration.Snapshot{}, nil
	}
	resumeLegacyTaskScan = func(task *model.Task) (string, error) {
		if deps.resumeLegacy != nil {
			return deps.resumeLegacy(task)
		}
		return "", nil
	}

	return func() {
		loadTaskWithStagesForControl = oldLoadWithStages
		loadTaskForControl = oldLoadTask
		saveTaskForControl = oldSaveTask
		saveTaskStageForControl = oldSaveStage
		markTaskOrchestrationPaused = oldMarkPaused
		loadTaskOrchestrationSummary = oldLoadSummary
		resumeTaskOrchestration = oldResumeOrchestration
		resumeLegacyTaskScan = oldResumeLegacy
	}
}

func performTaskControlRequest(method, path, taskID string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	c.Params = gin.Params{{Key: "id", Value: taskID}}
	handler(c)
	return w
}
