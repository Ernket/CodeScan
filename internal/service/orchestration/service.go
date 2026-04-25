package orchestration

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"codescan/internal/config"
	"codescan/internal/database"
	"codescan/internal/model"
	"codescan/internal/service/scanner"
	summarysvc "codescan/internal/service/summary"
	"codescan/internal/utils"
)

var defaultManager = NewManager()

var (
	controllerContextRetryInterval = time.Second
	controllerContextRetryLimit    = 10
	controllerLoopInterval         = 500 * time.Millisecond
	bootstrapRetryBackoff          = 2 * time.Second
	bootstrapGracePeriod           = 30 * time.Second
	bootstrapRetryLimit            = 2
	roleAttemptLimit               = int64(2)
)

type Manager struct {
	hub         *eventHub
	controllers sync.Map

	controllerRunner func(runID string)
	loadRunContextFn func(runID string) (*model.TaskRun, *model.Task, []model.TaskSubtask, error)
	startAgentRunFn  func(taskID, runID, subtaskID, role string) agentRunStartResult
}

func NewManager() *Manager {
	return &Manager{
		hub: newEventHub(),
	}
}

func DefaultManager() *Manager {
	return defaultManager
}

func (m *Manager) RecoverActiveRuns() error {
	if !config.Orchestration.Enabled {
		return nil
	}

	var runs []model.TaskRun
	if err := database.DB.Where("status = ?", runStatusRunning).Order("created_at asc").Find(&runs).Error; err != nil {
		return err
	}

	for _, run := range runs {
		if _, err := m.repairRunOrphans(run.ID, time.Now()); err != nil {
			log.Printf("warning: failed to repair orchestration run %s during startup recovery: %v", run.ID, err)
		}
		m.ensureController(run.ID)
	}

	return nil
}

type agentRunStartResult struct {
	AgentRun model.TaskAgentRun
	Resume   bool
	Scheduled bool
	Err      error
}

type roleExecutionContext struct {
	Run     *model.TaskRun
	Task    *model.Task
	Subtask *model.TaskSubtask
}

var errAgentRunSkipped = errors.New("agent run start skipped")

func (m *Manager) Start(taskID string) (*Snapshot, error) {
	if !config.Orchestration.Enabled {
		return nil, fmt.Errorf("orchestration is disabled")
	}

	var task model.Task
	if err := database.DB.First(&task, "id = ?", taskID).Error; err != nil {
		return nil, err
	}
	if strings.EqualFold(task.Status, "running") {
		return nil, fmt.Errorf("task %s is already running", taskID)
	}

	if run, _ := m.activeRun(taskID); run != nil {
		return nil, fmt.Errorf("task %s already has an active orchestration run", taskID)
	}

	now := time.Now()
	run := model.TaskRun{
		ID:               utils.NewOpaqueID(),
		TaskID:           taskID,
		Status:           runStatusRunning,
		PlannerPending:   true,
		LastReplanReason: "run_start",
		StartedAt:        now,
	}

	subtasks := buildInitialSubtasks(task, run.ID)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		if len(subtasks) > 0 {
			if err := tx.Create(&subtasks).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.Task{}).Where("id = ?", taskID).Update("status", "running").Error
	}); err != nil {
		return nil, err
	}

	if _, err := m.emit(taskID, run.ID, "", "", eventRunStarted, "info", "Orchestration run started.", map[string]any{
		"run_id":      run.ID,
		"subtask_ids": extractSubtaskIDs(subtasks),
	}); err != nil {
		log.Printf("warning: failed to emit orchestration start event: %v", err)
	}

	m.ensureController(run.ID)
	return m.Snapshot(taskID)
}

func (m *Manager) MarkPaused(taskID string) error {
	run, err := m.activeRun(taskID)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}

	now := time.Now()
	if err := database.DB.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status":    runStatusPaused,
		"paused_at": &now,
	}).Error; err != nil {
		return err
	}

	_, _ = m.emit(taskID, run.ID, "", "", eventRunPaused, "info", "Orchestration run paused.", nil)
	return nil
}

func (m *Manager) Resume(taskID string) (*Snapshot, error) {
	run, err := m.latestRun(taskID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("task %s has no orchestration run", taskID)
	}
	if run.Status != runStatusPaused {
		return nil, fmt.Errorf("task %s has no paused orchestration run", taskID)
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status":             runStatusRunning,
			"planner_pending":    true,
			"last_replan_reason": "resume",
			"paused_at":          nil,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.Task{}).Where("id = ?", taskID).Update("status", "running").Error
	}); err != nil {
		return nil, err
	}

	_, _ = m.emit(taskID, run.ID, "", "", eventRunResumed, "info", "Orchestration run resumed.", nil)
	m.ensureController(run.ID)
	return m.Snapshot(taskID)
}

func (m *Manager) Summary(taskID string) (*TaskSummary, error) {
	run, err := m.ensureActiveController(taskID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, nil
	}

	subtasks, err := m.loadSubtasks(run.ID)
	if err != nil {
		return nil, err
	}

	summary := &TaskSummary{
		ActiveRunID:        run.ID,
		PlannerRevision:    run.PlannerRevision,
		ActiveSubtaskCount: countActiveSubtasks(subtasks),
		StageProgress:      buildStageProgress(subtasks),
		LastRunStatus:      run.Status,
		LastReplanReason:   run.LastReplanReason,
	}

	if run.Status != runStatusRunning && run.Status != runStatusPaused {
		summary.ActiveRunID = ""
	}

	return summary, nil
}

func (m *Manager) Snapshot(taskID string) (*Snapshot, error) {
	run, err := m.ensureActiveController(taskID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return &Snapshot{
			Subtasks:  []model.TaskSubtask{},
			Agents:    []model.TaskAgentRun{},
			Routes:    []model.TaskRoute{},
			Findings:  []model.TaskFinding{},
			Events:    []model.TaskEvent{},
			UpdatedAt: time.Now(),
		}, nil
	}

	subtasks, err := m.loadSubtasks(run.ID)
	if err != nil {
		return nil, err
	}
	agents, err := m.loadAgents(run.ID)
	if err != nil {
		return nil, err
	}
	events, err := m.ListEvents(taskID, 0, 120)
	if err != nil {
		return nil, err
	}

	var routes []model.TaskRoute
	if err := database.DB.Where("task_id = ? AND run_id = ?", taskID, run.ID).Order("updated_at desc").Limit(120).Find(&routes).Error; err != nil {
		return nil, err
	}
	var findings []model.TaskFinding
	if err := database.DB.Where("task_id = ? AND run_id = ?", taskID, run.ID).Order("updated_at desc").Limit(200).Find(&findings).Error; err != nil {
		return nil, err
	}

	return &Snapshot{
		Run: &RunSummary{
			Run:                *run,
			ActiveSubtaskCount: countActiveSubtasks(subtasks),
			CompletedCount:     countSubtasksByStatus(subtasks, subtaskStatusCompleted),
			FailedCount:        countSubtasksByStatus(subtasks, subtaskStatusFailed),
			PausedCount:        countSubtasksByStatus(subtasks, subtaskStatusPaused),
			PlannerRevision:    run.PlannerRevision,
			StageProgress:      buildStageProgress(subtasks),
		},
		Diagnostics: buildSnapshotDiagnostics(run, subtasks, agents, events, time.Now()),
		Subtasks:    subtasks,
		Agents:      agents,
		Routes:      routes,
		Findings:    findings,
		Events:      events,
		UpdatedAt:   time.Now(),
	}, nil
}

func (m *Manager) ListSubtasks(taskID string) ([]model.TaskSubtask, error) {
	run, err := m.ensureActiveController(taskID)
	if err != nil || run == nil {
		return []model.TaskSubtask{}, err
	}
	return m.loadSubtasks(run.ID)
}

func (m *Manager) ListAgents(taskID string) ([]model.TaskAgentRun, error) {
	run, err := m.ensureActiveController(taskID)
	if err != nil || run == nil {
		return []model.TaskAgentRun{}, err
	}
	return m.loadAgents(run.ID)
}

