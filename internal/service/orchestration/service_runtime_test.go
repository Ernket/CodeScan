package orchestration

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/scanner"
)

func TestDriveRunRetriesLoadRunContextBeforeDispatch(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()
	restoreTimings := setControllerTimings(t, 5*time.Millisecond, 3, 5*time.Millisecond)
	defer restoreTimings()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-retry", runStatusRunning)
	subtask := buildStageSubtask(run.ID, task.ID, "init", true)
	mustCreateRecords(t, &task, &run, &subtask)

	loadCalls := 0
	startCalls := 0
	manager.loadRunContextFn = func(runID string) (*model.TaskRun, *model.Task, []model.TaskSubtask, error) {
		loadCalls++
		if loadCalls <= 2 {
			return &run, &task, nil, errors.New("temporary load failure")
		}
		return manager.loadRunContext(runID)
	}
	manager.startAgentRunFn = func(taskID, runID, subtaskID, role string) agentRunStartResult {
		startCalls++
		if err := database.DB.Model(&model.TaskRun{}).Where("id = ?", runID).Update("status", runStatusCompleted).Error; err != nil {
			return agentRunStartResult{Err: err}
		}
		return agentRunStartResult{}
	}

	manager.driveRun(run.ID)

	if loadCalls < 3 {
		t.Fatalf("expected driveRun to retry loadRunContext, got %d calls", loadCalls)
	}
	if startCalls != 1 {
		t.Fatalf("expected driveRun to reach dispatch after retries, got %d start attempts", startCalls)
	}
}

