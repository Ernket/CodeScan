package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"
)

func TestBuildSnapshotDiagnosticsRunning(t *testing.T) {
	restore := setDiagnosticsTestOrchestrationConfig(t, config.OrchestrationConfig{
		Planner:     config.ParallelRoleConfig{Parallelism: 1},
		Worker:      config.ModelRoleConfig{Parallelism: 4},
		Integrator:  config.ParallelRoleConfig{Parallelism: 2},
		Validator:   config.ModelRoleConfig{Parallelism: 2},
		Persistence: config.ParallelRoleConfig{Parallelism: 1},
	})
	defer restore()

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusRunning,
		PlannerPending: true,
		StartedAt:      now.Add(-2 * time.Minute),
		CreatedAt:      now.Add(-2 * time.Minute),
		UpdatedAt:      now.Add(-30 * time.Second),
	}
	subtasks := []model.TaskSubtask{
		testSubtask("rce", 10, subtaskStatusRunning, roleStatusRunning, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-45*time.Second)),
	}
	events := []model.TaskEvent{
		{Sequence: 10, EventType: eventAgentStarted, Message: "worker started for rce.", CreatedAt: now.Add(-20 * time.Second)},
	}

	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, events, now)
	if diagnostics == nil {
		t.Fatal("expected diagnostics")
	}
	if diagnostics.FocusStatus != "running" {
		t.Fatalf("expected running focus, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.FocusReason != "subtask_running" {
		t.Fatalf("expected running focus reason, got %q", diagnostics.FocusReason)
	}
	if diagnostics.CurrentStage != "rce" {
		t.Fatalf("expected current stage rce, got %q", diagnostics.CurrentStage)
	}
	if diagnostics.CurrentRole != roleWorker {
		t.Fatalf("expected current role worker, got %q", diagnostics.CurrentRole)
	}
	if !diagnostics.PlannerPending {
		t.Fatal("expected planner pending to be true")
	}
	if diagnostics.Parallelism.Worker != 4 || diagnostics.Parallelism.Validator != 2 {
		t.Fatalf("expected parallelism snapshot from config, got %+v", diagnostics.Parallelism)
	}
	if diagnostics.LatestEventType != eventAgentStarted {
		t.Fatalf("expected latest event type %q, got %q", eventAgentStarted, diagnostics.LatestEventType)
	}
	if diagnostics.Stalled {
		t.Fatal("did not expect stalled running diagnostics")
	}
}

func TestBuildSnapshotDiagnosticsBlockedBeatsRunning(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusRunning,
		PlannerPending: false,
		StartedAt:      now.Add(-5 * time.Minute),
		CreatedAt:      now.Add(-5 * time.Minute),
		UpdatedAt:      now.Add(-20 * time.Second),
	}
	subtasks := []model.TaskSubtask{
		testSubtask("injection", 20, subtaskStatusBlocked, roleStatusPending, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-35*time.Second)),
		testSubtask("xss", 50, subtaskStatusRunning, roleStatusRunning, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-10*time.Second)),
	}
	subtasks[0].BlockedReason = "awaiting upstream route verification"

	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, nil, now)
	if diagnostics.FocusStatus != "blocked" {
		t.Fatalf("expected blocked focus, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.CurrentStage != "injection" {
		t.Fatalf("expected blocked stage to be focused, got %q", diagnostics.CurrentStage)
	}
	if diagnostics.CurrentRole != roleWorker {
		t.Fatalf("expected worker role for blocked stage, got %q", diagnostics.CurrentRole)
	}
	if diagnostics.BlockedReason != "awaiting upstream route verification" {
		t.Fatalf("unexpected blocked reason %q", diagnostics.BlockedReason)
	}
}