func (m *Manager) ListEvents(taskID string, after uint64, limit int) ([]model.TaskEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	if _, err := m.ensureActiveController(taskID); err != nil {
		return nil, err
	}

	var events []model.TaskEvent
	query := database.DB.Where("task_id = ?", taskID)
	if after > 0 {
		query = query.Where("sequence > ?", after)
	}
	if err := query.Order("sequence asc").Limit(limit).Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func (m *Manager) Subscribe(taskID string) (<-chan model.TaskEvent, func()) {
	return m.hub.subscribe(taskID)
}

func (m *Manager) ensureActiveController(taskID string) (*model.TaskRun, error) {
	run, err := m.latestRun(taskID)
	if err != nil || run == nil {
		return run, err
	}
	if run.Status != runStatusRunning {
		return run, nil
	}
	if _, err := m.repairRunOrphans(run.ID, time.Now()); err != nil {
		log.Printf("warning: failed to repair orchestration run %s during inspection: %v", run.ID, err)
	}
	if _, loaded := m.controllers.Load(run.ID); loaded {
		return run, nil
	}
	m.ensureController(run.ID)
	return run, nil
}

func (m *Manager) ensureController(runID string) {
	if _, loaded := m.controllers.LoadOrStore(runID, struct{}{}); loaded {
		return
	}

	runner := m.controllerRunner
	if runner == nil {
		runner = m.driveRun
	}

	go func() {
		defer m.controllers.Delete(runID)
		runner(runID)
	}()
}

func (m *Manager) driveRun(runID string) {
	idleLoops := 0
	loadFailures := 0
	var lastRun *model.TaskRun
	var lastTask *model.Task
	loadRunContext := m.loadRunContextFn
	if loadRunContext == nil {
		loadRunContext = m.loadRunContext
	}
	for {
		run, task, subtasks, err := loadRunContext(runID)
		if run != nil {
			lastRun = run
		}
		if task != nil {
			lastTask = task
		}
		if err != nil {
			loadFailures++
			log.Printf("warning: orchestration controller failed to load run %s (attempt %d/%d): %v", runID, loadFailures, controllerContextRetryLimit, err)
			if loadFailures >= controllerContextRetryLimit {
				if lastRun == nil || lastTask == nil {
					fallbackRun, fallbackTask, fallbackErr := m.loadRunTask(runID)
					if fallbackErr != nil {
						log.Printf("warning: orchestration controller could not reload run %s for failure handling: %v", runID, fallbackErr)
					} else {
						lastRun = fallbackRun
						lastTask = fallbackTask
					}
				}

				message := fmt.Sprintf("controller lost run context after repeated retries: %v", err)
				if lastRun != nil && lastTask != nil {
					m.failRun(lastTask, lastRun, message)
				} else {
					log.Printf("warning: orchestration controller abandoning run %s without failure transition: %s", runID, message)
				}
				return
			}

			time.Sleep(controllerContextRetryInterval)
			continue
		}
		loadFailures = 0
		if run == nil {
			return
		}
		if run.Status == runStatusPaused || run.Status == runStatusCompleted || run.Status == runStatusFailed {
			return
		}

		if repaired, repairErr := m.repairOrphanAttempts(task, run, subtasks, time.Now()); repairErr != nil {
			log.Printf("warning: orchestration controller failed to repair orphan attempts for run %s: %v", run.ID, repairErr)
		} else if repaired > 0 {
			subtasks, _ = m.loadSubtasks(run.ID)
		}

		if run.PlannerPending {
			if err := m.executePlanner(task, run, subtasks); err != nil {
				m.failRun(task, run, fmt.Sprintf("Planner failed: %v", err))
				return
			}
			subtasks, _ = m.loadSubtasks(run.ID)
		}

		launched := false
		if m.dispatchRole(task, run, subtasks, roleWorker) {
			launched = true
		}
		subtasks, _ = m.loadSubtasks(run.ID)
		if m.dispatchRole(task, run, subtasks, roleIntegrator) {
			launched = true
		}
		subtasks, _ = m.loadSubtasks(run.ID)
		if m.dispatchRole(task, run, subtasks, roleValidator) {
			launched = true
		}
		subtasks, _ = m.loadSubtasks(run.ID)
		if m.dispatchRole(task, run, subtasks, rolePersistence) {
			launched = true
		}

		subtasks, _ = m.loadSubtasks(run.ID)
		if m.finalizeRunIfTerminal(task, run, subtasks) {
			return
		}

		if !launched && !hasReadyRole(subtasks) && !hasRunningRole(subtasks) {
			idleLoops++
			if idleLoops >= 2 {
				_ = m.requestPlanner(run.ID, "queue_idle")
				idleLoops = 0
			}
		} else {
			idleLoops = 0
		}

		time.Sleep(controllerLoopInterval)
	}
}

func (m *Manager) executePlanner(task *model.Task, run *model.TaskRun, subtasks []model.TaskSubtask) error {
	hasRoutes := m.routesAvailable(task, run.ID)
	initFailed := false
	for _, subtask := range subtasks {
		if subtask.Stage == "init" && subtask.Status == subtaskStatusFailed {
			initFailed = true
			break
		}
	}

	existing := make(map[string]model.TaskSubtask, len(subtasks))
	for _, subtask := range subtasks {
		existing[subtask.Stage] = subtask
	}

	type plannerDiff struct {
		Reason     string   `json:"reason"`
		Created    []string `json:"created"`
		Unlocked   []string `json:"unlocked"`
		Terminated []string `json:"terminated"`
	}

	diff := plannerDiff{
		Reason:     run.LastReplanReason,
		Created:    []string{},
		Unlocked:   []string{},
		Terminated: []string{},
	}

	updates := []model.TaskSubtask{}
	creates := []model.TaskSubtask{}

	ensureSubtask := func(stage string) {
		if _, ok := existing[stage]; ok {
			return
		}

		created := buildStageSubtask(run.ID, task.ID, stage, hasRoutes)
		if stage == "init" && hasRoutes {
			created.WorkerStatus = roleStatusCompleted
			created.IntegratorStatus = roleStatusCompleted
			created.ValidatorStatus = roleStatusSkipped
			created.PersistenceStatus = roleStatusReady
			created.Status = subtaskStatusReady
			created.ProvisionalCount = summarysvc.ParseRouteCount(task.OutputJSON, task.Result)
			created.ValidatedCount = created.ProvisionalCount
		}
		creates = append(creates, created)
		existing[stage] = created
		diff.Created = append(diff.Created, stage)
	}

	ensureSubtask("init")
	for _, stage := range auditStages() {
		ensureSubtask(stage)
	}

	for _, stage := range auditStages() {
		subtask := existing[stage]
		switch {
		case hasRoutes && subtask.Status == subtaskStatusBlocked && subtask.WorkerStatus == roleStatusPending:
			subtask.Status = subtaskStatusReady
			subtask.BlockedReason = ""
			subtask.WorkerStatus = roleStatusReady
			subtask.PlanRevision = run.PlannerRevision + 1
			updates = append(updates, subtask)
			diff.Unlocked = append(diff.Unlocked, stage)
		case initFailed && subtask.Status == subtaskStatusBlocked:
			subtask.Status = subtaskStatusFailed
			subtask.BlockedReason = "route inventory failed"
			subtask.ErrorMessage = "Route inventory failed; stage was not scheduled."
			subtask.WorkerStatus = roleStatusFailed
			subtask.IntegratorStatus = roleStatusSkipped
			subtask.ValidatorStatus = roleStatusSkipped
			subtask.PersistenceStatus = roleStatusSkipped
			subtask.PlanRevision = run.PlannerRevision + 1
			now := time.Now()
			subtask.CompletedAt = &now
			updates = append(updates, subtask)
			diff.Terminated = append(diff.Terminated, stage)
		}
	}

	nextRevision := run.PlannerRevision
	if len(creates) > 0 || len(updates) > 0 || nextRevision == 0 {
		nextRevision++
		now := time.Now()
		for i := range creates {
			creates[i].PlanRevision = nextRevision
			creates[i].UpdatedAt = now
		}
		for i := range updates {
			updates[i].PlanRevision = nextRevision
			updates[i].UpdatedAt = now
		}
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		for i := range creates {
			if err := tx.Create(&creates[i]).Error; err != nil {
				return err
			}
		}
		for i := range updates {
			if err := tx.Save(&updates[i]).Error; err != nil {
				return err
			}
		}
		return tx.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"planner_pending":    false,
			"planner_revision":   nextRevision,
			"last_replan_reason": "",
		}).Error
	}); err != nil {
		return err
	}

	if nextRevision > run.PlannerRevision || len(diff.Created) > 0 || len(diff.Unlocked) > 0 || len(diff.Terminated) > 0 {
		payload, _ := json.Marshal(diff)
		agentRun := m.buildAgentRun(task.ID, run.ID, "", rolePlanner, "planner", false)
		now := time.Now()
		agentRun.Status = roleStatusCompleted
		agentRun.StartedAt = &now
		agentRun.CompletedAt = &now
		agentRun.OutputJSON = payload
		_ = database.DB.Create(&agentRun).Error

		_, _ = m.emit(task.ID, run.ID, "", agentRun.ID, eventPlannerRevised, "info", "Planner revision applied.", diff)
	}

	return nil
}

