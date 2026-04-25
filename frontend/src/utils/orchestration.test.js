import test from 'node:test'
import assert from 'node:assert/strict'

import { buildActivityFeed, buildInitStageCard, buildStageMatrixRows, pickDefaultStage, resolveSubtaskDisplayStatus } from './orchestration.js'

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
  'orchestration.state.not_started': 'Not Started',
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

test('buildStageMatrixRows marks waiting-for-route-inventory as not started and starting stage as starting', () => {
  const snapshot = {
    subtasks: [
      { id: 'init-1', stage: 'init', title: 'Route Inventory', priority: 0, status: 'completed', worker_status: 'completed', integrator_status: 'completed', validator_status: 'skipped', persistence_status: 'completed', started_at: '2026-04-21T12:00:00Z' },
      { id: 'rce-1', stage: 'rce', title: 'RCE', priority: 10, status: 'blocked', worker_status: 'pending', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending', blocked_reason: 'waiting for route inventory' },
      { id: 'auth-1', stage: 'auth', title: 'Auth', priority: 30, status: 'ready', worker_status: 'starting', integrator_status: 'pending', validator_status: 'pending', persistence_status: 'pending' },
    ],
    run: { stage_progress: [] },
  }

  const normalRows = buildStageMatrixRows(snapshot, { current_stage: 'auth', focus_status: 'starting', stalled: false }, t)
  assert.equal(normalRows.find((node) => node.stage === 'rce')?.status, 'not_started')
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
