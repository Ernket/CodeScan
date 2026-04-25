package orchestration

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"codescan/internal/model"
)

var orchestrationOpaqueIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestBuildInitialSubtasksBlocksAuditStagesWithoutRoutes(t *testing.T) {
	task := model.Task{ID: "task-1"}

	subtasks := buildInitialSubtasks(task, "run-1")

	if len(subtasks) != 1+len(auditStages()) {
		t.Fatalf("expected %d subtasks, got %d", 1+len(auditStages()), len(subtasks))
	}

	for _, subtask := range subtasks {
		if subtask.Stage == "init" {
			if subtask.Status != subtaskStatusReady {
				t.Fatalf("expected init subtask ready, got %s", subtask.Status)
			}
			if subtask.WorkerStatus != roleStatusReady {
				t.Fatalf("expected init worker ready, got %s", subtask.WorkerStatus)
			}
			continue
		}

		if subtask.Status != subtaskStatusBlocked {
			t.Fatalf("expected stage %s blocked before routes, got %s", subtask.Stage, subtask.Status)
		}
		if subtask.WorkerStatus != roleStatusPending {
			t.Fatalf("expected stage %s worker pending, got %s", subtask.Stage, subtask.WorkerStatus)
		}
	}
}

func TestBuildInitialSubtasksAssignUniqueOpaqueIDs(t *testing.T) {
	task := model.Task{ID: "task-1"}
	subtasks := buildInitialSubtasks(task, "run-1")

	seen := make(map[string]struct{}, len(subtasks))
	for _, subtask := range subtasks {
		assertOpaqueOrchestrationID(t, subtask.ID)
		if _, exists := seen[subtask.ID]; exists {
			t.Fatalf("duplicate subtask id generated: %s", subtask.ID)
		}
		seen[subtask.ID] = struct{}{}
	}
}

func TestBuildInitialSubtasksReusesExistingRoutes(t *testing.T) {
	task := model.Task{
		ID:         "task-1",
		OutputJSON: json.RawMessage(`[{"method":"GET","path":"/health"}]`),
	}

	subtasks := buildInitialSubtasks(task, "run-1")

	for _, subtask := range subtasks {
		if subtask.Stage == "init" {
			if subtask.Status != subtaskStatusReady {
				t.Fatalf("expected init ready for reuse, got %s", subtask.Status)
			}
			continue
		}
		if subtask.Status != subtaskStatusReady {
			t.Fatalf("expected stage %s ready with precomputed routes, got %s", subtask.Stage, subtask.Status)
		}
		if subtask.WorkerStatus != roleStatusReady {
			t.Fatalf("expected stage %s worker ready, got %s", subtask.Stage, subtask.WorkerStatus)
		}
	}
}

func TestBuildStageProgressCountsByStatus(t *testing.T) {
	now := time.Now()
	progress := buildStageProgress([]model.TaskSubtask{
		{Stage: "init", Status: subtaskStatusCompleted, ProvisionalCount: 12, ValidatedCount: 12},
		{Stage: "rce", Status: subtaskStatusRunning, WorkerStatus: roleStatusRunning, ProvisionalCount: 3},
		{Stage: "auth", Status: subtaskStatusFailed, CompletedAt: &now},
		{Stage: "xss", Status: subtaskStatusReady},
	})

	stage := func(key string) StageProgress {
		for _, item := range progress {
			if item.Stage == key {
				return item
			}
		}
		t.Fatalf("stage %s not found", key)
		return StageProgress{}
	}

	if item := stage("init"); item.CompletedCount != 1 || item.ValidatedCount != 12 {
		t.Fatalf("unexpected init progress: %+v", item)
	}
	if item := stage("rce"); item.RunningCount != 1 || item.ProvisionalCount != 3 {
		t.Fatalf("unexpected rce progress: %+v", item)
	}
	if item := stage("auth"); item.FailedCount != 1 || item.Status != subtaskStatusFailed {
		t.Fatalf("unexpected auth progress: %+v", item)
	}
}

func TestBuildAgentRunAssignsUniqueOpaqueIDs(t *testing.T) {
	manager := NewManager()
	const count = 512

	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		agentRun := manager.buildAgentRun("task-1", "run-1", "subtask-1", roleWorker, "init", true)
		assertOpaqueOrchestrationID(t, agentRun.ID)
		if _, exists := seen[agentRun.ID]; exists {
			t.Fatalf("duplicate agent id generated: %s", agentRun.ID)
		}
		seen[agentRun.ID] = struct{}{}
	}
}

func TestBuildEventAssignsUniqueOpaqueIDs(t *testing.T) {
	const count = 512

	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		event := buildEvent("task-1", "run-1", "subtask-1", "agent-1", eventAgentStarted, "info", "started", map[string]any{"i": i})
		assertOpaqueOrchestrationID(t, event.ID)
		if _, exists := seen[event.ID]; exists {
			t.Fatalf("duplicate event id generated: %s", event.ID)
		}
		seen[event.ID] = struct{}{}
	}
}

func assertOpaqueOrchestrationID(t *testing.T, id string) {
	t.Helper()
	if !orchestrationOpaqueIDPattern.MatchString(id) {
		t.Fatalf("expected opaque id to match %s, got %q", orchestrationOpaqueIDPattern.String(), id)
	}
}