func (m *Manager) dispatchRole(task *model.Task, run *model.TaskRun, subtasks []model.TaskSubtask, role string) bool {
	limit := roleParallelism(role)
	if limit <= 0 {
		return false
	}

	startAgentRun := m.startAgentRunFn
	if startAgentRun == nil {
		startAgentRun = m.startAgentRun
	}

	running := countRunningRole(subtasks, role)
	if running >= limit {
		return false
	}

	candidates := readySubtasksForRole(subtasks, role)
	if len(candidates) == 0 {
		return false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority == candidates[j].Priority {
			return stagePriority(candidates[i].Stage) < stagePriority(candidates[j].Stage)
		}
		return candidates[i].Priority < candidates[j].Priority
	})

	launched := false
	for _, subtask := range candidates {
		if running >= limit {
			break
		}
		started := startAgentRun(task.ID, run.ID, subtask.ID, role)
		if started.Err != nil {
			message := fmt.Sprintf("failed to start %s for %s: %v", role, subtask.Stage, started.Err)
			switch role {
			case roleWorker:
				m.failAgent(run, task, &subtask, started.AgentRun.ID, message, scanner.StageRunInitial)
			case roleValidator:
				m.failAgent(run, task, &subtask, started.AgentRun.ID, message, scanner.StageRunRevalidate)
			default:
				m.failPureRole(run, task, &subtask, started.AgentRun.ID, role, message)
			}
			continue
		}
		if !started.Scheduled {
			continue
		}
		launched = true
		running++

		switch role {
		case roleWorker:
			go m.executeScannerRole(run.ID, subtask.ID, started.AgentRun.ID, scanner.StageRunInitial, started.Resume)
		case roleValidator:
			go m.executeScannerRole(run.ID, subtask.ID, started.AgentRun.ID, scanner.StageRunRevalidate, started.Resume)
		case roleIntegrator:
			go m.executeIntegrator(run.ID, subtask.ID, started.AgentRun.ID)
		case rolePersistence:
			go m.executePersistence(run.ID, subtask.ID, started.AgentRun.ID)
		}
	}

	return launched
}

func (m *Manager) executeScannerRole(runID, subtaskID, agentRunID string, kind scanner.StageRunKind, resume bool) {
	role := roleWorker
	if kind == scanner.StageRunRevalidate {
		role = roleValidator
	}

	ctx, ok := m.prepareRoleExecution(runID, subtaskID, agentRunID, role, resume, func(execCtx *roleExecutionContext) error {
		return scanner.EnsureBootstrapAnchor(execCtx.Task, execCtx.Subtask.Stage)
	})
	if !ok {
		return
	}

	run, task, subtask := ctx.Run, ctx.Task, ctx.Subtask
	task.BasePath = task.GetBasePath()
	options := scanner.OrchestratedExecutionOptions(subtask.Stage, kind)
	scanner.ExecuteAIScan(task, subtask.Stage, kind, resume, options)

	status, statusErr := scanner.RuntimeStatus(task, subtask.Stage)
	if statusErr != nil {
		m.failAgent(run, task, subtask, agentRunID, fmt.Sprintf("runtime status unavailable: %v", statusErr), kind)
		return
	}

	switch status {
	case "completed":
		if err := m.completeScannerAgent(run, task, subtask, agentRunID, kind); err != nil {
			m.failAgent(run, task, subtask, agentRunID, err.Error(), kind)
		}
	case "paused":
		if err := m.pauseAgent(run, task, subtask, agentRunID, kind); err != nil {
			log.Printf("warning: failed to mark agent paused: %v", err)
		}
	default:
		m.failAgent(run, task, subtask, agentRunID, "scanner execution failed", kind)
	}
}

func (m *Manager) executeIntegrator(runID, subtaskID, agentRunID string) {
	ctx, ok := m.prepareRoleExecution(runID, subtaskID, agentRunID, roleIntegrator, false, nil)
	if !ok {
		return
	}
	m.executeIntegratorBody(ctx.Run, ctx.Task, ctx.Subtask, agentRunID)
}

func (m *Manager) executeIntegratorBody(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID string) {
	var count int
	var err error
	switch subtask.Stage {
	case "init":
		count, err = m.materializeRoutes(task, run, subtask, agentRunID, false)
	default:
		count, err = m.materializeFindings(task, run, subtask, agentRunID, true)
	}
	if err != nil {
		m.failPureRole(run, task, subtask, agentRunID, roleIntegrator, err.Error())
		return
	}

	updates := map[string]any{
		"integrator_status": roleStatusCompleted,
		"updated_at":        time.Now(),
	}
	if subtask.Stage == "init" {
		updates["persistence_status"] = roleStatusReady
		updates["status"] = subtaskStatusReady
		updates["provisional_count"] = count
		updates["validated_count"] = count
	} else {
		updates["validator_status"] = roleStatusReady
		updates["status"] = subtaskStatusReady
		updates["provisional_count"] = count
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":       roleStatusCompleted,
			"completed_at": &now,
			"updated_at":   now,
			"output_json":  marshalJSON(map[string]any{"count": count}),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(updates).Error
	}); err != nil {
		m.failPureRole(run, task, subtask, agentRunID, roleIntegrator, err.Error())
		return
	}

	eventType := eventFindingsMaterialized
	if subtask.Stage == "init" {
		eventType = eventRoutesMaterialized
	}
	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventType, "info", fmt.Sprintf("Integrator materialized %d records for %s.", count, subtask.Stage), map[string]any{
		"count": count,
		"stage": subtask.Stage,
		"mode":  "provisional",
	})
	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentCompleted, "info", fmt.Sprintf("integrator completed for %s.", subtask.Stage), nil)
	_ = m.requestPlanner(run.ID, "integrator_completed")
}

func (m *Manager) executePersistence(runID, subtaskID, agentRunID string) {
	ctx, ok := m.prepareRoleExecution(runID, subtaskID, agentRunID, rolePersistence, false, nil)
	if !ok {
		return
	}
	m.executePersistenceBody(ctx.Run, ctx.Task, ctx.Subtask, agentRunID)
}

func (m *Manager) executePersistenceBody(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID string) {
	var count int
	var err error
	switch subtask.Stage {
	case "init":
		count, err = m.materializeRoutes(task, run, subtask, agentRunID, true)
	default:
		count, err = m.materializeFindings(task, run, subtask, agentRunID, false)
	}
	if err != nil {
		m.failPureRole(run, task, subtask, agentRunID, rolePersistence, err.Error())
		return
	}

	now := time.Now()
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":       roleStatusCompleted,
			"completed_at": &now,
			"updated_at":   now,
			"output_json":  marshalJSON(map[string]any{"count": count}),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(map[string]any{
			"status":              subtaskStatusCompleted,
			"persistence_status":  roleStatusCompleted,
			"validated_count":     count,
			"verification_status": effectiveSubtaskVerification(subtask.Stage, false),
			"completed_at":        &now,
			"updated_at":          now,
		}).Error
	}); err != nil {
		m.failPureRole(run, task, subtask, agentRunID, rolePersistence, err.Error())
		return
	}

	eventType := eventFindingsMaterialized
	if subtask.Stage == "init" {
		eventType = eventRoutesMaterialized
	}
	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventType, "info", fmt.Sprintf("Persistence stored %d records for %s.", count, subtask.Stage), map[string]any{
		"count": count,
		"stage": subtask.Stage,
		"mode":  "final",
	})
	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentCompleted, "info", fmt.Sprintf("persistence completed for %s.", subtask.Stage), nil)
	_ = m.requestPlanner(run.ID, "persistence_completed")
}

