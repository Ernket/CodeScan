import test from 'node:test'
import assert from 'node:assert/strict'

import {
  buildTaskOrchestrationPreview,
  buildActivityFeed,
  buildExecutionQueues,
  buildInitStageCard,
  buildOrchestrationFlow,
  buildStageMatrixRows,
  isTaskMainView,
  parseRunScope,
  pickDefaultRerunStages,
  pickDefaultStage,
  resolveSubtaskDisplayStatus,
} from './orchestration.js'

const translations = {
  'stage.init.label': 'Route Inventory',
  'stage.rce.label': 'RCE Audit',
  'stage.rce.shortLabel': 'RCE',
  'stage.injection.label': 'Injection Audit',
  'stage.injection.shortLabel': 'Injection',
  'stage.auth.label': 'Auth Audit',
  'stage.auth.shortLabel': 'Auth',
  'stage.access.label': 'Access Audit',
  'stage.access.shortLabel': 'Access',
  'stage.xss.label': 'XSS Audit',
  'stage.xss.shortLabel': 'XSS',
  'stage.config.label': 'Config Audit',
  'stage.config.shortLabel': 'Config',
  'stage.fileop.label': 'FileOp Audit',
  'stage.fileop.shortLabel': 'FileOp',
  'stage.logic.label': 'Logic Audit',
  'stage.logic.shortLabel': 'Logic',
  'orchestration.roles.worker': 'Worker',
  'orchestration.roles.integrator': 'Integrator',
  'orchestration.roles.validator': 'Validator',
  'orchestration.roles.persistence': 'Persistence',
  'orchestration.roles.planner': 'Planner',
  'orchestration.replan.integrator_completed': 'integrator completed',
  'orchestration.replan.unknown': 'unknown',
  'orchestration.materialized.provisional': 'provisional result',
  'orchestration.materialized.final': 'final result',
  'orchestration.activities.runStarted': 'Full orchestration started',
  'orchestration.activities.runStartedDetail': '{count} stage subtasks were created',
  'orchestration.activities.plannerRevised': 'Planner revision applied',
  'orchestration.activities.plannerRevisedDetail': 'Reason: {reason}, created {created}, unlocked {unlocked}, terminated {terminated}',
  'orchestration.activities.agentStarted': '{role} started {stage}',
  'orchestration.activities.agentCompleted': '{role} completed {stage}',
  'orchestration.activities.agentPaused': '{role} paused {stage}',
  'orchestration.activities.agentFailed': '{role} failed on {stage}',
  'orchestration.activities.routesMaterialized': '{stage} materialized {count} routes',
  'orchestration.activities.findingsMaterialized': '{stage} materialized {count} findings',
  'orchestration.activities.materializedMode': '{mode}',
  'orchestration.activities.subtaskStatusChanged': '{stage} changed status to {status}',
  'orchestration.activities.subtaskBlocked': '{stage} updated its blocked reason',
  'orchestration.activities.subtaskError': '{stage} recorded a new error',
  'orchestration.activities.generic': '{eventType}',
  'orchestration.state.starting': 'Starting',
  'orchestration.state.running': 'Running',
  'orchestration.state.blocked': 'Blocked',
  'orchestration.state.stalled': 'Stalled',
  'orchestration.state.completed': 'Completed',
  'orchestration.state.failed': 'Failed',
  'orchestration.state.paused': 'Paused',
  'orchestration.state.waiting': 'Waiting',
  'orchestration.state.not_started': 'Not Started',
  'orchestration.queue.plannerPending': 'Planner pending',
}

function t(key, params = {}) {
  const template = translations[key] ?? key
  return template.replace(/\{(\w+)\}/g, (_, name) => {
    return params[name] === undefined || params[name] === null ? '' : String(params[name])
  })
}