func TestBuildSnapshotDiagnosticsStalledAfterThreeMinutesWithoutProgress(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	lastProgress := now.Add(-181 * time.Second)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusRunning,
		PlannerPending: false,
		StartedAt:      now.Add(-10 * time.Minute),
		CreatedAt:      now.Add(-10 * time.Minute),
		UpdatedAt:      lastProgress,
	}
	subtasks := []model.TaskSubtask{
		testSubtask("auth", 30, subtaskStatusRunning, roleStatusRunning, roleStatusPending, roleStatusPending, roleStatusPending, lastProgress),
	}
	agents := []model.TaskAgentRun{
		{ID: "agent-1", SubtaskID: subtasks[0].ID, Role: roleWorker, Status: roleStatusRunning, UpdatedAt: lastProgress, CreatedAt: lastProgress},
	}
	events := []model.TaskEvent{
		{Sequence: 99, EventType: eventAgentStarted, Message: "worker started for auth.", CreatedAt: lastProgress},
	}

	diagnostics := buildSnapshotDiagnostics(run, subtasks, agents, events, now)
	if !diagnostics.Stalled {
		t.Fatal("expected stalled diagnostics")
	}
	if diagnostics.FocusStatus != "stalled" {
		t.Fatalf("expected stalled focus status, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.CurrentStage != "auth" {
		t.Fatalf("expected auth to be marked as stalled stage, got %q", diagnostics.CurrentStage)
	}
	if diagnostics.SilenceSeconds < 181 {
		t.Fatalf("expected silence seconds >= 181, got %d", diagnostics.SilenceSeconds)
	}
}

func TestBuildSnapshotDiagnosticsFailed(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(-30 * time.Second)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusFailed,
		PlannerPending: false,
		ErrorMessage:   "validator pipeline failed",
		StartedAt:      now.Add(-5 * time.Minute),
		CreatedAt:      now.Add(-5 * time.Minute),
		UpdatedAt:      completedAt,
		CompletedAt:    &completedAt,
	}
	subtasks := []model.TaskSubtask{
		testSubtask("access", 40, subtaskStatusFailed, roleStatusCompleted, roleStatusCompleted, roleStatusFailed, roleStatusPending, completedAt),
	}
	subtasks[0].ErrorMessage = "validator could not reconcile findings"

	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, nil, now)
	if diagnostics.FocusStatus != "failed" {
		t.Fatalf("expected failed focus status, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.CurrentRole != roleValidator {
		t.Fatalf("expected validator role, got %q", diagnostics.CurrentRole)
	}
	if diagnostics.ErrorMessage != "validator could not reconcile findings" {
		t.Fatalf("unexpected error message %q", diagnostics.ErrorMessage)
	}
}

func TestBuildSnapshotDiagnosticsPaused(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	pausedAt := now.Add(-40 * time.Second)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusPaused,
		PlannerPending: false,
		StartedAt:      now.Add(-7 * time.Minute),
		CreatedAt:      now.Add(-7 * time.Minute),
		UpdatedAt:      pausedAt,
		PausedAt:       &pausedAt,
	}
	subtasks := []model.TaskSubtask{
		testSubtask("xss", 50, subtaskStatusPaused, roleStatusPaused, roleStatusPending, roleStatusPending, roleStatusPending, pausedAt),
	}

	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, nil, now)
	if diagnostics.FocusStatus != "paused" {
		t.Fatalf("expected paused focus status, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.CurrentStage != "xss" {
		t.Fatalf("expected xss to be focused, got %q", diagnostics.CurrentStage)
	}
	if diagnostics.CurrentRole != roleWorker {
		t.Fatalf("expected worker paused role, got %q", diagnostics.CurrentRole)
	}
}

func TestBuildSnapshotDiagnosticsCompleted(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	completedAt := now.Add(-1 * time.Minute)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusCompleted,
		PlannerPending: false,
		StartedAt:      now.Add(-12 * time.Minute),
		CreatedAt:      now.Add(-12 * time.Minute),
		UpdatedAt:      completedAt,
		CompletedAt:    &completedAt,
	}
	subtasks := []model.TaskSubtask{
		testCompletedSubtask("init", 0, completedAt),
		testCompletedSubtask("logic", 80, completedAt),
	}

	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, nil, now)
	if diagnostics.FocusStatus != "completed" {
		t.Fatalf("expected completed focus status, got %q", diagnostics.FocusStatus)
	}
	if diagnostics.CurrentStage != "" {
		t.Fatalf("expected no current stage for completed run, got %q", diagnostics.CurrentStage)
	}
}