func (m *Manager) prepareRoleExecution(runID, subtaskID, agentRunID, role string, resume bool, preflight func(*roleExecutionContext) error) (*roleExecutionContext, bool) {
	if runningCtx, ok := m.loadRunningExecutionContext(runID, subtaskID, agentRunID, role); ok {
		return runningCtx, true
	}
	if resume {
		return nil, false
	}

	for attempt := 1; attempt <= bootstrapRetryLimit; attempt++ {
		ctx, err := m.loadBootstrapExecutionContext(runID, subtaskID, agentRunID, role)
		if err == nil && preflight != nil {
			err = preflight(ctx)
		}
		if err == nil {
			ctx, err = m.confirmRoleStarted(ctx, agentRunID, role)
			if err == nil {
				_, _ = m.emit(ctx.Task.ID, ctx.Run.ID, ctx.Subtask.ID, agentRunID, eventAgentStarted, "info", fmt.Sprintf("%s started for %s.", role, ctx.Subtask.Stage), map[string]any{
					"role":  role,
					"stage": ctx.Subtask.Stage,
				})
				return ctx, true
			}
		}
		if errors.Is(err, errAgentRunSkipped) {
			return nil, false
		}
		if attempt < bootstrapRetryLimit {
			time.Sleep(bootstrapRetryBackoff)
			continue
		}
		m.handleBootstrapFailure(runID, subtaskID, agentRunID, role, err)
	}

	return nil, false
}

func (m *Manager) loadRunningExecutionContext(runID, subtaskID, agentRunID, role string) (*roleExecutionContext, bool) {
	ctx, err := m.loadExecutionContext(runID, subtaskID)
	if err != nil || ctx.Run.Status != runStatusRunning {
		return nil, false
	}
	if subtaskRoleStatus(*ctx.Subtask, role) != roleStatusRunning {
		return nil, false
	}

	var agent model.TaskAgentRun
	if err := database.DB.Where("id = ? AND run_id = ? AND subtask_id = ? AND role = ?", agentRunID, runID, subtaskID, role).First(&agent).Error; err != nil {
		return nil, false
	}
	if agent.Status != roleStatusRunning {
		return nil, false
	}
	return ctx, true
}

func (m *Manager) loadExecutionContext(runID, subtaskID string) (*roleExecutionContext, error) {
	run, task, subtask, err := m.loadRunTaskSubtask(runID, subtaskID)
	if err != nil {
		return nil, err
	}
	if run == nil || task == nil || subtask == nil {
		return nil, errAgentRunSkipped
	}
	return &roleExecutionContext{
		Run:     run,
		Task:    task,
		Subtask: subtask,
	}, nil
}

func (m *Manager) loadBootstrapExecutionContext(runID, subtaskID, agentRunID, role string) (*roleExecutionContext, error) {
	ctx, err := m.loadExecutionContext(runID, subtaskID)
	if err != nil {
		return nil, err
	}
	if ctx.Run.Status != runStatusRunning {
		return nil, errAgentRunSkipped
	}
	if subtaskRoleStatus(*ctx.Subtask, role) != roleStatusStarting {
		return nil, errAgentRunSkipped
	}

	var agent model.TaskAgentRun
	if err := database.DB.Where("id = ? AND run_id = ? AND subtask_id = ? AND role = ?", agentRunID, runID, subtaskID, role).First(&agent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errAgentRunSkipped
		}
		return nil, err
	}
	if agent.Status != roleStatusStarting {
		return nil, errAgentRunSkipped
	}
	return ctx, nil
}

func (m *Manager) confirmRoleStarted(ctx *roleExecutionContext, agentRunID, role string) (*roleExecutionContext, error) {
	now := time.Now()
	roleField := roleStatusField(role)

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		agentResult := tx.Model(&model.TaskAgentRun{}).
			Where("id = ? AND status = ?", agentRunID, roleStatusStarting).
			Updates(map[string]any{
				"status":     roleStatusRunning,
				"started_at": &now,
				"updated_at": now,
			})
		if agentResult.Error != nil {
			return agentResult.Error
		}
		if agentResult.RowsAffected == 0 {
			return errAgentRunSkipped
		}

		updates := map[string]any{
			"status":     subtaskStatusRunning,
			roleField:    roleStatusRunning,
			"updated_at": now,
		}
		if ctx.Subtask.StartedAt == nil {
			updates["started_at"] = &now
		}
		subtaskResult := tx.Model(&model.TaskSubtask{}).
			Where("id = ? AND "+roleField+" = ?", ctx.Subtask.ID, roleStatusStarting).
			Updates(updates)
		if subtaskResult.Error != nil {
			return subtaskResult.Error
		}
		if subtaskResult.RowsAffected == 0 {
			return errAgentRunSkipped
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ctx.Subtask.Status = subtaskStatusRunning
	setSubtaskRoleStatus(ctx.Subtask, role, roleStatusRunning)
	if ctx.Subtask.StartedAt == nil {
		ctx.Subtask.StartedAt = &now
	}
	return ctx, nil
}

func (m *Manager) handleBootstrapFailure(runID, subtaskID, agentRunID, role string, cause error) {
	ctx, err := m.loadExecutionContext(runID, subtaskID)
	if err != nil {
		log.Printf("warning: failed to load bootstrap failure context for run %s subtask %s role %s: %v", runID, subtaskID, role, err)
		return
	}

	attempts, countErr := m.countRoleAttempts(runID, subtaskID, role)
	if countErr != nil {
		log.Printf("warning: failed to count bootstrap attempts for run %s subtask %s role %s: %v", runID, subtaskID, role, countErr)
		attempts = roleAttemptLimit
	}
	retryAllowed := attempts < roleAttemptLimit
	message := structuredAttemptError("bootstrap", "retry_exhausted", cause)
	now := time.Now()
	roleField := roleStatusField(role)

	_ = database.DB.Transaction(func(tx *gorm.DB) error {
		if strings.TrimSpace(agentRunID) != "" {
			if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
				"status":        roleStatusFailed,
				"error_message": message,
				"completed_at":  &now,
				"updated_at":    now,
			}).Error; err != nil {
				return err
			}
		}

		updates := map[string]any{
			"updated_at": now,
		}
		if retryAllowed {
			updates["status"] = subtaskStatusReady
			updates[roleField] = roleStatusReady
			updates["error_message"] = ""
			updates["completed_at"] = nil
		} else {
			updates["status"] = subtaskStatusFailed
			updates[roleField] = roleStatusFailed
			updates["error_message"] = message
			updates["completed_at"] = &now
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", ctx.Subtask.ID).Updates(updates).Error
	})

	_, _ = m.emit(ctx.Task.ID, ctx.Run.ID, ctx.Subtask.ID, agentRunID, eventAgentFailed, "error", fmt.Sprintf("%s failed for %s.", role, ctx.Subtask.Stage), map[string]any{
		"error":         message,
		"category":      "bootstrap",
		"reason":        "retry_exhausted",
		"attempt_count": attempts,
		"will_retry":    retryAllowed,
	})
	if !retryAllowed {
		_ = m.requestPlanner(ctx.Run.ID, role+"_failed")
	}
}

func (m *Manager) completeScannerAgent(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID string, kind scanner.StageRunKind) error {
	now := time.Now()
	output := marshalJSON(map[string]any{
		"stage": subtask.Stage,
		"kind":  string(kind),
	})

	updates := map[string]any{
		"status":     subtaskStatusReady,
		"updated_at": now,
	}

	role := roleWorker
	switch kind {
	case scanner.StageRunRevalidate:
		role = roleValidator
		updates["validator_status"] = roleStatusCompleted
		updates["persistence_status"] = roleStatusReady
	default:
		updates["worker_status"] = roleStatusCompleted
		updates["integrator_status"] = roleStatusReady
		if subtask.Stage == "init" {
			updates["validator_status"] = roleStatusSkipped
		}
	}

	if subtask.StartedAt == nil {
		updates["started_at"] = &now
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":       roleStatusCompleted,
			"completed_at": &now,
			"updated_at":   now,
			"output_json":  output,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(updates).Error
	}); err != nil {
		return err
	}

	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentCompleted, "info", fmt.Sprintf("%s completed for %s.", role, subtask.Stage), map[string]any{
		"role": role,
		"kind": string(kind),
	})
	_ = m.requestPlanner(run.ID, role+"_completed")
	return nil
}