test('pickDefaultStage prioritizes blocked before starting, running, and failed', () => {
  const snapshot = {
    subtasks: [
      { id: 'init-1', stage: 'init', title: 'Route Inventory', priority: 0, status: 'completed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'skipped', persistence_status: 'completed' },
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'ready', worker_status: 'starting', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'auth-1', stage: 'auth', title: 'Auth', priority: 30, status: 'failed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'failed', persistence_status: 'pending' },
      { id: 'xss-1', stage: 'xss', title: 'XSS', priority: 50, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for review handoff', started_at: '2026-04-21T12:00:00Z' },
    ],
    run: { stage_progress: [] },
  }

  const focusEntries = [
    buildInitStageCard(snapshot, { current_stage: 'rce', focus_status: 'starting', stalled: false }, t),
    ...buildStageMatrixRows(snapshot, { current_stage: 'rce', focus_status: 'starting', stalled: false }, t),
  ]
  assert.equal(pickDefaultStage(focusEntries), 'xss')
})

test('buildStageMatrixRows marks waiting-for-route-inventory as waiting and starting stage as starting', () => {
  const snapshot = {
    subtasks: [
      { id: 'init-1', stage: 'init', title: 'Route Inventory', priority: 0, status: 'completed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'skipped', persistence_status: 'completed', started_at: '2026-04-21T12:00:00Z' },
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for route inventory' },
      { id: 'auth-1', stage: 'auth', title: 'Auth', priority: 30, status: 'ready', worker_status: 'starting', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
    ],
    run: { stage_progress: [] },
  }

  const normalRows = buildStageMatrixRows(snapshot, { current_stage: 'auth', focus_status: 'starting', stalled: false }, t)
  assert.equal(normalRows.find((node) => node.stage === 'rce')?.status, 'waiting')
  assert.equal(normalRows.find((node) => node.stage === 'auth')?.status, 'starting')

  const stalledRows = buildStageMatrixRows(snapshot, { current_stage: 'auth', focus_status: 'stalled', stalled: true }, t)
  assert.equal(stalledRows.find((node) => node.stage === 'auth')?.status, 'stalled')
})

test('buildStageMatrixRows aggregates role states with fixed priority across subtasks', () => {
  const snapshot = {
    subtasks: [
      { id: 'auth-1', stage: 'auth', title: 'Auth #1', priority: 30, status: 'running', worker_status: 'completed', integrator_status: 'running', validator_status: 'pending', persistence_status: 'skipped' },
      { id: 'auth-2', stage: 'auth', title: 'Auth #2', priority: 31, status: 'ready', worker_status: 'failed', integrator_status: 'completed', validator_status: 'ready', persistence_status: 'pending' },
      { id: 'logic-1', stage: 'logic', title: 'Logic', priority: 80, status: 'completed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'completed', persistence_status: 'completed' },
    ],
    run: { stage_progress: [] },
  }

  const rows = buildStageMatrixRows(snapshot, { current_stage: 'auth', focus_status: 'running', stalled: false }, t)
  const authRow = rows.find((row) => row.stage === 'auth')

  assert.equal(authRow?.status, 'running')
  assert.equal(authRow?.isCurrent, true)
  assert.deepEqual(
    authRow?.roleCells.map((cell) => [cell.role, cell.status]),
    [
      ['worker', 'failed'],
      ['integrator', 'running'],
      ['validator', 'ready'],
      ['persistence', 'pending'],
    ],
  )
})

test('resolveSubtaskDisplayStatus shows starting when role bootstrap is still pending', () => {
  assert.equal(
    resolveSubtaskDisplayStatus({
      status: 'ready',
      worker_status: 'starting',
      integrator_status: 'pending',
      validator_status: 'pending',
      persistence_status: 'pending',
    }),
    'starting',
  )
})

test('buildOrchestrationFlow models init gate, active stages, and ready queue counts', () => {
  const snapshot = {
    run: {
      stage_progress: [
        { stage: 'init', completed_count: 1, subtask_count: 1 },
        { stage: 'rce', running_count: 1, completed_count: 0, subtask_count: 1 },
        { stage: 'auth', running_count: 0, completed_count: 0, subtask_count: 1 },
      ],
    },
    subtasks: [
      { id: 'init-1', stage: 'init', title: 'Route Inventory', priority: 0, status: 'completed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'skipped', persistence_status: 'completed' },
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'running', worker_status: 'running', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'auth-1', stage: 'auth', title: 'Auth', priority: 30, status: 'ready', worker_status: 'ready', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
    ],
  }

  const flow = buildOrchestrationFlow(snapshot, { current_stage: 'rce', focus_status: 'running', focus_subtask_id: 'rce-1' }, t)
  assert.equal(flow.init.status, 'completed')
  assert.equal(flow.activeStageCount, 1)
  assert.equal(flow.queuedUnitCount, 1)
  assert.equal(flow.completedStageCount, 1)
  assert.equal(flow.auditStages.find((node) => node.stage === 'rce')?.activeCount, 1)
  assert.equal(flow.auditStages.find((node) => node.stage === 'auth')?.readyCount, 1)
})

test('buildOrchestrationFlow keeps downstream nodes locked before init completes', () => {
  const snapshot = {
    run: { stage_progress: [] },
    subtasks: [
      { id: 'init-1', stage: 'init', title: 'Route Inventory', priority: 0, status: 'ready', worker_status: 'ready', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for route inventory' },
    ],
  }

  const flow = buildOrchestrationFlow(snapshot, { current_stage: 'init', focus_status: 'waiting' }, t)
  assert.equal(flow.init.readyCount, 1)
  assert.equal(flow.auditStages.find((node) => node.stage === 'rce')?.locked, true)
  assert.equal(flow.auditStages.find((node) => node.stage === 'rce')?.status, 'waiting')
  assert.equal(flow.auditStages.find((node) => node.stage === 'rce')?.waitingCount, 1)
})

test('buildExecutionQueues separates active, ready, waiting, and blocked units', () => {
  const snapshot = {
    run: { run: { planner_pending: true }, stage_progress: [] },
    subtasks: [
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'running', worker_status: 'running', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'auth-1', stage: 'auth', title: 'Auth', priority: 30, status: 'ready', worker_status: 'completed', integrator_status: 'ready', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'access-1', stage: 'access', title: 'Access', priority: 40, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for route inventory' },
      { id: 'xss-1', stage: 'xss', title: 'XSS', priority: 50, status: 'ready', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
      { id: 'logic-1', stage: 'logic', title: 'Logic', priority: 80, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for review' },
    ],
  }

  const queues = buildExecutionQueues(snapshot, {
    current_stage: 'rce',
    current_role: 'worker',
    focus_status: 'running',
    focus_subtask_id: 'rce-1',
    planner_pending: true,
    parallelism: { planner: 1, worker: 2, integrator: 1, validator: 1, persistence: 1 },
  }, t)

  assert.deepEqual(queues.active.map((unit) => [unit.role, unit.stage]), [['planner', 'rce'], ['worker', 'rce']])
  assert.deepEqual(queues.ready.map((unit) => [unit.role, unit.stage, unit.position]), [['integrator', 'auth', 1]])
  assert.equal(queues.ready.some((unit) => unit.stage === 'xss'), false)
  assert.deepEqual(queues.waiting.map((unit) => [unit.stage, unit.status, unit.position]), [['access', 'waiting', 1], ['xss', 'waiting', 2]])
  assert.equal(queues.blocked.length, 1)
  assert.equal(queues.blocked.some((unit) => unit.stage === 'access'), false)
  assert.equal(queues.totals.active, 2)
  assert.equal(queues.totals.ready, 1)
  assert.equal(queues.totals.waiting, 2)
  assert.equal(queues.totals.blocked, 1)
})

test('buildActivityFeed maps key events to readable summaries and filters unchanged subtask updates', () => {
  const subtasks = [
    { id: 'rce-1', stage: 'rce' },
  ]
  const events = [
    {
      sequence: 1,
      id: 'e1',
      event_type: 'planner.revised',
      payload_json: { reason: 'integrator_completed', created: ['auth'], unlocked: ['xss'], terminated: [] },
      created_at: '2026-04-21T12:00:01Z',
      message: 'Planner revision applied.',
    },
    {
      sequence: 2,
      id: 'e2',
      event_type: 'agent.started',
      subtask_id: 'rce-1',
      payload_json: { stage: 'rce', role: 'worker' },
      created_at: '2026-04-21T12:00:02Z',
      message: 'worker started for rce.',
    },
    {
      sequence: 3,
      id: 'e3',
      event_type: 'subtask.updated',
      subtask_id: 'rce-1',
      payload_json: { status: 'running', previous_status: 'running', blocked_reason: '', previous_blocked_reason: '', error_message: '', previous_error_message: '' },
      created_at: '2026-04-21T12:00:03Z',
      message: 'unchanged update',
    },
    {
      sequence: 4,
      id: 'e4',
      event_type: 'subtask.updated',
      subtask_id: 'rce-1',
      payload_json: { status: 'failed', previous_status: 'running', blocked_reason: '', previous_blocked_reason: '', error_message: 'validator crashed', previous_error_message: '' },
      created_at: '2026-04-21T12:00:04Z',
      message: 'status changed',
    },
  ]

  const activities = buildActivityFeed(events, subtasks, t)
  assert.equal(activities.length, 3)
  assert.equal(activities[0].headline, 'RCE Audit recorded a new error')
  assert.equal(activities[0].summary, 'validator crashed')
  assert.equal(activities[1].headline, 'Worker started RCE Audit')
  assert.equal(activities[2].headline, 'Planner revision applied')
  assert.match(activities[2].summary, /Reason: integrator completed, created 1, unlocked 1, terminated 0/)
})

test('pickDefaultRerunStages prefers failed audit stages and falls back to init when routes are missing', () => {
  assert.deepEqual(
    pickDefaultRerunStages({
      status: 'failed',
      output_json: [],
      stages: [
        { name: 'xss', status: 'failed' },
        { name: 'auth', status: 'failed' },
        { name: 'rce', status: 'completed' },
      ],
    }),
    ['init', 'auth', 'xss'],
  )

  assert.deepEqual(
    pickDefaultRerunStages({
      status: 'failed',
      output_json: [{ method: 'GET', path: '/health' }],
      stages: [
        { name: 'logic', status: 'failed' },
      ],
    }),
    ['logic'],
  )
})

test('parseRunScope normalizes rerun metadata and falls back for legacy runs', () => {
  assert.deepEqual(
    parseRunScope({
      mode: 'rerun_selected',
      selected_stages: ['xss', 'auth', 'auth'],
      carried_over_stages: ['init', 'rce'],
      reused_route_inventory: true,
    }),
    {
      mode: 'rerun_selected',
      selectedStages: ['auth', 'xss'],
      carriedOverStages: ['init', 'rce'],
      reusedRouteInventory: true,
    },
  )

  assert.equal(parseRunScope(null).mode, 'full')
  assert.equal(parseRunScope(null).selectedStages[0], 'init')
})

test('isTaskMainView only matches overview, orchestration, and report shells', () => {
  assert.equal(isTaskMainView('task-detail'), true)
  assert.equal(isTaskMainView('task-orchestration'), true)
  assert.equal(isTaskMainView('task-report'), true)
  assert.equal(isTaskMainView('task-auth'), false)
  assert.equal(isTaskMainView('dashboard'), false)
})

test('buildTaskOrchestrationPreview normalizes summary states for overview cards', () => {
  assert.deepEqual(
    buildTaskOrchestrationPreview(null),
    {
      hasRun: false,
      focusStatus: 'not_started',
      currentStage: '',
      activeSubtaskCount: 0,
      lastProgressAt: '',
      latestEventAt: '',
      latestEventMessage: '',
      lastRunStatus: '',
    },
  )

  assert.deepEqual(
    buildTaskOrchestrationPreview({
      focus_status: 'running',
      current_stage: 'auth',
      active_subtask_count: 3,
      last_progress_at: '2026-05-20T09:00:00Z',
      latest_event_at: '2026-05-20T09:01:00Z',
      latest_event_message: 'worker started for auth.',
      last_run_status: 'running',
    }),
    {
      hasRun: true,
      focusStatus: 'running',
      currentStage: 'auth',
      activeSubtaskCount: 3,
      lastProgressAt: '2026-05-20T09:00:00Z',
      latestEventAt: '2026-05-20T09:01:00Z',
      latestEventMessage: 'worker started for auth.',
      lastRunStatus: 'running',
    },
  )

  assert.equal(buildTaskOrchestrationPreview({ focus_status: 'paused', last_run_status: 'paused' }).focusStatus, 'paused')
  assert.equal(buildTaskOrchestrationPreview({ focus_status: 'failed', last_run_status: 'failed' }).focusStatus, 'failed')
  assert.equal(buildTaskOrchestrationPreview({ focus_status: 'completed', last_run_status: 'completed' }).focusStatus, 'completed')
  assert.equal(buildTaskOrchestrationPreview({ last_run_status: 'paused' }).focusStatus, 'paused')
})
