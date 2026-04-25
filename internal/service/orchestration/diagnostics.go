package orchestration

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"
)

const stuckThreshold = 180 * time.Second

type diagnosticsFocus struct {
	status  string
	reason  string
	subtask *model.TaskSubtask
}

func buildSnapshotDiagnostics(run *model.TaskRun, subtasks []model.TaskSubtask, agents []model.TaskAgentRun, events []model.TaskEvent, now time.Time) *SnapshotDiagnostics {
	if run == nil {
		return nil
	}

	diagnostics := &SnapshotDiagnostics{
		PlannerPending:   run.PlannerPending,
		LastReplanReason: latestReplanReason(run, events),
		Parallelism:      snapshotParallelism(),
	}

	if event := latestEvent(events); event != nil {
		diagnostics.LatestEventType = event.EventType
		diagnostics.LatestEventMessage = event.Message
		eventAt := event.CreatedAt
		diagnostics.LatestEventAt = &eventAt
	}

	lastProgressAt := latestProgressAt(run, subtasks, agents, events)
	if !lastProgressAt.IsZero() {
		progressAt := lastProgressAt
		diagnostics.LastProgressAt = &progressAt
		if now.After(lastProgressAt) {
			diagnostics.SilenceSeconds = int64(now.Sub(lastProgressAt) / time.Second)
		}
	}

	diagnostics.Stalled = run.Status == runStatusRunning &&
		!run.PlannerPending &&
		diagnostics.LastProgressAt != nil &&
		now.Sub(*diagnostics.LastProgressAt) >= stuckThreshold

	focus := selectDiagnosticsFocus(run, subtasks, diagnostics.Stalled)
	diagnostics.FocusStatus = focus.status
	diagnostics.FocusReason = focus.reason

	if focus.subtask != nil {
		diagnostics.FocusSubtaskID = focus.subtask.ID
		diagnostics.FocusSubtaskTitle = focus.subtask.Title
		diagnostics.CurrentStage = focus.subtask.Stage
		diagnostics.CurrentRole = currentRoleForSubtask(*focus.subtask)
		diagnostics.BlockedReason = strings.TrimSpace(focus.subtask.BlockedReason)
		diagnostics.ErrorMessage = strings.TrimSpace(focus.subtask.ErrorMessage)
	}

	if diagnostics.CurrentRole == "" && run.PlannerPending {
		diagnostics.CurrentRole = rolePlanner
	}

	if diagnostics.ErrorMessage == "" {
		diagnostics.ErrorMessage = strings.TrimSpace(run.ErrorMessage)
	}

	return diagnostics
}

func snapshotParallelism() Parallelism {
	return Parallelism{
		Planner:     config.Orchestration.Planner.Parallelism,
		Worker:      config.Orchestration.Worker.Parallelism,
		Integrator:  config.Orchestration.Integrator.Parallelism,
		Validator:   config.Orchestration.Validator.Parallelism,
		Persistence: config.Orchestration.Persistence.Parallelism,
	}
}

func selectDiagnosticsFocus(run *model.TaskRun, subtasks []model.TaskSubtask, stalled bool) diagnosticsFocus {
	ordered := orderedSubtasks(subtasks)

	if blocked := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return subtask.Status == subtaskStatusBlocked
	}); blocked != nil {
		return diagnosticsFocus{status: "blocked", reason: "subtask_blocked", subtask: blocked}
	}

	if stalled {
		target := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
			return subtask.Status == subtaskStatusRunning || hasRoleState(subtask, roleStatusStarting) || hasRoleState(subtask, roleStatusRunning)
		})
		if target == nil {
			target = firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
				return subtask.Status != subtaskStatusCompleted
			})
		}
		return diagnosticsFocus{status: "stalled", reason: "silence_timeout", subtask: target}
	}

	if starting := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return hasRoleState(subtask, roleStatusStarting)
	}); starting != nil {
		return diagnosticsFocus{status: "starting", reason: "subtask_starting", subtask: starting}
	}

	if running := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return subtask.Status == subtaskStatusRunning || hasRoleState(subtask, roleStatusRunning)
	}); running != nil {
		return diagnosticsFocus{status: "running", reason: "subtask_running", subtask: running}
	}

	if run.PlannerPending {
		return diagnosticsFocus{status: "running", reason: "planner_pending", subtask: firstIncompleteSubtask(ordered)}
	}

	if failed := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return subtask.Status == subtaskStatusFailed
	}); failed != nil {
		return diagnosticsFocus{status: "failed", reason: "subtask_failed", subtask: failed}
	}

	if paused := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return subtask.Status == subtaskStatusPaused || hasRoleState(subtask, roleStatusPaused)
	}); paused != nil {
		return diagnosticsFocus{status: "paused", reason: "subtask_paused", subtask: paused}
	}

	if waiting := firstIncompleteSubtask(ordered); waiting != nil {
		return diagnosticsFocus{status: "waiting", reason: "awaiting_execution", subtask: waiting}
	}

	switch run.Status {
	case runStatusPaused:
		return diagnosticsFocus{status: "paused", reason: "run_paused"}
	case runStatusFailed:
		return diagnosticsFocus{status: "failed", reason: "run_failed"}
	default:
		return diagnosticsFocus{status: "completed", reason: "run_completed"}
	}
}

