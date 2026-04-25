package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"
)

func TestAutoStartUploadedTaskStartsOrchestrationWhenEnabled(t *testing.T) {
	task := &model.Task{ID: "task-1", Logs: []string{}}

	legacyCalled := false
	restore := setUploadStartTestDeps(t, uploadStartTestDeps{
		orchestrationEnabled: true,
		startOrchestration: func(taskID string) error {
			if taskID != "task-1" {
				t.Fatalf("expected orchestration start for task-1, got %s", taskID)
			}
			return nil
		},
		startLegacy: func(*model.Task) {
			legacyCalled = true
		},
		loadStatus: func(string) (string, error) {
			t.Fatalf("loadStatus should not be called on successful orchestration start")
			return "", nil
		},
		saveTask: func(*model.Task) error {
			t.Fatalf("saveTask should not be called on successful orchestration start")
			return nil
		},
		updateStatus: func(string, string) error {
			t.Fatalf("updateStatus should not be called when orchestration is enabled")
			return nil
		},
		now: func() time.Time {
			return time.Unix(0, 0)
		},
	})
	defer restore()

	if err := autoStartUploadedTask(task); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if task.Status != "running" {
		t.Fatalf("expected task status running, got %s", task.Status)
	}
	if legacyCalled {
		t.Fatalf("expected legacy scan not to start when orchestration is enabled")
	}
}

func TestAutoStartUploadedTaskFallsBackToLegacyInitWhenOrchestrationDisabled(t *testing.T) {
	task := &model.Task{ID: "task-2", Logs: []string{}}

	legacyCalled := false
	var updatedTaskID string
	var updatedStatus string

	restore := setUploadStartTestDeps(t, uploadStartTestDeps{
		orchestrationEnabled: false,
		startOrchestration: func(string) error {
			t.Fatalf("orchestration start should not be called when disabled")
			return nil
		},
		startLegacy: func(started *model.Task) {
			legacyCalled = true
			if started != task {
				t.Fatalf("expected legacy starter to receive the same task pointer")
			}
		},
		loadStatus: func(string) (string, error) {
			t.Fatalf("loadStatus should not be called when orchestration is disabled")
			return "", nil
		},
		saveTask: func(*model.Task) error {
			t.Fatalf("saveTask should not be called for disabled fallback success path")
			return nil
		},
		updateStatus: func(taskID, status string) error {
			updatedTaskID = taskID
			updatedStatus = status
			return nil
		},
		now: func() time.Time {
			return time.Unix(0, 0)
		},
	})
	defer restore()

	if err := autoStartUploadedTask(task); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if task.Status != "running" {
		t.Fatalf("expected task status running, got %s", task.Status)
	}
	if !legacyCalled {
		t.Fatalf("expected legacy init scan to start when orchestration is disabled")
	}
	if updatedTaskID != "task-2" || updatedStatus != "running" {
		t.Fatalf("expected persisted status update to running for task-2, got %s %s", updatedTaskID, updatedStatus)
	}
}

func TestAutoStartUploadedTaskTreatsRunningStatusAsSuccessfulStart(t *testing.T) {
	task := &model.Task{ID: "task-3", Logs: []string{}}

	restore := setUploadStartTestDeps(t, uploadStartTestDeps{
		orchestrationEnabled: true,
		startOrchestration: func(string) error {
			return errors.New("snapshot failed after run creation")
		},
		startLegacy: func(*model.Task) {
			t.Fatalf("legacy scan should not be used when orchestration already started")
		},
		loadStatus: func(taskID string) (string, error) {
			if taskID != "task-3" {
				t.Fatalf("expected status check for task-3, got %s", taskID)
			}
			return "running", nil
		},
		saveTask: func(*model.Task) error {
			t.Fatalf("saveTask should not be called when the task is already running")
			return nil
		},
		updateStatus: func(string, string) error {
			t.Fatalf("updateStatus should not be called in orchestration mode")
			return nil
		},
		now: func() time.Time {
			return time.Unix(0, 0)
		},
	})
	defer restore()

	if err := autoStartUploadedTask(task); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if task.Status != "running" {
		t.Fatalf("expected task status running, got %s", task.Status)
	}
	if task.Result != "" {
		t.Fatalf("expected task result to remain empty, got %q", task.Result)
	}
}