func (m *Manager) pauseAgent(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID string, kind scanner.StageRunKind) error {
	role := roleWorker
	roleStatusField := "worker_status"
	if kind == scanner.StageRunRevalidate {
		role = roleValidator
		roleStatusField = "validator_status"
	}

	now := time.Now()
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":     roleStatusPaused,
			"updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(map[string]any{
			"status":        subtaskStatusPaused,
			roleStatusField: roleStatusPaused,
			"updated_at":    now,
		}).Error
	}); err != nil {
		return err
	}

	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentPaused, "info", fmt.Sprintf("%s paused for %s.", role, subtask.Stage), nil)
	return nil
}

func (m *Manager) failAgent(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID string, message string, kind scanner.StageRunKind) {
	role := roleWorker
	roleStatusField := "worker_status"
	if kind == scanner.StageRunRevalidate {
		role = roleValidator
		roleStatusField = "validator_status"
	}

	now := time.Now()
	_ = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":        roleStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(map[string]any{
			"status":        subtaskStatusFailed,
			roleStatusField: roleStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error
	})

	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentFailed, "error", fmt.Sprintf("%s failed for %s.", role, subtask.Stage), map[string]any{
		"error": message,
	})
	_ = m.requestPlanner(run.ID, role+"_failed")
}

func (m *Manager) failPureRole(run *model.TaskRun, task *model.Task, subtask *model.TaskSubtask, agentRunID, role, message string) {
	roleStatusField := role + "_status"
	now := time.Now()
	_ = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agentRunID).Updates(map[string]any{
			"status":        roleStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(map[string]any{
			"status":        subtaskStatusFailed,
			roleStatusField: roleStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error
	})

	_, _ = m.emit(task.ID, run.ID, subtask.ID, agentRunID, eventAgentFailed, "error", fmt.Sprintf("%s failed for %s.", role, subtask.Stage), map[string]any{
		"error": message,
	})
	_ = m.requestPlanner(run.ID, role+"_failed")
}

func (m *Manager) finalizeRunIfTerminal(task *model.Task, run *model.TaskRun, subtasks []model.TaskSubtask) bool {
	if len(subtasks) == 0 {
		return false
	}

	for _, subtask := range subtasks {
		if subtask.Status == subtaskStatusBlocked || subtask.Status == subtaskStatusReady || subtask.Status == subtaskStatusRunning || subtask.Status == subtaskStatusPaused || hasRoleState(subtask, roleStatusStarting) {
			return false
		}
	}

	failedCount := countSubtasksByStatus(subtasks, subtaskStatusFailed)
	now := time.Now()
	runStatus := runStatusCompleted
	taskStatus := "completed"
	eventType := eventRunCompleted
	message := "Orchestration run completed."
	if failedCount > 0 {
		runStatus = runStatusFailed
		taskStatus = "failed"
		eventType = eventRunFailed
		message = "Orchestration run finished with failures."
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status":       runStatus,
			"completed_at": &now,
			"updated_at":   now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", taskStatus).Error
	}); err != nil {
		log.Printf("warning: failed to finalize run %s: %v", run.ID, err)
		return false
	}

	_, _ = m.emit(task.ID, run.ID, "", "", eventType, "info", message, map[string]any{
		"failed_count": failedCount,
	})
	return true
}

func (m *Manager) failRun(task *model.Task, run *model.TaskRun, message string) {
	now := time.Now()
	_ = database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskRun{}).Where("id = ?", run.ID).Updates(map[string]any{
			"status":        runStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.Task{}).Where("id = ?", task.ID).Update("status", "failed").Error
	})
	_, _ = m.emit(task.ID, run.ID, "", "", eventRunFailed, "error", message, nil)
}

func (m *Manager) repairRunOrphans(runID string, now time.Time) (int, error) {
	run, task, subtasks, err := m.loadRunContext(runID)
	if err != nil || run == nil || task == nil {
		return 0, err
	}
	if run.Status != runStatusRunning {
		return 0, nil
	}
	return m.repairOrphanAttempts(task, run, subtasks, now)
}

func (m *Manager) repairOrphanAttempts(task *model.Task, run *model.TaskRun, subtasks []model.TaskSubtask, now time.Time) (int, error) {
	agents, err := m.loadAgents(run.ID)
	if err != nil {
		return 0, err
	}

	latestAgents := latestAgentRunsByKey(agents)
	repaired := 0
	for _, subtask := range subtasks {
		for _, role := range []string{roleWorker, roleIntegrator, roleValidator, rolePersistence} {
			status := subtaskRoleStatus(subtask, role)
			key := agentAttemptKey(subtask.ID, role)
			agent, ok := latestAgents[key]
			if !ok {
				continue
			}

			switch status {
			case roleStatusStarting:
				if agent.Status != roleStatusStarting || now.Sub(agent.CreatedAt) < bootstrapGracePeriod {
					continue
				}
				message := structuredAttemptError("orphan", "bootstrap_grace_exceeded", fmt.Errorf("starting exceeded %s grace period", bootstrapGracePeriod))
				didRepair, repairErr := m.repairOrphanAttempt(task, run, &subtask, role, agent, message, "orphan", "bootstrap_grace_exceeded")
				if repairErr != nil {
					return repaired, repairErr
				}
				if didRepair {
					repaired++
				}
			case roleStatusRunning:
				if !m.isLegacyRunningOrphan(task, subtask, role, agent) {
					continue
				}
				message := structuredAttemptError("orphan", "legacy_running_without_anchor", errors.New("running attempt has no startup anchor"))
				didRepair, repairErr := m.repairOrphanAttempt(task, run, &subtask, role, agent, message, "orphan", "legacy_running_without_anchor")
				if repairErr != nil {
					return repaired, repairErr
				}
				if didRepair {
					repaired++
				}
			}
		}
	}

	return repaired, nil
}

func (m *Manager) repairOrphanAttempt(task *model.Task, run *model.TaskRun, subtask *model.TaskSubtask, role string, agent model.TaskAgentRun, message, category, reason string) (bool, error) {
	attempts, err := m.countRoleAttempts(run.ID, subtask.ID, role)
	if err != nil {
		return false, err
	}
	retryAllowed := attempts < roleAttemptLimit
	now := time.Now()
	roleField := roleStatusField(role)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.TaskAgentRun{}).Where("id = ?", agent.ID).Updates(map[string]any{
			"status":        roleStatusFailed,
			"error_message": message,
			"completed_at":  &now,
			"updated_at":    now,
		}).Error; err != nil {
			return err
		}

		updates := map[string]any{
			"updated_at": now,
		}
		if retryAllowed {
			updates["status"] = subtaskStatusReady
			updates[roleField] = roleStatusReady
			updates["error_message"] = ""
			updates["completed_at"] = nil
		} else {
			updates["status"] = subtaskStatusFailed
			updates[roleField] = roleStatusFailed
			updates["error_message"] = message
			updates["completed_at"] = &now
		}
		return tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID).Updates(updates).Error
	}); err != nil {
		return false, err
	}

	_, _ = m.emit(task.ID, run.ID, subtask.ID, agent.ID, eventAgentFailed, "error", fmt.Sprintf("%s failed for %s.", role, subtask.Stage), map[string]any{
		"error":         message,
		"category":      category,
		"reason":        reason,
		"attempt_count": attempts,
		"will_retry":    retryAllowed,
	})
	if !retryAllowed {
		_ = m.requestPlanner(run.ID, role+"_failed")
	}
	return true, nil
}

