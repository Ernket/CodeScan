package orchestration

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"codescan/internal/config"
	"codescan/internal/model"
)

const stallThreshold = 15 * time.Minute
const routeInventoryWaitReason = "waiting for route inventory"

type diagnosticsFocus struct {
	status              string
	reason              string
	subtask             *model.TaskSubtask
	activeAgentRunID    string
	stallCandidateSince time.Time
	focusSilenceSeconds int64
}

type activeExecutionCandidate struct {
	subtask        *model.TaskSubtask
	agent          *model.TaskAgentRun
	role           string
	status         string
	lastProgress   time.Time
	silenceSeconds int64
}

func buildSnapshotDiagnostics(run *model.TaskRun, subtasks []model.TaskSubtask, agents []model.TaskAgentRun, events []model.TaskEvent, now time.Time) *SnapshotDiagnostics {
	if run == nil {
		return nil
	}

	diagnostics := &SnapshotDiagnostics{
		PlannerPending:        run.PlannerPending,
		LastReplanReason:      latestReplanReason(run, events),
		StallThresholdSeconds: int64(stallThreshold / time.Second),
		Parallelism:           snapshotParallelism(),
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

	activeCandidates := activeExecutionCandidates(subtasks, agents, events, now)
	activeCandidate := currentActiveCandidate(activeCandidates)
	stalledCandidate := stalledActiveCandidate(activeCandidates, subtasks)

	candidateForDiagnostics := activeCandidate
	diagnostics.Stalled = run.Status == runStatusRunning &&
		!run.PlannerPending &&
		stalledCandidate != nil
	if diagnostics.Stalled {
		candidateForDiagnostics = stalledCandidate
	}
	if candidateForDiagnostics != nil {
		if !candidateForDiagnostics.lastProgress.IsZero() {
			candidateSince := candidateForDiagnostics.lastProgress
			diagnostics.StallCandidateSince = &candidateSince
			diagnostics.FocusSilenceSeconds = candidateForDiagnostics.silenceSeconds
		}
		if candidateForDiagnostics.agent != nil {
			diagnostics.ActiveAgentRunID = candidateForDiagnostics.agent.ID
		}
	}

	focus := selectDiagnosticsFocus(run, subtasks, diagnostics.Stalled, candidateForDiagnostics)
	diagnostics.FocusStatus = focus.status
	diagnostics.FocusReason = focus.reason
	if focus.activeAgentRunID != "" {
		diagnostics.ActiveAgentRunID = focus.activeAgentRunID
	}
	if !focus.stallCandidateSince.IsZero() {
		candidateSince := focus.stallCandidateSince
		diagnostics.StallCandidateSince = &candidateSince
		diagnostics.FocusSilenceSeconds = focus.focusSilenceSeconds
	}

	if focus.subtask != nil {
		diagnostics.FocusSubtaskID = focus.subtask.ID
		diagnostics.FocusSubtaskTitle = focus.subtask.Title
		diagnostics.CurrentStage = focus.subtask.Stage
		diagnostics.CurrentRole = currentRoleForSubtask(*focus.subtask)
		diagnostics.BlockedReason = strings.TrimSpace(focus.subtask.BlockedReason)
		diagnostics.ErrorMessage = strings.TrimSpace(focus.subtask.ErrorMessage)
	}

	if focus.reason == "planner_pending" {
		diagnostics.CurrentRole = rolePlanner
	} else if diagnostics.CurrentRole == "" && run.PlannerPending {
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

func selectDiagnosticsFocus(run *model.TaskRun, subtasks []model.TaskSubtask, stalled bool, activeCandidate *activeExecutionCandidate) diagnosticsFocus {
	ordered := orderedSubtasks(subtasks)

	if blocked := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return isActionableBlockedSubtask(subtask)
	}); blocked != nil {
		return diagnosticsFocus{status: "blocked", reason: "subtask_blocked", subtask: blocked}
	}

	if stalled && activeCandidate != nil {
		return diagnosticsFocus{
			status:              "stalled",
			reason:              "silence_timeout",
			subtask:             activeCandidate.subtask,
			activeAgentRunID:    activeCandidateAgentID(activeCandidate),
			stallCandidateSince: activeCandidate.lastProgress,
			focusSilenceSeconds: activeCandidate.silenceSeconds,
		}
	}

	if activeCandidate != nil {
		status := "running"
		reason := "subtask_running"
		if activeCandidate.status == roleStatusStarting {
			status = "starting"
			reason = "subtask_starting"
		}
		return diagnosticsFocus{
			status:              status,
			reason:              reason,
			subtask:             activeCandidate.subtask,
			activeAgentRunID:    activeCandidateAgentID(activeCandidate),
			stallCandidateSince: activeCandidate.lastProgress,
			focusSilenceSeconds: activeCandidate.silenceSeconds,
		}
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

	if waiting := firstSubtask(ordered, func(subtask model.TaskSubtask) bool {
		return isDependencyWaitingSubtask(subtask)
	}); waiting != nil {
		return diagnosticsFocus{status: "waiting", reason: "dependency_waiting", subtask: waiting}
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

func activeExecutionCandidates(subtasks []model.TaskSubtask, agents []model.TaskAgentRun, events []model.TaskEvent, now time.Time) []activeExecutionCandidate {
	ordered := orderedSubtasks(subtasks)
	subtasksByID := make(map[string]*model.TaskSubtask, len(ordered))
	for i := range ordered {
		subtasksByID[ordered[i].ID] = &ordered[i]
	}

	latestAgents := latestAgentRunsByKey(agents)
	candidates := []activeExecutionCandidate{}
	seen := map[string]bool{}

	appendCandidate := func(subtask *model.TaskSubtask, role, status string, agent *model.TaskAgentRun) {
		if subtask == nil || strings.TrimSpace(role) == "" {
			return
		}
		key := agentAttemptKey(subtask.ID, role)
		if seen[key] {
			return
		}
		seen[key] = true

		lastProgress := activeCandidateLastProgress(*subtask, agent, events)
		candidate := activeExecutionCandidate{
			subtask:        subtask,
			agent:          agent,
			role:           role,
			status:         status,
			lastProgress:   lastProgress,
			silenceSeconds: secondsSince(now, lastProgress),
		}
		candidates = append(candidates, candidate)
	}

	for i := range ordered {
		subtask := &ordered[i]
		for _, role := range diagnosticRoleOrder() {
			status := subtaskRoleStatus(*subtask, role)
			if !isActiveExecutionStatus(status) {
				continue
			}

			var agent *model.TaskAgentRun
			if latest, ok := latestAgents[agentAttemptKey(subtask.ID, role)]; ok && isActiveExecutionStatus(latest.Status) {
				agentCopy := latest
				agent = &agentCopy
				status = latest.Status
			}
			appendCandidate(subtask, role, status, agent)
		}
	}

	for i := range agents {
		agent := agents[i]
		if !isActiveExecutionStatus(agent.Status) {
			continue
		}
		subtask := subtasksByID[agent.SubtaskID]
		if subtask == nil {
			continue
		}
		agentCopy := agent
		appendCandidate(subtask, agent.Role, agent.Status, &agentCopy)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return activeCandidateSortLess(candidates[i], candidates[j])
	})
	return candidates
}

func diagnosticRoleOrder() []string {
	return []string{roleWorker, roleIntegrator, roleValidator, rolePersistence}
}

func currentActiveCandidate(candidates []activeExecutionCandidate) *activeExecutionCandidate {
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0]
}

func stalledActiveCandidate(candidates []activeExecutionCandidate, subtasks []model.TaskSubtask) *activeExecutionCandidate {
	var selected *activeExecutionCandidate
	suppressInitGate := hasRouteInventoryDependencyWait(subtasks)
	for i := range candidates {
		candidate := &candidates[i]
		if suppressInitGate && activeCandidateStage(*candidate) == "init" {
			continue
		}
		if candidate.lastProgress.IsZero() || candidate.silenceSeconds < int64(stallThreshold/time.Second) {
			continue
		}
		if selected == nil ||
			candidate.silenceSeconds > selected.silenceSeconds ||
			(candidate.silenceSeconds == selected.silenceSeconds && activeCandidateSortLess(*candidate, *selected)) {
			selected = candidate
		}
	}
	return selected
}

func activeCandidateLastProgress(subtask model.TaskSubtask, agent *model.TaskAgentRun, events []model.TaskEvent) time.Time {
	latest := maxTime(subtask.CreatedAt, subtask.UpdatedAt, derefTime(subtask.StartedAt))
	if agent != nil {
		latest = maxTime(latest, agent.CreatedAt, agent.UpdatedAt, derefTime(agent.StartedAt))
	}

	for _, event := range events {
		if !isActiveProgressEvent(event) {
			continue
		}
		if agent != nil && event.AgentRunID == agent.ID {
			latest = maxTime(latest, event.CreatedAt)
			continue
		}
		if event.SubtaskID == subtask.ID {
			latest = maxTime(latest, event.CreatedAt)
		}
	}

	return latest
}

func isActiveProgressEvent(event model.TaskEvent) bool {
	switch event.EventType {
	case eventAgentStarted, eventAgentUpdated, eventSubtaskUpdated:
		return true
	default:
		return false
	}
}

func secondsSince(now time.Time, value time.Time) int64 {
	if value.IsZero() || !now.After(value) {
		return 0
	}
	return int64(now.Sub(value) / time.Second)
}

func activeCandidateSortLess(left, right activeExecutionCandidate) bool {
	if leftStatus, rightStatus := activeStatusRank(left.status), activeStatusRank(right.status); leftStatus != rightStatus {
		return leftStatus < rightStatus
	}
	if leftStage, rightStage := stagePriority(activeCandidateStage(left)), stagePriority(activeCandidateStage(right)); leftStage != rightStage {
		return leftStage < rightStage
	}
	if leftPriority, rightPriority := activeCandidatePriority(left), activeCandidatePriority(right); leftPriority != rightPriority {
		return leftPriority < rightPriority
	}
	if leftRole, rightRole := roleOrderIndex(left.role), roleOrderIndex(right.role); leftRole != rightRole {
		return leftRole < rightRole
	}
	return activeCandidateAgentID(&left) < activeCandidateAgentID(&right)
}

func activeStatusRank(status string) int {
	switch status {
	case roleStatusStarting:
		return 0
	case roleStatusRunning:
		return 1
	default:
		return 2
	}
}

func roleOrderIndex(role string) int {
	for index, item := range diagnosticRoleOrder() {
		if item == role {
			return index
		}
	}
	return len(diagnosticRoleOrder())
}

func activeCandidateStage(candidate activeExecutionCandidate) string {
	if candidate.subtask != nil {
		return candidate.subtask.Stage
	}
	if candidate.agent != nil {
		return candidate.agent.Stage
	}
	return ""
}

func activeCandidatePriority(candidate activeExecutionCandidate) int {
	if candidate.subtask != nil {
		return candidate.subtask.Priority
	}
	return 0
}

func activeCandidateAgentID(candidate *activeExecutionCandidate) string {
	if candidate == nil || candidate.agent == nil {
		return ""
	}
	return candidate.agent.ID
}

func isActiveExecutionStatus(status string) bool {
	return status == roleStatusStarting || status == roleStatusRunning
}

func isActionableBlockedSubtask(subtask model.TaskSubtask) bool {
	return subtask.Status == subtaskStatusBlocked && hasActionableBlockedReason(subtask.BlockedReason)
}

func isDependencyWaitingSubtask(subtask model.TaskSubtask) bool {
	return subtask.Status == subtaskStatusBlocked && !hasActionableBlockedReason(subtask.BlockedReason)
}

func hasActionableBlockedReason(reason string) bool {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	return normalized != "" && normalized != routeInventoryWaitReason
}

func hasRouteInventoryDependencyWait(subtasks []model.TaskSubtask) bool {
	for _, subtask := range subtasks {
		if subtask.Status == subtaskStatusBlocked && strings.EqualFold(strings.TrimSpace(subtask.BlockedReason), routeInventoryWaitReason) {
			return true
		}
	}
	return false
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