func TestAutoStartUploadedTaskMarksTaskFailedWhenStartFails(t *testing.T) {
	task := &model.Task{ID: "task-4", Logs: []string{}}

	persisted := false
	now := time.Date(2026, 4, 21, 10, 11, 12, 0, time.UTC)
	restore := setUploadStartTestDeps(t, uploadStartTestDeps{
		orchestrationEnabled: true,
		startOrchestration: func(string) error {
			return errors.New("planner boot failed")
		},
		startLegacy: func(*model.Task) {
			t.Fatalf("legacy scan should not be used when orchestration start fails")
		},
		loadStatus: func(string) (string, error) {
			return "pending", nil
		},
		saveTask: func(saved *model.Task) error {
			persisted = true
			if saved.Status != "failed" {
				t.Fatalf("expected persisted status failed, got %s", saved.Status)
			}
			if !strings.Contains(saved.Result, "Automatic orchestration failed to start: planner boot failed") {
				t.Fatalf("expected failure result to describe orchestration start error, got %q", saved.Result)
			}
			if len(saved.Logs) != 1 {
				t.Fatalf("expected one failure log, got %d", len(saved.Logs))
			}
			if !strings.Contains(saved.Logs[0], "[10:11:12] Automatic orchestration failed to start: planner boot failed") {
				t.Fatalf("unexpected failure log entry: %q", saved.Logs[0])
			}
			return nil
		},
		updateStatus: func(string, string) error {
			t.Fatalf("updateStatus should not be called when orchestration is enabled")
			return nil
		},
		now: func() time.Time {
			return now
		},
	})
	defer restore()

	if err := autoStartUploadedTask(task); err != nil {
		t.Fatalf("expected no error after persisting failed state, got %v", err)
	}
	if !persisted {
		t.Fatalf("expected failed task state to be persisted")
	}
	if task.Status != "failed" {
		t.Fatalf("expected task status failed, got %s", task.Status)
	}
	if !strings.Contains(task.Result, "Automatic orchestration failed to start: planner boot failed") {
		t.Fatalf("expected task result to describe orchestration start error, got %q", task.Result)
	}
	if len(task.Logs) != 1 {
		t.Fatalf("expected one failure log on task, got %d", len(task.Logs))
	}
}

type uploadStartTestDeps struct {
	orchestrationEnabled bool
	startOrchestration   func(taskID string) error
	startLegacy          func(task *model.Task)
	loadStatus           func(taskID string) (string, error)
	saveTask             func(task *model.Task) error
	updateStatus         func(taskID, status string) error
	now                  func() time.Time
}

func setUploadStartTestDeps(t *testing.T, deps uploadStartTestDeps) func() {
	t.Helper()

	oldCfg := config.Orchestration
	oldStartOrchestration := launchTaskOrchestration
	oldStartLegacy := launchLegacyInitScan
	oldLoadStatus := loadTaskStatus
	oldPersistTask := persistTask
	oldPersistTaskStatus := persistTaskStatus
	oldTaskLogClock := taskLogClock

	config.Orchestration = config.OrchestrationConfig{Enabled: deps.orchestrationEnabled}
	launchTaskOrchestration = deps.startOrchestration
	launchLegacyInitScan = deps.startLegacy
	loadTaskStatus = deps.loadStatus
	persistTask = deps.saveTask
	persistTaskStatus = deps.updateStatus
	taskLogClock = deps.now

	return func() {
		config.Orchestration = oldCfg
		launchTaskOrchestration = oldStartOrchestration
		launchLegacyInitScan = oldStartLegacy
		loadTaskStatus = oldLoadStatus
		persistTask = oldPersistTask
		persistTaskStatus = oldPersistTaskStatus
		taskLogClock = oldTaskLogClock
	}
}