func (m *Manager) isLegacyRunningOrphan(task *model.Task, subtask model.TaskSubtask, role string, agent model.TaskAgentRun) bool {
	if agent.Status != roleStatusRunning || !agent.UpdatedAt.Equal(agent.CreatedAt) {
		return false
	}

	switch role {
	case roleWorker, roleValidator:
		if scanner.HasRuntimeBootstrapAnchor(task, subtask.Stage) {
			return false
		}
		var stageCount int64
		if err := database.DB.Model(&model.TaskStage{}).Where("task_id = ? AND name = ?", task.ID, subtask.Stage).Count(&stageCount).Error; err != nil {
			return false
		}
		return stageCount == 0
	case roleIntegrator, rolePersistence:
		if agent.CompletedAt != nil || strings.TrimSpace(agent.ErrorMessage) != "" || agentOutputWritten(agent) {
			return false
		}
		return true
	default:
		return false
	}
}

func (m *Manager) countRoleAttempts(runID, subtaskID, role string) (int64, error) {
	var count int64
	err := database.DB.Model(&model.TaskAgentRun{}).Where("run_id = ? AND subtask_id = ? AND role = ?", runID, subtaskID, role).Count(&count).Error
	return count, err
}

func (m *Manager) startAgentRun(taskID, runID, subtaskID, role string) agentRunStartResult {
	var created model.TaskAgentRun
	resume := false

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		var subtask model.TaskSubtask
		if err := tx.First(&subtask, "id = ?", subtaskID).Error; err != nil {
			return err
		}

		updateQuery := tx.Model(&model.TaskSubtask{}).Where("id = ?", subtask.ID)
		updates := map[string]any{
			"updated_at": time.Now(),
		}
		roleField := roleStatusField(role)
		switch role {
		case roleWorker:
			resume = subtask.WorkerStatus == roleStatusPaused
		case roleIntegrator:
			if subtask.IntegratorStatus != roleStatusReady {
				return errAgentRunSkipped
			}
		case roleValidator:
			resume = subtask.ValidatorStatus == roleStatusPaused
		case rolePersistence:
			if subtask.PersistenceStatus != roleStatusReady {
				return errAgentRunSkipped
			}
		default:
			return fmt.Errorf("unsupported role %q", role)
		}

		if role == roleWorker {
			if subtask.WorkerStatus != roleStatusReady && subtask.WorkerStatus != roleStatusPaused {
				return errAgentRunSkipped
			}
			updateQuery = updateQuery.Where("worker_status = ?", subtask.WorkerStatus)
		}
		if role == roleValidator {
			if subtask.ValidatorStatus != roleStatusReady && subtask.ValidatorStatus != roleStatusPaused {
				return errAgentRunSkipped
			}
			updateQuery = updateQuery.Where("validator_status = ?", subtask.ValidatorStatus)
		}
		if role == roleIntegrator {
			updateQuery = updateQuery.Where("integrator_status = ?", subtask.IntegratorStatus)
		}
		if role == rolePersistence {
			updateQuery = updateQuery.Where("persistence_status = ?", subtask.PersistenceStatus)
		}

		if resume {
			updates["status"] = subtaskStatusRunning
			updates[roleField] = roleStatusRunning
			result := updateQuery.Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return errAgentRunSkipped
			}

			if err := tx.Where("task_id = ? AND run_id = ? AND subtask_id = ? AND role = ? AND status = ?", taskID, runID, subtaskID, role, roleStatusPaused).Order("updated_at desc").First(&created).Error; err != nil {
				return err
			}
			return tx.Model(&model.TaskAgentRun{}).Where("id = ?", created.ID).Updates(map[string]any{
				"status":       roleStatusRunning,
				"resume_count": created.ResumeCount + 1,
				"updated_at":   time.Now(),
			}).Error
		}

		updates[roleField] = roleStatusStarting
		result := updateQuery.Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAgentRunSkipped
		}

		created = m.buildAgentRun(taskID, runID, subtaskID, role, subtask.Stage, role == roleWorker || role == roleValidator)
		created.Status = roleStatusStarting
		return tx.Create(&created).Error
	})

	if err != nil {
		if errors.Is(err, errAgentRunSkipped) {
			return agentRunStartResult{}
		}
		return agentRunStartResult{
			AgentRun: created,
			Resume:   resume,
			Err:      err,
		}
	}
	return agentRunStartResult{
		AgentRun:  created,
		Resume:    resume,
		Scheduled: true,
	}
}

func (m *Manager) buildAgentRun(taskID, runID, subtaskID, role, stage string, attachSession bool) model.TaskAgentRun {
	roleCfg := roleConfig(role)
	agentRun := model.TaskAgentRun{
		ID:        utils.NewOpaqueID(),
		TaskID:    taskID,
		RunID:     runID,
		SubtaskID: subtaskID,
		Role:      role,
		Stage:     stage,
		Model:     roleCfg.Model,
	}
	if attachSession {
		task := model.Task{ID: taskID}
		agentRun.SessionPath = task.StageRuntimePath(stage)
	}
	return agentRun
}

func buildEvent(taskID, runID, subtaskID, agentRunID, eventType, level, message string, payload any) model.TaskEvent {
	return model.TaskEvent{
		ID:          utils.NewOpaqueID(),
		TaskID:      taskID,
		RunID:       runID,
		SubtaskID:   subtaskID,
		AgentRunID:  agentRunID,
		EventType:   eventType,
		Level:       level,
		Message:     message,
		PayloadJSON: marshalJSON(payload),
		CreatedAt:   time.Now(),
	}
}