func TestDriveRunFailsRunAfterRepeatedLoadRunContextErrors(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()
	restoreTimings := setControllerTimings(t, 5*time.Millisecond, 3, 5*time.Millisecond)
	defer restoreTimings()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-fail", runStatusRunning)
	mustCreateRecords(t, &task, &run)

	manager.loadRunContextFn = func(runID string) (*model.TaskRun, *model.Task, []model.TaskSubtask, error) {
		return &run, &task, nil, errors.New("context store unavailable")
	}

	manager.driveRun(run.ID)

	var storedRun model.TaskRun
	if err := database.DB.First(&storedRun, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load failed run: %v", err)
	}
	if storedRun.Status != runStatusFailed {
		t.Fatalf("expected run failed after repeated load errors, got %s", storedRun.Status)
	}
	if !strings.Contains(storedRun.ErrorMessage, "controller lost run context after repeated retries") {
		t.Fatalf("expected repeated retry failure message, got %q", storedRun.ErrorMessage)
	}
	if !strings.Contains(storedRun.ErrorMessage, "context store unavailable") {
		t.Fatalf("expected original error summary in failure message, got %q", storedRun.ErrorMessage)
	}

	var storedTask model.Task
	if err := database.DB.First(&storedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load failed task: %v", err)
	}
	if storedTask.Status != "failed" {
		t.Fatalf("expected task failed after repeated load errors, got %s", storedTask.Status)
	}

	var events []model.TaskEvent
	if err := database.DB.Where("run_id = ?", run.ID).Order("sequence asc").Find(&events).Error; err != nil {
		t.Fatalf("load task events: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].EventType != eventRunFailed {
		t.Fatalf("expected final event %q, got %+v", eventRunFailed, events)
	}
}

func TestSnapshotRestartsMissingControllerForRunningRun(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-snapshot", runStatusRunning)
	mustCreateRecords(t, &task, &run)

	started := make(chan string, 2)
	release := make(chan struct{})
	manager.controllerRunner = func(runID string) {
		started <- runID
		<-release
	}

	snapshot, err := manager.Snapshot(task.ID)
	if err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if snapshot.Run == nil || snapshot.Run.Run.ID != run.ID {
		t.Fatalf("expected snapshot for run %s, got %+v", run.ID, snapshot.Run)
	}

	startedRunID := waitForRunID(t, started)
	if startedRunID != run.ID {
		t.Fatalf("expected restarted controller for %s, got %s", run.ID, startedRunID)
	}
	assertNoAdditionalControllerStart(t, started)
	close(release)
}

func TestCompletedRunDoesNotRestartController(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-completed", runStatusCompleted)
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	task.Status = "completed"
	mustCreateRecords(t, &task, &run)

	started := make(chan string, 1)
	manager.controllerRunner = func(runID string) {
		started <- runID
	}

	if _, err := manager.Snapshot(task.ID); err != nil {
		t.Fatalf("snapshot failed: %v", err)
	}
	if _, err := manager.ListEvents(task.ID, 0, 20); err != nil {
		t.Fatalf("list events failed: %v", err)
	}

	select {
	case runID := <-started:
		t.Fatalf("did not expect controller restart for completed run, got %s", runID)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestDispatchRoleMarksFailedWhenStartAgentRunErrors(t *testing.T) {
	testCases := []struct {
		name               string
		role               string
		stage              string
		expectedReason     string
		expectedStatus     string
		expectedWorker     string
		expectedIntegrator string
	}{
		{
			name:               "worker",
			role:               roleWorker,
			stage:              "rce",
			expectedReason:     "worker_failed",
			expectedStatus:     subtaskStatusFailed,
			expectedWorker:     roleStatusFailed,
			expectedIntegrator: roleStatusPending,
		},
		{
			name:               "integrator",
			role:               roleIntegrator,
			stage:              "rce",
			expectedReason:     "integrator_failed",
			expectedStatus:     subtaskStatusFailed,
			expectedWorker:     roleStatusCompleted,
			expectedIntegrator: roleStatusFailed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setupOrchestrationServiceTestDB(t)
			restoreConfig := setServiceTestConfig(t)
			defer restoreConfig()

			manager := NewManager()
			task, run := createTestTaskAndRun(t, "task-dispatch-"+tc.name, runStatusRunning)
			subtask := buildStageSubtask(run.ID, task.ID, tc.stage, true)
			if tc.role == roleIntegrator {
				subtask.WorkerStatus = roleStatusCompleted
				subtask.IntegratorStatus = roleStatusReady
				subtask.Status = subtaskStatusReady
			}
			mustCreateRecords(t, &task, &run, &subtask)

			manager.startAgentRunFn = func(taskID, runID, subtaskID, role string) agentRunStartResult {
				return agentRunStartResult{Err: errors.New("transaction create failed")}
			}

			launched := manager.dispatchRole(&task, &run, []model.TaskSubtask{subtask}, tc.role)
			if launched {
				t.Fatalf("expected %s dispatch not to launch on start error", tc.role)
			}

			var storedSubtask model.TaskSubtask
			if err := database.DB.First(&storedSubtask, "id = ?", subtask.ID).Error; err != nil {
				t.Fatalf("load subtask: %v", err)
			}
			if storedSubtask.Status != tc.expectedStatus {
				t.Fatalf("expected subtask status %s, got %s", tc.expectedStatus, storedSubtask.Status)
			}
			if storedSubtask.WorkerStatus != tc.expectedWorker {
				t.Fatalf("expected worker status %s, got %s", tc.expectedWorker, storedSubtask.WorkerStatus)
			}
			if storedSubtask.IntegratorStatus != tc.expectedIntegrator {
				t.Fatalf("expected integrator status %s, got %s", tc.expectedIntegrator, storedSubtask.IntegratorStatus)
			}
			if !strings.Contains(storedSubtask.ErrorMessage, "failed to start") {
				t.Fatalf("expected start failure message, got %q", storedSubtask.ErrorMessage)
			}

			var storedRun model.TaskRun
			if err := database.DB.First(&storedRun, "id = ?", run.ID).Error; err != nil {
				t.Fatalf("load run: %v", err)
			}
			if !storedRun.PlannerPending {
				t.Fatal("expected planner to be requested after role start failure")
			}
			if storedRun.LastReplanReason != tc.expectedReason {
				t.Fatalf("expected replan reason %s, got %s", tc.expectedReason, storedRun.LastReplanReason)
			}

			var event model.TaskEvent
			if err := database.DB.Where("run_id = ? AND event_type = ?", run.ID, eventAgentFailed).Order("sequence desc").First(&event).Error; err != nil {
				t.Fatalf("load agent.failed event: %v", err)
			}
			if !strings.Contains(event.Message, tc.role+" failed") {
				t.Fatalf("expected agent.failed message for %s, got %q", tc.role, event.Message)
			}
		})
	}
}

func TestDispatchRoleSkipsCompetitionWithoutFailing(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-skip", runStatusRunning)
	subtask := buildStageSubtask(run.ID, task.ID, "rce", true)
	mustCreateRecords(t, &task, &run, &subtask)

	manager.startAgentRunFn = func(taskID, runID, subtaskID, role string) agentRunStartResult {
		return agentRunStartResult{}
	}

	launched := manager.dispatchRole(&task, &run, []model.TaskSubtask{subtask}, roleWorker)
	if launched {
		t.Fatal("expected skipped start not to count as launched")
	}

	var storedSubtask model.TaskSubtask
	if err := database.DB.First(&storedSubtask, "id = ?", subtask.ID).Error; err != nil {
		t.Fatalf("load subtask: %v", err)
	}
	if storedSubtask.Status != subtaskStatusReady {
		t.Fatalf("expected subtask to remain ready, got %s", storedSubtask.Status)
	}
	if storedSubtask.WorkerStatus != roleStatusReady {
		t.Fatalf("expected worker to remain ready, got %s", storedSubtask.WorkerStatus)
	}

	var eventCount int64
	if err := database.DB.Model(&model.TaskEvent{}).Where("run_id = ? AND event_type = ?", run.ID, eventAgentFailed).Count(&eventCount).Error; err != nil {
		t.Fatalf("count agent.failed events: %v", err)
	}
	if eventCount != 0 {
		t.Fatalf("expected no agent.failed event on skipped start, got %d", eventCount)
	}
}

func TestAuditChainPreservesPlannerReplanTriggers(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-chain", runStatusRunning)
	run.PlannerRevision = 1
	findings := marshalJSON([]map[string]any{
		{
			"type":                "sql_injection",
			"subtype":             "blind",
			"description":         "unsanitized query reaches sink",
			"severity":            "HIGH",
			"verification_status": "confirmed",
			"verification_reason": "reproduced with crafted payload",
			"reviewed_severity":   "HIGH",
			"trigger": map[string]any{
				"method": "GET",
				"path":   "/users",
			},
			"location": map[string]any{
				"file": "handler.go",
				"line": "12",
			},
		},
	})
	stage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "rce",
		Status:     "completed",
		OutputJSON: findings,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	subtask := buildStageSubtask(run.ID, task.ID, "rce", true)
	subtask.Status = subtaskStatusRunning
	subtask.WorkerStatus = roleStatusRunning
	mustCreateRecords(t, &task, &run, &stage, &subtask)

	workerAgent := mustCreateRunningAgentRun(t, manager, task.ID, run.ID, subtask.ID, roleWorker, subtask.Stage)
	if err := manager.completeScannerAgent(&run, &task, &subtask, workerAgent.ID, scanner.StageRunInitial); err != nil {
		t.Fatalf("complete worker: %v", err)
	}

	currentSubtask := loadSubtaskForTest(t, subtask.ID)
	if currentSubtask.IntegratorStatus != roleStatusReady {
		t.Fatalf("expected integrator ready after worker completion, got %s", currentSubtask.IntegratorStatus)
	}
	if err := database.DB.Model(&model.TaskSubtask{}).Where("id = ?", currentSubtask.ID).Updates(map[string]any{
		"status":            subtaskStatusRunning,
		"integrator_status": roleStatusRunning,
		"updated_at":        time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("set integrator running: %v", err)
	}

	integratorAgent := mustCreateRunningAgentRun(t, manager, task.ID, run.ID, subtask.ID, roleIntegrator, subtask.Stage)
	manager.executeIntegrator(run.ID, subtask.ID, integratorAgent.ID)

	currentSubtask = loadSubtaskForTest(t, subtask.ID)
	if currentSubtask.ValidatorStatus != roleStatusReady {
		t.Fatalf("expected validator ready after integrator completion, got %s", currentSubtask.ValidatorStatus)
	}
	if currentSubtask.ProvisionalCount != 1 {
		t.Fatalf("expected 1 provisional finding, got %d", currentSubtask.ProvisionalCount)
	}
	currentRun := loadRunForTest(t, run.ID)
	if currentRun.LastReplanReason != "integrator_completed" {
		t.Fatalf("expected integrator_completed replan reason, got %s", currentRun.LastReplanReason)
	}

	var provisionalFinding model.TaskFinding
	if err := database.DB.Where("subtask_id = ?", subtask.ID).First(&provisionalFinding).Error; err != nil {
		t.Fatalf("load provisional finding: %v", err)
	}
	if provisionalFinding.VerificationStatus != "unverified" {
		t.Fatalf("expected provisional finding to be unverified, got %s", provisionalFinding.VerificationStatus)
	}

	currentSubtask.Status = subtaskStatusRunning
	currentSubtask.ValidatorStatus = roleStatusRunning
	if err := database.DB.Model(&model.TaskSubtask{}).Where("id = ?", currentSubtask.ID).Updates(map[string]any{
		"status":           currentSubtask.Status,
		"validator_status": currentSubtask.ValidatorStatus,
		"updated_at":       time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("set validator running: %v", err)
	}

	validatorAgent := mustCreateRunningAgentRun(t, manager, task.ID, run.ID, subtask.ID, roleValidator, subtask.Stage)
	if err := manager.completeScannerAgent(&run, &task, &currentSubtask, validatorAgent.ID, scanner.StageRunRevalidate); err != nil {
		t.Fatalf("complete validator: %v", err)
	}

	currentSubtask = loadSubtaskForTest(t, subtask.ID)
	if currentSubtask.PersistenceStatus != roleStatusReady {
		t.Fatalf("expected persistence ready after validator completion, got %s", currentSubtask.PersistenceStatus)
	}

	if err := database.DB.Model(&model.TaskSubtask{}).Where("id = ?", currentSubtask.ID).Updates(map[string]any{
		"status":             subtaskStatusRunning,
		"persistence_status": roleStatusRunning,
		"updated_at":         time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("set persistence running: %v", err)
	}

	persistenceAgent := mustCreateRunningAgentRun(t, manager, task.ID, run.ID, subtask.ID, rolePersistence, subtask.Stage)
	manager.executePersistence(run.ID, subtask.ID, persistenceAgent.ID)

	currentSubtask = loadSubtaskForTest(t, subtask.ID)
	if currentSubtask.Status != subtaskStatusCompleted {
		t.Fatalf("expected completed subtask after persistence, got %s", currentSubtask.Status)
	}
	if currentSubtask.PersistenceStatus != roleStatusCompleted {
		t.Fatalf("expected completed persistence status, got %s", currentSubtask.PersistenceStatus)
	}
	if currentSubtask.ValidatedCount != 1 {
		t.Fatalf("expected 1 validated finding, got %d", currentSubtask.ValidatedCount)
	}
	if currentSubtask.VerificationStatus != "reviewed" {
		t.Fatalf("expected reviewed subtask verification status, got %s", currentSubtask.VerificationStatus)
	}

	currentRun = loadRunForTest(t, run.ID)
	if currentRun.LastReplanReason != "persistence_completed" {
		t.Fatalf("expected persistence_completed replan reason, got %s", currentRun.LastReplanReason)
	}

	var finalFinding model.TaskFinding
	if err := database.DB.Where("subtask_id = ?", subtask.ID).First(&finalFinding).Error; err != nil {
		t.Fatalf("load final finding: %v", err)
	}
	if finalFinding.VerificationStatus != "confirmed" {
		t.Fatalf("expected final finding to be confirmed, got %s", finalFinding.VerificationStatus)
	}
}

func TestPrepareRoleExecutionTransitionsStartingToRunningAfterBootstrapRetry(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()
	restoreBootstrap := setBootstrapTestTiming(t, 5*time.Millisecond, 30*time.Second, 2, 2)
	defer restoreBootstrap()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-bootstrap-success", runStatusRunning)
	subtask := buildStageSubtask(run.ID, task.ID, "rce", true)
	subtask.WorkerStatus = roleStatusCompleted
	subtask.IntegratorStatus = roleStatusReady
	mustCreateRecords(t, &task, &run, &subtask)

	started := manager.startAgentRun(task.ID, run.ID, subtask.ID, roleIntegrator)
	if !started.Scheduled {
		t.Fatal("expected integrator attempt to be scheduled")
	}

	preflightCalls := 0
	ctx, ok := manager.prepareRoleExecution(run.ID, subtask.ID, started.AgentRun.ID, roleIntegrator, false, func(*roleExecutionContext) error {
		preflightCalls++
		if preflightCalls == 1 {
			return errors.New("bootstrap failed once")
		}
		return nil
	})
	if !ok || ctx == nil {
		t.Fatal("expected bootstrap retry to succeed on the second try")
	}
	if preflightCalls != 2 {
		t.Fatalf("expected 2 bootstrap attempts, got %d", preflightCalls)
	}

	storedSubtask := loadSubtaskForTest(t, subtask.ID)
	if storedSubtask.Status != subtaskStatusRunning {
		t.Fatalf("expected running subtask after bootstrap confirmation, got %s", storedSubtask.Status)
	}
	if storedSubtask.IntegratorStatus != roleStatusRunning {
		t.Fatalf("expected running integrator after bootstrap confirmation, got %s", storedSubtask.IntegratorStatus)
	}
	if storedSubtask.StartedAt == nil {
		t.Fatal("expected started_at to be set after bootstrap confirmation")
	}

	var agent model.TaskAgentRun
	if err := database.DB.First(&agent, "id = ?", started.AgentRun.ID).Error; err != nil {
		t.Fatalf("load agent: %v", err)
	}
	if agent.Status != roleStatusRunning {
		t.Fatalf("expected running agent after bootstrap confirmation, got %s", agent.Status)
	}
	if agent.StartedAt == nil {
		t.Fatal("expected agent started_at after bootstrap confirmation")
	}

	var startedCount int64
	if err := database.DB.Model(&model.TaskEvent{}).Where("run_id = ? AND event_type = ?", run.ID, eventAgentStarted).Count(&startedCount).Error; err != nil {
		t.Fatalf("count agent.started events: %v", err)
	}
	if startedCount != 1 {
		t.Fatalf("expected exactly one agent.started event, got %d", startedCount)
	}
}

func TestPrepareRoleExecutionReschedulesOnceThenFailsSubtask(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()
	restoreBootstrap := setBootstrapTestTiming(t, 5*time.Millisecond, 30*time.Second, 2, 2)
	defer restoreBootstrap()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-bootstrap-fail", runStatusRunning)
	subtask := buildStageSubtask(run.ID, task.ID, "rce", true)
	subtask.WorkerStatus = roleStatusCompleted
	subtask.IntegratorStatus = roleStatusReady
	mustCreateRecords(t, &task, &run, &subtask)

	firstAttempt := manager.startAgentRun(task.ID, run.ID, subtask.ID, roleIntegrator)
	if !firstAttempt.Scheduled {
		t.Fatal("expected first integrator attempt to be scheduled")
	}
	if ctx, ok := manager.prepareRoleExecution(run.ID, subtask.ID, firstAttempt.AgentRun.ID, roleIntegrator, false, func(*roleExecutionContext) error {
		return errors.New("bootstrap unavailable")
	}); ok || ctx != nil {
		t.Fatal("expected first bootstrap attempt to fail and reschedule")
	}

	storedSubtask := loadSubtaskForTest(t, subtask.ID)
	if storedSubtask.Status != subtaskStatusReady {
		t.Fatalf("expected ready subtask after first bootstrap failure, got %s", storedSubtask.Status)
	}
	if storedSubtask.IntegratorStatus != roleStatusReady {
		t.Fatalf("expected ready integrator after first bootstrap failure, got %s", storedSubtask.IntegratorStatus)
	}

	secondAttempt := manager.startAgentRun(task.ID, run.ID, subtask.ID, roleIntegrator)
	if !secondAttempt.Scheduled {
		t.Fatal("expected second integrator attempt to be scheduled")
	}
	if ctx, ok := manager.prepareRoleExecution(run.ID, subtask.ID, secondAttempt.AgentRun.ID, roleIntegrator, false, func(*roleExecutionContext) error {
		return errors.New("bootstrap still unavailable")
	}); ok || ctx != nil {
		t.Fatal("expected second bootstrap attempt to fail terminally")
	}

	storedSubtask = loadSubtaskForTest(t, subtask.ID)
	if storedSubtask.Status != subtaskStatusFailed {
		t.Fatalf("expected failed subtask after bootstrap budget exhaustion, got %s", storedSubtask.Status)
	}
	if storedSubtask.IntegratorStatus != roleStatusFailed {
		t.Fatalf("expected failed integrator after bootstrap budget exhaustion, got %s", storedSubtask.IntegratorStatus)
	}

	var failedCount int64
	if err := database.DB.Model(&model.TaskEvent{}).Where("run_id = ? AND event_type = ?", run.ID, eventAgentFailed).Count(&failedCount).Error; err != nil {
		t.Fatalf("count agent.failed events: %v", err)
	}
	if failedCount != 2 {
		t.Fatalf("expected two agent.failed events across two attempts, got %d", failedCount)
	}

	storedRun := loadRunForTest(t, run.ID)
	if !storedRun.PlannerPending {
		t.Fatal("expected planner to be requested after terminal bootstrap failure")
	}
	if storedRun.LastReplanReason != "integrator_failed" {
		t.Fatalf("expected integrator_failed replan reason, got %s", storedRun.LastReplanReason)
	}
}

func TestStartingRoleCountsAgainstParallelism(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	config.Orchestration.Worker.Parallelism = 1

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-parallelism", runStatusRunning)
	first := buildStageSubtask(run.ID, task.ID, "rce", true)
	second := buildStageSubtask(run.ID, task.ID, "auth", true)
	mustCreateRecords(t, &task, &run, &first, &second)

	started := manager.startAgentRun(task.ID, run.ID, first.ID, roleWorker)
	if !started.Scheduled {
		t.Fatal("expected first worker attempt to be scheduled")
	}

	subtasks, err := manager.loadSubtasks(run.ID)
	if err != nil {
		t.Fatalf("load subtasks: %v", err)
	}
	if launched := manager.dispatchRole(&task, &run, subtasks, roleWorker); launched {
		t.Fatal("expected starting worker to occupy the only worker slot")
	}

	var agentCount int64
	if err := database.DB.Model(&model.TaskAgentRun{}).Where("run_id = ? AND role = ?", run.ID, roleWorker).Count(&agentCount).Error; err != nil {
		t.Fatalf("count worker attempts: %v", err)
	}
	if agentCount != 1 {
		t.Fatalf("expected only one worker attempt while first is starting, got %d", agentCount)
	}
}

func TestRepairLegacyRunningOrphanRequiresScannerAnchor(t *testing.T) {
	testCases := []struct {
		name         string
		withStageRow bool
		expectRepair bool
	}{
		{name: "without_anchor", withStageRow: false, expectRepair: true},
		{name: "with_task_stage_anchor", withStageRow: true, expectRepair: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setupOrchestrationServiceTestDB(t)
			restoreConfig := setServiceTestConfig(t)
			defer restoreConfig()

			manager := NewManager()
			task, run := createTestTaskAndRun(t, "task-orphan-"+tc.name, runStatusRunning)
			subtask := buildStageSubtask(run.ID, task.ID, "logic", true)
			subtask.Status = subtaskStatusRunning
			subtask.WorkerStatus = roleStatusRunning
			now := time.Now().UTC().Add(-1 * time.Minute)
			agent := manager.buildAgentRun(task.ID, run.ID, subtask.ID, roleWorker, subtask.Stage, true)
			agent.Status = roleStatusRunning
			agent.CreatedAt = now
			agent.UpdatedAt = now

			records := []any{&task, &run, &subtask, &agent}
			if tc.withStageRow {
				stage := model.TaskStage{
					TaskID:     task.ID,
					Name:       subtask.Stage,
					Status:     "running",
					CreatedAt:  now,
					UpdatedAt:  now,
					OutputJSON: marshalJSON(map[string]any{}),
				}
				records = append(records, &stage)
			}
			mustCreateRecords(t, records...)

			repaired, err := manager.repairOrphanAttempts(&task, &run, []model.TaskSubtask{subtask}, time.Now().UTC())
			if err != nil {
				t.Fatalf("repair orphan attempts: %v", err)
			}
			if tc.expectRepair && repaired != 1 {
				t.Fatalf("expected exactly one repaired orphan, got %d", repaired)
			}
			if !tc.expectRepair && repaired != 0 {
				t.Fatalf("expected no orphan repair, got %d", repaired)
			}

			storedSubtask := loadSubtaskForTest(t, subtask.ID)
			expectedStatus := subtaskStatusRunning
			expectedWorker := roleStatusRunning
			if tc.expectRepair {
				expectedStatus = subtaskStatusReady
				expectedWorker = roleStatusReady
			}
			if storedSubtask.Status != expectedStatus {
				t.Fatalf("expected subtask status %s, got %s", expectedStatus, storedSubtask.Status)
			}
			if storedSubtask.WorkerStatus != expectedWorker {
				t.Fatalf("expected worker status %s, got %s", expectedWorker, storedSubtask.WorkerStatus)
			}
		})
	}
}

func TestRecoverActiveRunsRepairsOrphansAndRestartsController(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-recover", runStatusRunning)
	subtask := buildStageSubtask(run.ID, task.ID, "rce", true)
	subtask.WorkerStatus = roleStatusStarting
	now := time.Now().UTC().Add(-45 * time.Second)
	agent := manager.buildAgentRun(task.ID, run.ID, subtask.ID, roleWorker, subtask.Stage, true)
	agent.Status = roleStatusStarting
	agent.CreatedAt = now
	agent.UpdatedAt = now
	mustCreateRecords(t, &task, &run, &subtask, &agent)

	started := make(chan string, 1)
	manager.controllerRunner = func(runID string) {
		started <- runID
	}

	if err := manager.RecoverActiveRuns(); err != nil {
		t.Fatalf("recover active runs: %v", err)
	}

	storedSubtask := loadSubtaskForTest(t, subtask.ID)
	if storedSubtask.WorkerStatus != roleStatusReady {
		t.Fatalf("expected orphaned starting worker to be reset to ready, got %s", storedSubtask.WorkerStatus)
	}

	if recoveredRunID := waitForRunID(t, started); recoveredRunID != run.ID {
		t.Fatalf("expected controller restart for %s, got %s", run.ID, recoveredRunID)
	}
}

func TestDriveRunRepairsLegacyOrphansAndConvergesRunToFailed(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()
	restoreTimings := setControllerTimings(t, 5*time.Millisecond, 3, 5*time.Millisecond)
	defer restoreTimings()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-legacy-converge", runStatusRunning)
	now := time.Now().UTC().Add(-1 * time.Minute)

	access := buildStageSubtask(run.ID, task.ID, "access", true)
	access.Status = subtaskStatusFailed
	access.WorkerStatus = roleStatusCompleted
	access.IntegratorStatus = roleStatusCompleted
	access.ValidatorStatus = roleStatusFailed
	access.CompletedAt = &now
	access.ErrorMessage = "access validation failed"

	logic := buildStageSubtask(run.ID, task.ID, "logic", true)
	logic.Status = subtaskStatusRunning
	logic.WorkerStatus = roleStatusRunning

	injection := buildStageSubtask(run.ID, task.ID, "injection", true)
	injection.Status = subtaskStatusRunning
	injection.WorkerStatus = roleStatusCompleted
	injection.IntegratorStatus = roleStatusCompleted
	injection.ValidatorStatus = roleStatusCompleted
	injection.PersistenceStatus = roleStatusRunning

	configSubtask := buildStageSubtask(run.ID, task.ID, "config", true)
	configSubtask.Status = subtaskStatusReady
	configSubtask.WorkerStatus = roleStatusCompleted
	configSubtask.IntegratorStatus = roleStatusCompleted
	configSubtask.ValidatorStatus = roleStatusCompleted
	configSubtask.PersistenceStatus = roleStatusReady

	logicAgent := manager.buildAgentRun(task.ID, run.ID, logic.ID, roleWorker, logic.Stage, true)
	logicAgent.Status = roleStatusRunning
	logicAgent.CreatedAt = now
	logicAgent.UpdatedAt = now
	injectionAgent := manager.buildAgentRun(task.ID, run.ID, injection.ID, rolePersistence, injection.Stage, false)
	injectionAgent.Status = roleStatusRunning
	injectionAgent.CreatedAt = now
	injectionAgent.UpdatedAt = now

	mustCreateRecords(t, &task, &run, &access, &logic, &injection, &configSubtask, &logicAgent, &injectionAgent)

	calls := map[string]int{}
	manager.startAgentRunFn = func(taskID, runID, subtaskID, role string) agentRunStartResult {
		calls[subtaskID+":"+role]++
		terminalAt := time.Now().UTC()
		switch {
		case subtaskID == logic.ID && role == roleWorker:
			_ = database.DB.Model(&model.TaskSubtask{}).Where("id = ?", subtaskID).Updates(map[string]any{
				"status":        subtaskStatusFailed,
				"worker_status": roleStatusFailed,
				"error_message": "logic retry failed",
				"completed_at":  &terminalAt,
				"updated_at":    terminalAt,
			}).Error
		case subtaskID == injection.ID && role == rolePersistence:
			_ = database.DB.Model(&model.TaskSubtask{}).Where("id = ?", subtaskID).Updates(map[string]any{
				"status":              subtaskStatusFailed,
				"persistence_status":  roleStatusFailed,
				"error_message":       "injection persistence retry failed",
				"completed_at":        &terminalAt,
				"updated_at":          terminalAt,
			}).Error
		case subtaskID == configSubtask.ID && role == rolePersistence:
			_ = database.DB.Model(&model.TaskSubtask{}).Where("id = ?", subtaskID).Updates(map[string]any{
				"status":             subtaskStatusCompleted,
				"persistence_status": roleStatusCompleted,
				"validated_count":    0,
				"completed_at":       &terminalAt,
				"updated_at":         terminalAt,
			}).Error
		}
		return agentRunStartResult{}
	}

	manager.driveRun(run.ID)

	storedRun := loadRunForTest(t, run.ID)
	if storedRun.Status != runStatusFailed {
		t.Fatalf("expected repaired run to converge to failed, got %s", storedRun.Status)
	}
	if calls[logic.ID+":"+roleWorker] == 0 {
		t.Fatal("expected repaired logic worker to be rescheduled")
	}
	if calls[injection.ID+":"+rolePersistence] == 0 {
		t.Fatal("expected repaired injection persistence to be rescheduled")
	}
	if calls[configSubtask.ID+":"+rolePersistence] == 0 {
		t.Fatal("expected config persistence to be released after orphan repair")
	}

	var storedLogicAgent model.TaskAgentRun
	if err := database.DB.First(&storedLogicAgent, "id = ?", logicAgent.ID).Error; err != nil {
		t.Fatalf("load logic agent: %v", err)
	}
	if storedLogicAgent.Status != roleStatusFailed {
		t.Fatalf("expected legacy logic agent to be marked failed during orphan repair, got %s", storedLogicAgent.Status)
	}

	var storedInjectionAgent model.TaskAgentRun
	if err := database.DB.First(&storedInjectionAgent, "id = ?", injectionAgent.ID).Error; err != nil {
		t.Fatalf("load injection agent: %v", err)
	}
	if storedInjectionAgent.Status != roleStatusFailed {
		t.Fatalf("expected legacy injection agent to be marked failed during orphan repair, got %s", storedInjectionAgent.Status)
	}
}

func setupOrchestrationServiceTestDB(t *testing.T) {
	t.Helper()

	previousDB := database.DB
	dbPath := filepath.Join(t.TempDir(), "orchestration-test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("unwrap sqlite database: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&model.Task{},
		&model.TaskStage{},
		&model.TaskRun{},
		&model.TaskSubtask{},
		&model.TaskAgentRun{},
		&model.TaskEvent{},
		&model.TaskRoute{},
		&model.TaskFinding{},
	); err != nil {
		t.Fatalf("auto-migrate sqlite schema: %v", err)
	}

	database.DB = db
	t.Cleanup(func() {
		database.DB = previousDB
		_ = sqlDB.Close()
	})
}

func setServiceTestConfig(t *testing.T) func() {
	t.Helper()

	previousAI := config.AI
	previousOrchestration := config.Orchestration

	cfg := config.DefaultOrchestrationConfig()
	cfg.Enabled = true
	cfg.Worker.Model = "worker-test-model"
	cfg.Validator.Model = "validator-test-model"
	config.AI = config.AIConfig{Model: "planner-test-model"}
	config.Orchestration = cfg

	return func() {
		config.AI = previousAI
		config.Orchestration = previousOrchestration
	}
}

func setControllerTimings(t *testing.T, retryInterval time.Duration, retryLimit int, loopInterval time.Duration) func() {
	t.Helper()

	previousRetryInterval := controllerContextRetryInterval
	previousRetryLimit := controllerContextRetryLimit
	previousLoopInterval := controllerLoopInterval

	controllerContextRetryInterval = retryInterval
	controllerContextRetryLimit = retryLimit
	controllerLoopInterval = loopInterval

	return func() {
		controllerContextRetryInterval = previousRetryInterval
		controllerContextRetryLimit = previousRetryLimit
		controllerLoopInterval = previousLoopInterval
	}
}

func setBootstrapTestTiming(t *testing.T, retryBackoff, gracePeriod time.Duration, retryLimit int, attemptLimit int64) func() {
	t.Helper()

	previousRetryBackoff := bootstrapRetryBackoff
	previousGracePeriod := bootstrapGracePeriod
	previousRetryLimit := bootstrapRetryLimit
	previousAttemptLimit := roleAttemptLimit

	bootstrapRetryBackoff = retryBackoff
	bootstrapGracePeriod = gracePeriod
	bootstrapRetryLimit = retryLimit
	roleAttemptLimit = attemptLimit

	return func() {
		bootstrapRetryBackoff = previousRetryBackoff
		bootstrapGracePeriod = previousGracePeriod
		bootstrapRetryLimit = previousRetryLimit
		roleAttemptLimit = previousAttemptLimit
	}
}

func createTestTaskAndRun(t *testing.T, taskID, runStatus string) (model.Task, model.TaskRun) {
	t.Helper()

	now := time.Now().UTC()
	task := model.Task{
		ID:        taskID,
		Name:      "test task",
		Status:    "running",
		CreatedAt: now,
	}
	run := model.TaskRun{
		ID:             taskID + "-run",
		TaskID:         task.ID,
		Status:         runStatus,
		StartedAt:      now,
		CreatedAt:      now,
		UpdatedAt:      now,
		PlannerPending: false,
	}
	return task, run
}

func mustCreateRunningAgentRun(t *testing.T, manager *Manager, taskID, runID, subtaskID, role, stage string) model.TaskAgentRun {
	t.Helper()

	agentRun := manager.buildAgentRun(taskID, runID, subtaskID, role, stage, role == roleWorker || role == roleValidator)
	now := time.Now().UTC()
	agentRun.Status = roleStatusRunning
	agentRun.StartedAt = &now
	agentRun.CreatedAt = now
	agentRun.UpdatedAt = now
	mustCreateRecords(t, &agentRun)
	return agentRun
}

func loadSubtaskForTest(t *testing.T, subtaskID string) model.TaskSubtask {
	t.Helper()

	var subtask model.TaskSubtask
	if err := database.DB.First(&subtask, "id = ?", subtaskID).Error; err != nil {
		t.Fatalf("load subtask %s: %v", subtaskID, err)
	}
	return subtask
}

func loadRunForTest(t *testing.T, runID string) model.TaskRun {
	t.Helper()

	var run model.TaskRun
	if err := database.DB.First(&run, "id = ?", runID).Error; err != nil {
		t.Fatalf("load run %s: %v", runID, err)
	}
	return run
}

func mustCreateRecords(t *testing.T, values ...any) {
	t.Helper()
	for _, value := range values {
		if err := database.DB.Create(value).Error; err != nil {
			t.Fatalf("create %T: %v", value, err)
		}
	}
}

func waitForRunID(t *testing.T, started <-chan string) string {
	t.Helper()

	select {
	case runID := <-started:
		return runID
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timed out waiting for controller restart")
		return ""
	}
}

func assertNoAdditionalControllerStart(t *testing.T, started <-chan string) {
	t.Helper()

	select {
	case runID := <-started:
		t.Fatalf("expected a single controller restart, got extra start for %s", runID)
	case <-time.After(50 * time.Millisecond):
	}
}