func TestBuildSnapshotDiagnosticsUsesLatestPlannerReasonFromEvent(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusRunning,
		PlannerPending: false,
		StartedAt:      now.Add(-5 * time.Minute),
		CreatedAt:      now.Add(-5 * time.Minute),
		UpdatedAt:      now.Add(-10 * time.Second),
	}
	events := []model.TaskEvent{
		{
			Sequence:    11,
			EventType:   eventPlannerRevised,
			Message:     "Planner revision applied.",
			PayloadJSON: json.RawMessage(`{"reason":"integrator_completed","created":["auth"]}`),
			CreatedAt:   now.Add(-15 * time.Second),
		},
	}

	diagnostics := buildSnapshotDiagnostics(run, nil, nil, events, now)
	if diagnostics.LastReplanReason != "integrator_completed" {
		t.Fatalf("expected latest replan reason from event, got %q", diagnostics.LastReplanReason)
	}
}

func TestSnapshotJSONIncludesDiagnosticsWithoutBreakingExistingFields(t *testing.T) {
	restore := setDiagnosticsTestOrchestrationConfig(t, config.OrchestrationConfig{
		Planner:     config.ParallelRoleConfig{Parallelism: 3},
		Worker:      config.ModelRoleConfig{Parallelism: 7},
		Integrator:  config.ParallelRoleConfig{Parallelism: 5},
		Validator:   config.ModelRoleConfig{Parallelism: 4},
		Persistence: config.ParallelRoleConfig{Parallelism: 2},
	})
	defer restore()

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	run := &model.TaskRun{
		ID:             "run-1",
		Status:         runStatusRunning,
		PlannerPending: false,
		StartedAt:      now.Add(-2 * time.Minute),
		CreatedAt:      now.Add(-2 * time.Minute),
		UpdatedAt:      now.Add(-5 * time.Second),
	}
	subtasks := []model.TaskSubtask{
		testSubtask("rce", 10, subtaskStatusRunning, roleStatusRunning, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-5*time.Second)),
	}
	diagnostics := buildSnapshotDiagnostics(run, subtasks, nil, nil, now)
	snapshot := Snapshot{
		Run: &RunSummary{
			Run:           *run,
			StageProgress: buildStageProgress(subtasks),
		},
		Diagnostics: diagnostics,
		Subtasks:    subtasks,
		Agents:      []model.TaskAgentRun{},
		Routes:      []model.TaskRoute{},
		Findings:    []model.TaskFinding{},
		Events:      []model.TaskEvent{},
		UpdatedAt:   now,
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	body := string(encoded)
	for _, token := range []string{
		`"diagnostics":`,
		`"parallelism":{"planner":3,"worker":7,"integrator":5,"validator":4,"persistence":2}`,
		`"subtasks":`,
		`"agents":`,
		`"routes":`,
		`"findings":`,
		`"events":`,
		`"updated_at":`,
	} {
		if !strings.Contains(body, token) {
			t.Fatalf("expected serialized snapshot to contain %s, got %s", token, body)
		}
	}
}

func setDiagnosticsTestOrchestrationConfig(t *testing.T, cfg config.OrchestrationConfig) func() {
	t.Helper()

	previous := config.Orchestration
	config.Orchestration = cfg

	return func() {
		config.Orchestration = previous
	}
}

func testSubtask(stage string, priority int, status, worker, integrator, validator, persistence string, updatedAt time.Time) model.TaskSubtask {
	startedAt := updatedAt.Add(-1 * time.Minute)
	return model.TaskSubtask{
		ID:                stage + "-1",
		Stage:             stage,
		Title:             stageLabel(stage),
		Priority:          priority,
		Status:            status,
		WorkerStatus:      worker,
		IntegratorStatus:  integrator,
		ValidatorStatus:   validator,
		PersistenceStatus: persistence,
		StartedAt:         &startedAt,
		CreatedAt:         startedAt,
		UpdatedAt:         updatedAt,
	}
}

func testCompletedSubtask(stage string, priority int, completedAt time.Time) model.TaskSubtask {
	subtask := testSubtask(stage, priority, subtaskStatusCompleted, roleStatusCompleted, roleStatusCompleted, roleStatusCompleted, roleStatusCompleted, completedAt)
	subtask.CompletedAt = &completedAt
	return subtask
}