func orderedSubtasks(subtasks []model.TaskSubtask) []model.TaskSubtask {
	ordered := append([]model.TaskSubtask(nil), subtasks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority == ordered[j].Priority {
			if ordered[i].Stage == ordered[j].Stage {
				return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
			}
			return stagePriority(ordered[i].Stage) < stagePriority(ordered[j].Stage)
		}
		return ordered[i].Priority < ordered[j].Priority
	})
	return ordered
}

func firstSubtask(subtasks []model.TaskSubtask, match func(model.TaskSubtask) bool) *model.TaskSubtask {
	for i := range subtasks {
		if match(subtasks[i]) {
			return &subtasks[i]
		}
	}
	return nil
}

func firstIncompleteSubtask(subtasks []model.TaskSubtask) *model.TaskSubtask {
	return firstSubtask(subtasks, func(subtask model.TaskSubtask) bool {
		return subtask.Status != subtaskStatusCompleted
	})
}

func latestEvent(events []model.TaskEvent) *model.TaskEvent {
	if len(events) == 0 {
		return nil
	}

	latest := events[0]
	for _, event := range events[1:] {
		if event.Sequence > latest.Sequence {
			latest = event
		}
	}
	return &latest
}

func latestProgressAt(run *model.TaskRun, subtasks []model.TaskSubtask, agents []model.TaskAgentRun, events []model.TaskEvent) time.Time {
	latest := maxTime(run.StartedAt, run.CreatedAt, run.UpdatedAt, derefTime(run.CompletedAt), derefTime(run.PausedAt))

	for _, subtask := range subtasks {
		latest = maxTime(latest, subtask.CreatedAt, subtask.UpdatedAt, derefTime(subtask.StartedAt), derefTime(subtask.CompletedAt))
	}
	for _, agent := range agents {
		latest = maxTime(latest, agent.CreatedAt, agent.UpdatedAt, derefTime(agent.StartedAt), derefTime(agent.CompletedAt))
	}
	for _, event := range events {
		latest = maxTime(latest, event.CreatedAt)
	}

	return latest
}

func currentRoleForSubtask(subtask model.TaskSubtask) string {
	roles := []struct {
		role   string
		status string
	}{
		{role: roleWorker, status: subtask.WorkerStatus},
		{role: roleIntegrator, status: subtask.IntegratorStatus},
		{role: roleValidator, status: subtask.ValidatorStatus},
		{role: rolePersistence, status: subtask.PersistenceStatus},
	}

	statusOrder := []string{
		roleStatusStarting,
		roleStatusRunning,
		roleStatusPaused,
		roleStatusFailed,
		roleStatusReady,
		roleStatusPending,
	}

	for _, desired := range statusOrder {
		for _, item := range roles {
			if item.status == desired {
				return item.role
			}
		}
	}

	return ""
}

func hasRoleState(subtask model.TaskSubtask, desired string) bool {
	return subtask.WorkerStatus == desired ||
		subtask.IntegratorStatus == desired ||
		subtask.ValidatorStatus == desired ||
		subtask.PersistenceStatus == desired
}

func latestReplanReason(run *model.TaskRun, events []model.TaskEvent) string {
	if strings.TrimSpace(run.LastReplanReason) != "" {
		return strings.TrimSpace(run.LastReplanReason)
	}

	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.EventType != eventPlannerRevised {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal(event.PayloadJSON, &payload); err != nil {
			continue
		}
		if reason := strings.TrimSpace(stringValue(payload["reason"])); reason != "" {
			return reason
		}
	}

	return ""
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}

func maxTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.IsZero() {
			continue
		}
		if latest.IsZero() || value.After(latest) {
			latest = value
		}
	}
	return latest
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