func (m *Manager) materializeRoutes(task *model.Task, run *model.TaskRun, subtask *model.TaskSubtask, agentRunID string, final bool) (int, error) {
	items, _, ok := summarysvc.ParseFindings(task.OutputJSON, task.Result)
	if !ok || items == nil {
		return 0, nil
	}

	records := make([]model.TaskRoute, 0, len(items))
	now := time.Now()
	for _, item := range items {
		method := strings.ToUpper(summarysvc.ExtractString(item["method"]))
		path := summarysvc.ExtractString(item["path"])
		source := firstNonEmpty(
			summarysvc.ExtractString(item["source"]),
			summarysvc.ExtractString(item["source_file"]),
			summarysvc.ExtractString(item["file"]),
		)
		handler := firstNonEmpty(
			summarysvc.ExtractString(item["handler"]),
			summarysvc.ExtractString(item["function"]),
		)
		record := model.TaskRoute{
			ID:                 hashKey("route", task.ID, subtask.Stage, method, path, source, handler),
			TaskID:             task.ID,
			RunID:              run.ID,
			SubtaskID:          subtask.ID,
			PlanRevision:       run.PlannerRevision,
			AgentRunID:         agentRunID,
			OriginStage:        subtask.Stage,
			Method:             method,
			Path:               path,
			Handler:            handler,
			SourceFile:         source,
			VerificationStatus: ternary(final, "confirmed", "unverified"),
			ReviewedSeverity:   "",
			VerificationReason: ternaryString(final, "Route inventory persisted from task projection.", ""),
			EvidenceRefs:       extractEvidenceJSON(item),
			PayloadJSON:        marshalJSON(item),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		records = append(records, record)
	}

	if err := replaceRoutesForSubtask(subtask.ID, records); err != nil {
		return 0, err
	}
	return len(records), nil
}

func (m *Manager) materializeFindings(task *model.Task, run *model.TaskRun, subtask *model.TaskSubtask, agentRunID string, provisional bool) (int, error) {
	var stage model.TaskStage
	if err := database.DB.Where("task_id = ? AND name = ?", task.ID, subtask.Stage).First(&stage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, err
	}

	items, _, ok := summarysvc.ParseFindings(stage.OutputJSON, stage.Result)
	if !ok || items == nil {
		return 0, nil
	}

	now := time.Now()
	records := make([]model.TaskFinding, 0, len(items))
	for _, item := range items {
		verificationStatus := "unverified"
		verificationReason := ""
		reviewedSeverity := ""
		if !provisional {
			verificationStatus = normalizeVerificationStatus(summarysvc.ExtractString(item["verification_status"]))
			verificationReason = summarysvc.ExtractString(item["verification_reason"])
			if reviewed := summarysvc.ExtractString(item["reviewed_severity"]); strings.TrimSpace(reviewed) != "" {
				reviewedSeverity = summarysvc.NormalizeSeverity(reviewed)
			}
		}
		record := model.TaskFinding{
			ID:                 findingID(task.ID, subtask.Stage, item),
			TaskID:             task.ID,
			RunID:              run.ID,
			SubtaskID:          subtask.ID,
			PlanRevision:       run.PlannerRevision,
			AgentRunID:         agentRunID,
			OriginStage:        subtask.Stage,
			Type:               summarysvc.ExtractString(item["type"]),
			Subtype:            summarysvc.ExtractString(item["subtype"]),
			Severity:           summarysvc.NormalizeSeverity(summarysvc.ExtractString(item["severity"])),
			VerificationStatus: verificationStatus,
			ReviewedSeverity:   reviewedSeverity,
			VerificationReason: verificationReason,
			EvidenceRefs:       extractEvidenceJSON(item),
			PayloadJSON:        withInjectedMetadata(item, provisional),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		records = append(records, record)
	}

	if err := replaceFindingsForSubtask(subtask.ID, records); err != nil {
		return 0, err
	}
	return len(records), nil
}

func (m *Manager) requestPlanner(runID, reason string) error {
	return database.DB.Model(&model.TaskRun{}).Where("id = ? AND status = ?", runID, runStatusRunning).Updates(map[string]any{
		"planner_pending":    true,
		"last_replan_reason": reason,
		"updated_at":         time.Now(),
	}).Error
}

func (m *Manager) emit(taskID, runID, subtaskID, agentRunID, eventType, level, message string, payload any) (model.TaskEvent, error) {
	event := buildEvent(taskID, runID, subtaskID, agentRunID, eventType, level, message, payload)

	if err := database.DB.Create(&event).Error; err != nil {
		return model.TaskEvent{}, err
	}
	m.hub.publish(event)
	return event, nil
}

func (m *Manager) activeRun(taskID string) (*model.TaskRun, error) {
	var run model.TaskRun
	err := database.DB.Where("task_id = ? AND status IN ?", taskID, []string{runStatusRunning, runStatusPaused}).Order("created_at desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (m *Manager) latestRun(taskID string) (*model.TaskRun, error) {
	var run model.TaskRun
	err := database.DB.Where("task_id = ?", taskID).Order("created_at desc").First(&run).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (m *Manager) loadRunTask(runID string) (*model.TaskRun, *model.Task, error) {
	var run model.TaskRun
	if err := database.DB.First(&run, "id = ?", runID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var task model.Task
	if err := database.DB.First(&task, "id = ?", run.TaskID).Error; err != nil {
		return &run, nil, err
	}

	return &run, &task, nil
}

func (m *Manager) loadRunContext(runID string) (*model.TaskRun, *model.Task, []model.TaskSubtask, error) {
	run, task, err := m.loadRunTask(runID)
	if err != nil || run == nil {
		return run, task, nil, err
	}
	subtasks, err := m.loadSubtasks(run.ID)
	return run, task, subtasks, err
}

func (m *Manager) loadRunTaskSubtask(runID, subtaskID string) (*model.TaskRun, *model.Task, *model.TaskSubtask, error) {
	run, task, _, err := m.loadRunContext(runID)
	if err != nil {
		return nil, nil, nil, err
	}
	var subtask model.TaskSubtask
	if err := database.DB.First(&subtask, "id = ?", subtaskID).Error; err != nil {
		return nil, nil, nil, err
	}
	return run, task, &subtask, nil
}

func (m *Manager) loadSubtasks(runID string) ([]model.TaskSubtask, error) {
	var subtasks []model.TaskSubtask
	if err := database.DB.Where("run_id = ?", runID).Order("priority asc, created_at asc").Find(&subtasks).Error; err != nil {
		return nil, err
	}
	return subtasks, nil
}

func (m *Manager) loadAgents(runID string) ([]model.TaskAgentRun, error) {
	var agents []model.TaskAgentRun
	if err := database.DB.Where("run_id = ?", runID).Order("created_at asc").Find(&agents).Error; err != nil {
		return nil, err
	}
	return agents, nil
}

func (m *Manager) routesAvailable(task *model.Task, runID string) bool {
	if summarysvc.ParseRouteCount(task.OutputJSON, task.Result) > 0 {
		return true
	}

	var count int64
	if err := database.DB.Model(&model.TaskRoute{}).Where("task_id = ? AND run_id = ?", task.ID, runID).Count(&count).Error; err != nil {
		return false
	}
	return count > 0
}

func buildInitialSubtasks(task model.Task, runID string) []model.TaskSubtask {
	hasRoutes := summarysvc.ParseRouteCount(task.OutputJSON, task.Result) > 0
	subtasks := make([]model.TaskSubtask, 0, 1+len(auditStages()))
	subtasks = append(subtasks, buildStageSubtask(runID, task.ID, "init", hasRoutes))
	for _, stage := range auditStages() {
		subtasks = append(subtasks, buildStageSubtask(runID, task.ID, stage, hasRoutes))
	}
	return subtasks
}

func buildStageSubtask(runID, taskID, stage string, routesReady bool) model.TaskSubtask {
	now := time.Now()
	subtask := model.TaskSubtask{
		ID:                utils.NewOpaqueID(),
		TaskID:            taskID,
		RunID:             runID,
		Stage:             stage,
		Title:             stageLabel(stage),
		Priority:          stagePriority(stage),
		Status:            subtaskStatusReady,
		WorkerStatus:      roleStatusReady,
		IntegratorStatus:  roleStatusPending,
		ValidatorStatus:   roleStatusPending,
		PersistenceStatus: roleStatusPending,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if stage == "init" {
		subtask.ValidatorStatus = roleStatusSkipped
		return subtask
	}

	if !routesReady {
		subtask.Status = subtaskStatusBlocked
		subtask.WorkerStatus = roleStatusPending
		subtask.BlockedReason = "waiting for route inventory"
	}

	return subtask
}

func readySubtasksForRole(subtasks []model.TaskSubtask, role string) []model.TaskSubtask {
	result := []model.TaskSubtask{}
	for _, subtask := range subtasks {
		switch role {
		case roleWorker:
			if subtask.WorkerStatus == roleStatusReady || subtask.WorkerStatus == roleStatusPaused {
				result = append(result, subtask)
			}
		case roleIntegrator:
			if subtask.IntegratorStatus == roleStatusReady {
				result = append(result, subtask)
			}
		case roleValidator:
			if subtask.ValidatorStatus == roleStatusReady || subtask.ValidatorStatus == roleStatusPaused {
				result = append(result, subtask)
			}
		case rolePersistence:
			if subtask.PersistenceStatus == roleStatusReady {
				result = append(result, subtask)
			}
		}
	}
	return result
}

func roleStatusField(role string) string {
	return role + "_status"
}

func subtaskRoleStatus(subtask model.TaskSubtask, role string) string {
	switch role {
	case roleWorker:
		return subtask.WorkerStatus
	case roleIntegrator:
		return subtask.IntegratorStatus
	case roleValidator:
		return subtask.ValidatorStatus
	case rolePersistence:
		return subtask.PersistenceStatus
	default:
		return ""
	}
}

func setSubtaskRoleStatus(subtask *model.TaskSubtask, role, status string) {
	if subtask == nil {
		return
	}
	switch role {
	case roleWorker:
		subtask.WorkerStatus = status
	case roleIntegrator:
		subtask.IntegratorStatus = status
	case roleValidator:
		subtask.ValidatorStatus = status
	case rolePersistence:
		subtask.PersistenceStatus = status
	}
}

func latestAgentRunsByKey(agents []model.TaskAgentRun) map[string]model.TaskAgentRun {
	latest := make(map[string]model.TaskAgentRun, len(agents))
	for _, agent := range agents {
		key := agentAttemptKey(agent.SubtaskID, agent.Role)
		current, exists := latest[key]
		if !exists || agent.CreatedAt.After(current.CreatedAt) || (agent.CreatedAt.Equal(current.CreatedAt) && agent.UpdatedAt.After(current.UpdatedAt)) {
			latest[key] = agent
		}
	}
	return latest
}

func agentAttemptKey(subtaskID, role string) string {
	return subtaskID + ":" + role
}

func agentOutputWritten(agent model.TaskAgentRun) bool {
	trimmed := strings.TrimSpace(string(agent.OutputJSON))
	return trimmed != "" && trimmed != "null"
}

func structuredAttemptError(category, reason string, cause error) string {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	payload := map[string]any{
		"category": category,
		"reason":   reason,
	}
	if strings.TrimSpace(detail) != "" {
		payload["detail"] = detail
	}
	return string(marshalJSON(payload))
}

func countActiveSubtasks(subtasks []model.TaskSubtask) int {
	count := 0
	for _, subtask := range subtasks {
		if subtask.Status == subtaskStatusReady || subtask.Status == subtaskStatusRunning || subtask.Status == subtaskStatusPaused {
			count++
		}
	}
	return count
}

func countSubtasksByStatus(subtasks []model.TaskSubtask, status string) int {
	count := 0
	for _, subtask := range subtasks {
		if subtask.Status == status {
			count++
		}
	}
	return count
}

func buildStageProgress(subtasks []model.TaskSubtask) []StageProgress {
	order := append([]string{"init"}, auditStages()...)
	progressByStage := map[string]*StageProgress{}
	for _, stage := range order {
		progressByStage[stage] = &StageProgress{
			Stage: stage,
			Label: stageLabel(stage),
		}
	}

	for _, subtask := range subtasks {
		stageProgress := progressByStage[subtask.Stage]
		stageProgress.SubtaskCount++
		stageProgress.ProvisionalCount += subtask.ProvisionalCount
		stageProgress.ValidatedCount += subtask.ValidatedCount
		switch subtask.Status {
		case subtaskStatusCompleted:
			stageProgress.CompletedCount++
			stageProgress.Status = subtaskStatusCompleted
		case subtaskStatusFailed:
			stageProgress.FailedCount++
			stageProgress.Status = subtaskStatusFailed
		case subtaskStatusRunning:
			stageProgress.RunningCount++
			if stageProgress.Status != subtaskStatusFailed {
				stageProgress.Status = subtaskStatusRunning
			}
		case subtaskStatusReady, subtaskStatusPaused:
			if stageProgress.Status == "" {
				stageProgress.Status = subtask.Status
			}
		case subtaskStatusBlocked:
			if stageProgress.Status == "" {
				stageProgress.Status = subtaskStatusBlocked
			}
		}
	}

	out := make([]StageProgress, 0, len(order))
	for _, stage := range order {
		item := progressByStage[stage]
		if item.Status == "" {
			item.Status = subtaskStatusBlocked
		}
		out = append(out, *item)
	}
	return out
}

func hasReadyRole(subtasks []model.TaskSubtask) bool {
	for _, subtask := range subtasks {
		if subtask.WorkerStatus == roleStatusReady || subtask.IntegratorStatus == roleStatusReady || subtask.ValidatorStatus == roleStatusReady || subtask.PersistenceStatus == roleStatusReady {
			return true
		}
	}
	return false
}

func hasRunningRole(subtasks []model.TaskSubtask) bool {
	for _, subtask := range subtasks {
		if subtask.WorkerStatus == roleStatusStarting || subtask.WorkerStatus == roleStatusRunning ||
			subtask.IntegratorStatus == roleStatusStarting || subtask.IntegratorStatus == roleStatusRunning ||
			subtask.ValidatorStatus == roleStatusStarting || subtask.ValidatorStatus == roleStatusRunning ||
			subtask.PersistenceStatus == roleStatusStarting || subtask.PersistenceStatus == roleStatusRunning {
			return true
		}
	}
	return false
}

func countRunningRole(subtasks []model.TaskSubtask, role string) int {
	count := 0
	for _, subtask := range subtasks {
		switch role {
		case roleWorker:
			if subtask.WorkerStatus == roleStatusStarting || subtask.WorkerStatus == roleStatusRunning {
				count++
			}
		case roleIntegrator:
			if subtask.IntegratorStatus == roleStatusStarting || subtask.IntegratorStatus == roleStatusRunning {
				count++
			}
		case roleValidator:
			if subtask.ValidatorStatus == roleStatusStarting || subtask.ValidatorStatus == roleStatusRunning {
				count++
			}
		case rolePersistence:
			if subtask.PersistenceStatus == roleStatusStarting || subtask.PersistenceStatus == roleStatusRunning {
				count++
			}
		}
	}
	return count
}

type roleRuntimeConfig struct {
	Model       string
	Parallelism int
}

func roleConfig(role string) roleRuntimeConfig {
	cfg := roleRuntimeConfig{
		Model:       config.AI.Model,
		Parallelism: config.Orchestration.Worker.Parallelism,
	}
	switch role {
	case rolePlanner:
		cfg.Parallelism = config.Orchestration.Planner.Parallelism
		cfg.Model = config.AI.Model
	case roleWorker:
		cfg.Model = config.Orchestration.Worker.Model
		cfg.Parallelism = config.Orchestration.Worker.Parallelism
	case roleIntegrator:
		cfg.Parallelism = config.Orchestration.Integrator.Parallelism
		cfg.Model = config.AI.Model
	case roleValidator:
		cfg.Model = config.Orchestration.Validator.Model
		cfg.Parallelism = config.Orchestration.Validator.Parallelism
	case rolePersistence:
		cfg.Parallelism = config.Orchestration.Persistence.Parallelism
		cfg.Model = config.AI.Model
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = config.AI.Model
	}
	return cfg
}

func roleParallelism(role string) int {
	return roleConfig(role).Parallelism
}

func stagePriority(stage string) int {
	switch stage {
	case "init":
		return 0
	case "rce":
		return 10
	case "injection":
		return 20
	case "auth":
		return 30
	case "access":
		return 40
	case "xss":
		return 50
	case "config":
		return 60
	case "fileop":
		return 70
	case "logic":
		return 80
	default:
		return 999
	}
}

func stageLabel(stage string) string {
	if stage == "init" {
		return "Route Inventory"
	}
	if label := summarysvc.StageLabel(stage); label != "" {
		return label
	}
	return stage
}

func replaceRoutesForSubtask(subtaskID string, records []model.TaskRoute) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("subtask_id = ?", subtaskID).Delete(&model.TaskRoute{}).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(&records).Error
	})
}

func replaceFindingsForSubtask(subtaskID string, records []model.TaskFinding) error {
	return database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("subtask_id = ?", subtaskID).Delete(&model.TaskFinding{}).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(&records).Error
	})
}

func findingID(taskID, stage string, item map[string]any) string {
	location, _ := item["location"].(map[string]any)
	trigger, _ := item["trigger"].(map[string]any)
	return hashKey(
		"finding",
		taskID,
		stage,
		summarysvc.ExtractString(item["type"]),
		summarysvc.ExtractString(item["subtype"]),
		summarysvc.ExtractString(item["description"]),
		summarysvc.ExtractString(trigger["method"]),
		summarysvc.ExtractString(trigger["path"]),
		summarysvc.ExtractString(location["file"]),
		summarysvc.ExtractString(location["line"]),
	)
}

func normalizeVerificationStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "confirmed":
		return "confirmed"
	case "uncertain":
		return "uncertain"
	case "rejected":
		return "rejected"
	default:
		return "unverified"
	}
}

func effectiveSubtaskVerification(stage string, provisional bool) string {
	if stage == "init" {
		return "confirmed"
	}
	if provisional {
		return "unverified"
	}
	return "reviewed"
}

func extractEvidenceJSON(item map[string]any) json.RawMessage {
	if refs, ok := item["evidence_refs"]; ok {
		return marshalJSON(refs)
	}
	return marshalJSON([]any{})
}

func withInjectedMetadata(item map[string]any, provisional bool) json.RawMessage {
	clone := map[string]any{}
	for key, value := range item {
		clone[key] = value
	}
	if provisional {
		clone["verification_status"] = "unverified"
	}
	return marshalJSON(clone)
}

func marshalJSON(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage([]byte("null"))
	}
	data, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage([]byte("null"))
	}
	return json.RawMessage(data)
}

func hashKey(parts ...string) string {
	h := sha1.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func extractSubtaskIDs(subtasks []model.TaskSubtask) []string {
	out := make([]string, 0, len(subtasks))
	for _, subtask := range subtasks {
		out = append(out, subtask.ID)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func ternaryString(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
