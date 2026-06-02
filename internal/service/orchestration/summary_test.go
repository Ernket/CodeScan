package orchestration

import (
	"encoding/json"
	"testing"
	"time"

	"codescan/internal/model"
)

func TestManagerSummaryIncludesLightweightFocusFields(t *testing.T) {
	now := time.Now().UTC()

	testCases := []struct {
		name                 string
		run                  model.TaskRun
		subtasks             []model.TaskSubtask
		events               []model.TaskEvent
		expectedActiveRunID  string
		expectedFocusStatus  string
		expectedCurrentStage string
		expectedLastStatus   string
		expectedEventMessage string
		expectEventAt        bool
		expectProgressAt     bool
		expectedReplanReason string
	}{
		{
			name: "running",
			run: model.TaskRun{
				ID:              "task-running-run",
				TaskID:          "task-running",
				Status:          runStatusRunning,
				PlannerRevision: 3,
				PlannerPending:  false,

				StartedAt: now.Add(-5 * time.Minute),
				CreatedAt: now.Add(-5 * time.Minute),
				UpdatedAt: now.Add(-40 * time.Second),
			},
			subtasks: []model.TaskSubtask{
				testSubtask("rce", 10, subtaskStatusRunning, roleStatusRunning, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-30*time.Second)),
			},
			events: []model.TaskEvent{
				{
					Sequence:    1,
					ID:          "evt-running-1",
					TaskID:      "task-running",
					RunID:       "task-running-run",
					EventType:   eventPlannerRevised,
					Message:     "Planner revision applied.",
					PayloadJSON: json.RawMessage(`{"reason":"integrator_completed"}`),
					CreatedAt:   now.Add(-20 * time.Second),
				},
				{
					Sequence:  2,
					ID:        "evt-running-2",
					TaskID:    "task-running",
					RunID:     "task-running-run",
					SubtaskID: "rce-1",
					EventType: eventAgentStarted,
					Message:   "worker started for rce.",
					CreatedAt: now.Add(-10 * time.Second),
				},
			},
			expectedActiveRunID:  "task-running-run",
			expectedFocusStatus:  "running",
			expectedCurrentStage: "rce",
			expectedLastStatus:   runStatusRunning,
			expectedEventMessage: "worker started for rce.",
			expectEventAt:        true,
			expectProgressAt:     true,
			expectedReplanReason: "integrator_completed",
		},
		{
			name: "blocked",
			run: model.TaskRun{
				ID:              "task-blocked-run",
				TaskID:          "task-blocked",
				Status:          runStatusRunning,
				PlannerRevision: 1,
				StartedAt:       now.Add(-6 * time.Minute),
				CreatedAt:       now.Add(-6 * time.Minute),
				UpdatedAt:       now.Add(-45 * time.Second),
			},
			subtasks: []model.TaskSubtask{
				func() model.TaskSubtask {
					subtask := testSubtask("auth", 20, subtaskStatusBlocked, roleStatusPending, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-35*time.Second))
					subtask.BlockedReason = "awaiting upstream route verification"
					return subtask
				}(),
			},
			expectedActiveRunID:  "task-blocked-run",
			expectedFocusStatus:  "blocked",
			expectedCurrentStage: "auth",
			expectedLastStatus:   runStatusRunning,
			expectProgressAt:     true,
		},
		{
			name: "paused",
			run: model.TaskRun{
				ID:              "task-paused-run",
				TaskID:          "task-paused",
				Status:          runStatusPaused,
				PlannerRevision: 2,
				StartedAt:       now.Add(-7 * time.Minute),
				CreatedAt:       now.Add(-7 * time.Minute),
				UpdatedAt:       now.Add(-25 * time.Second),
			},
			subtasks: []model.TaskSubtask{
				testSubtask("xss", 30, subtaskStatusPaused, roleStatusPaused, roleStatusPending, roleStatusPending, roleStatusPending, now.Add(-25*time.Second)),
			},
			expectedActiveRunID:  "task-paused-run",
			expectedFocusStatus:  "paused",
			expectedCurrentStage: "xss",
			expectedLastStatus:   runStatusPaused,
			expectProgressAt:     true,
		},
		{
			name: "completed",
			run: model.TaskRun{
				ID:              "task-completed-run",
				TaskID:          "task-completed",
				Status:          runStatusCompleted,
				PlannerRevision: 4,
				StartedAt:       now.Add(-9 * time.Minute),
				CreatedAt:       now.Add(-9 * time.Minute),
				UpdatedAt:       now.Add(-1 * time.Minute),
			},
			subtasks: []model.TaskSubtask{
				testCompletedSubtask("init", 0, now.Add(-1*time.Minute)),
				testCompletedSubtask("logic", 80, now.Add(-50*time.Second)),
			},
			expectedFocusStatus: "completed",
			expectedLastStatus:  runStatusCompleted,
			expectProgressAt:    true,
		},
		{
			name: "failed",
			run: model.TaskRun{
				ID:              "task-failed-run",
				TaskID:          "task-failed",
				Status:          runStatusFailed,
				PlannerRevision: 2,
				StartedAt:       now.Add(-8 * time.Minute),
				CreatedAt:       now.Add(-8 * time.Minute),
				UpdatedAt:       now.Add(-40 * time.Second),
			},
			subtasks: []model.TaskSubtask{
				func() model.TaskSubtask {
					subtask := testSubtask("access", 40, subtaskStatusFailed, roleStatusCompleted, roleStatusCompleted, roleStatusFailed, roleStatusPending, now.Add(-40*time.Second))
					subtask.ErrorMessage = "validator could not reconcile findings"
					return subtask
				}(),
			},
			expectedFocusStatus:  "failed",
			expectedCurrentStage: "access",
			expectedLastStatus:   runStatusFailed,
			expectProgressAt:     true,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			setupOrchestrationServiceTestDB(t)
			restoreConfig := setServiceTestConfig(t)
			defer restoreConfig()

			manager := NewManager()
			manager.controllerRunner = func(string) {}

			task := model.Task{
				ID:        tc.run.TaskID,
				Name:      tc.name,
				Status:    "running",
				CreatedAt: tc.run.CreatedAt,
			}
			if tc.run.Status == runStatusPaused {
				task.Status = "paused"
			}
			if tc.run.Status == runStatusCompleted {
				task.Status = "completed"
			}
			if tc.run.Status == runStatusFailed {
				task.Status = "failed"
			}

			records := []any{&task, &tc.run}
			for i := range tc.subtasks {
				tc.subtasks[i].TaskID = tc.run.TaskID
				tc.subtasks[i].RunID = tc.run.ID
				records = append(records, &tc.subtasks[i])
			}
			for i := range tc.events {
				if tc.events[i].TaskID == "" {
					tc.events[i].TaskID = tc.run.TaskID
				}
				if tc.events[i].RunID == "" {
					tc.events[i].RunID = tc.run.ID
				}
				records = append(records, &tc.events[i])
			}
			mustCreateRecords(t, records...)

			summary, err := manager.Summary(tc.run.TaskID)
			if err != nil {
				t.Fatalf("summary failed: %v", err)
			}
			if summary == nil {
				t.Fatal("expected summary")
			}
			if summary.ActiveRunID != tc.expectedActiveRunID {
				t.Fatalf("expected active run id %q, got %q", tc.expectedActiveRunID, summary.ActiveRunID)
			}
			if summary.FocusStatus != tc.expectedFocusStatus {
				t.Fatalf("expected focus status %q, got %q", tc.expectedFocusStatus, summary.FocusStatus)
			}
			if summary.CurrentStage != tc.expectedCurrentStage {
				t.Fatalf("expected current stage %q, got %q", tc.expectedCurrentStage, summary.CurrentStage)
			}
			if summary.LastRunStatus != tc.expectedLastStatus {
				t.Fatalf("expected last run status %q, got %q", tc.expectedLastStatus, summary.LastRunStatus)
			}
			if summary.ActiveSubtaskCount != countActiveSubtasks(tc.subtasks) {
				t.Fatalf("expected active subtask count %d, got %d", countActiveSubtasks(tc.subtasks), summary.ActiveSubtaskCount)
			}
			if summary.LatestEventMessage != tc.expectedEventMessage {
				t.Fatalf("expected latest event message %q, got %q", tc.expectedEventMessage, summary.LatestEventMessage)
			}
			if tc.expectEventAt != (summary.LatestEventAt != nil) {
				t.Fatalf("expected latest event presence %v, got %v", tc.expectEventAt, summary.LatestEventAt != nil)
			}
			if tc.expectProgressAt != (summary.LastProgressAt != nil) {
				t.Fatalf("expected last progress presence %v, got %v", tc.expectProgressAt, summary.LastProgressAt != nil)
			}
			if summary.LastReplanReason != tc.expectedReplanReason {
				t.Fatalf("expected replan reason %q, got %q", tc.expectedReplanReason, summary.LastReplanReason)
			}
		})
	}
}
