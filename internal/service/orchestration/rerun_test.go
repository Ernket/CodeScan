package orchestration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"codescan/internal/database"
	"codescan/internal/model"
)

func TestRerunSelectedCarriesCompletedStagesAndReusesRoutes(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	manager.controllerRunner = func(string) {}
	task, _ := createTestTaskAndRun(t, "task-rerun-carry", runStatusFailed)
	task.Status = "failed"
	task.OutputJSON = json.RawMessage(`[{"method":"GET","path":"/health","source":"internal/api.go"}]`)
	stageCompletedAt := time.Now().UTC()
	completedStage := model.TaskStage{
		TaskID:     task.ID,
		Name:       "rce",
		Status:     "completed",
		OutputJSON: json.RawMessage(`[{"type":"RCE","subtype":"Command Injection","severity":"HIGH","description":"reachable sink","location":{"file":"handler.go","line":"18"},"trigger":{"method":"POST","path":"/run"}}]`),
		CreatedAt:  stageCompletedAt.Add(-1 * time.Minute),
		UpdatedAt:  stageCompletedAt,
	}
	failedStage := model.TaskStage{
		TaskID:    task.ID,
		Name:      "auth",
		Status:    "failed",
		Result:    "auth failed",
		CreatedAt: stageCompletedAt.Add(-2 * time.Minute),
		UpdatedAt: stageCompletedAt.Add(-90 * time.Second),
	}
	mustCreateRecords(t, &task, &completedStage, &failedStage)

	snapshot, err := manager.RerunSelected(task.ID, []string{"auth"})
	if err != nil {
		t.Fatalf("rerun selected: %v", err)
	}
	if snapshot.Run == nil {
		t.Fatal("expected run snapshot")
	}

	run := loadRunForTest(t, snapshot.Run.Run.ID)
	scope := decodeRunScope(&run)
	if scope.Mode != runModeRerunSelected {
		t.Fatalf("expected rerun_selected scope, got %+v", scope)
	}
	if strings.Join(scope.SelectedStages, ",") != "auth" {
		t.Fatalf("unexpected selected stages: %+v", scope.SelectedStages)
	}
	if strings.Join(scope.CarriedOverStages, ",") != "init,rce" {
		t.Fatalf("unexpected carried over stages: %+v", scope.CarriedOverStages)
	}

	subtasks, err := manager.loadSubtasks(run.ID)
	if err != nil {
		t.Fatalf("load subtasks: %v", err)
	}
	if len(subtasks) != 3 {
		t.Fatalf("expected 3 subtasks (init, rce carry-over, auth selected), got %d", len(subtasks))
	}

	stageStatus := map[string]string{}
	workerStatus := map[string]string{}
	for _, subtask := range subtasks {
		stageStatus[subtask.Stage] = subtask.Status
		workerStatus[subtask.Stage] = subtask.WorkerStatus
	}
	if stageStatus["init"] != subtaskStatusCompleted {
		t.Fatalf("expected carried-over init completed, got %s", stageStatus["init"])
	}
	if stageStatus["rce"] != subtaskStatusCompleted {
		t.Fatalf("expected carried-over rce completed, got %s", stageStatus["rce"])
	}
	if stageStatus["auth"] != subtaskStatusReady {
		t.Fatalf("expected selected auth ready, got %s", stageStatus["auth"])
	}
	if workerStatus["auth"] != roleStatusReady {
		t.Fatalf("expected selected auth worker ready, got %s", workerStatus["auth"])
	}

	var routeCount int64
	if err := database.DB.Model(&model.TaskRoute{}).Where("run_id = ?", run.ID).Count(&routeCount).Error; err != nil {
		t.Fatalf("count carried routes: %v", err)
	}
	if routeCount != 1 {
		t.Fatalf("expected 1 carried route, got %d", routeCount)
	}

	var findingCount int64
	if err := database.DB.Model(&model.TaskFinding{}).Where("run_id = ? AND origin_stage = ?", run.ID, "rce").Count(&findingCount).Error; err != nil {
		t.Fatalf("count carried findings: %v", err)
	}
	if findingCount != 1 {
		t.Fatalf("expected 1 carried finding, got %d", findingCount)
	}
}

func TestRerunSelectedRejectsAuditWithoutRouteInventory(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, _ := createTestTaskAndRun(t, "task-rerun-no-routes", runStatusFailed)
	task.Status = "failed"
	mustCreateRecords(t, &task)

	_, err := manager.RerunSelected(task.ID, []string{"auth"})
	if err == nil {
		t.Fatal("expected rerun to reject audit selection without routes")
	}
	if !strings.Contains(err.Error(), "route inventory is unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFinalizeRunIfTerminalKeepsTaskFailedWhenHistoricalFailuresRemain(t *testing.T) {
	setupOrchestrationServiceTestDB(t)
	restoreConfig := setServiceTestConfig(t)
	defer restoreConfig()

	manager := NewManager()
	task, run := createTestTaskAndRun(t, "task-rerun-terminal", runStatusRunning)
	task.Status = "running"
	run.SummaryJSON = marshalRunScope(runScope{
		Mode:                 runModeRerunSelected,
		SelectedStages:       []string{"rce"},
		CarriedOverStages:    []string{"init"},
		ReusedRouteInventory: true,
	})
	failedStage := model.TaskStage{
		TaskID:    task.ID,
		Name:      "auth",
		Status:    "failed",
		Result:    "auth failed",
		CreatedAt: time.Now().UTC().Add(-2 * time.Minute),
		UpdatedAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	mustCreateRecords(t, &task, &run, &failedStage)

	completedSubtasks := []model.TaskSubtask{
		buildCarryOverSubtask(task, run.ID, "init", nil),
		buildCarryOverSubtask(task, run.ID, "rce", &model.TaskStage{
			TaskID:     task.ID,
			Name:       "rce",
			Status:     "completed",
			OutputJSON: json.RawMessage(`[]`),
			CreatedAt:  time.Now().UTC().Add(-90 * time.Second),
			UpdatedAt:  time.Now().UTC().Add(-30 * time.Second),
		}),
	}

	if !manager.finalizeRunIfTerminal(&task, &run, completedSubtasks) {
		t.Fatal("expected finalizeRunIfTerminal to close the run")
	}

	storedRun := loadRunForTest(t, run.ID)
	if storedRun.Status != runStatusCompleted {
		t.Fatalf("expected run completed, got %s", storedRun.Status)
	}

	var storedTask model.Task
	if err := database.DB.First(&storedTask, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("load task: %v", err)
	}
	if storedTask.Status != "failed" {
		t.Fatalf("expected task to remain failed, got %s", storedTask.Status)
	}
}
