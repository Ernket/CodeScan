export const ORCHESTRATION_STAGE_ORDER = ['init', 'rce', 'injection', 'auth', 'access', 'xss', 'config', 'fileop', 'logic']
export const ORCHESTRATION_AUDIT_STAGE_ORDER = ORCHESTRATION_STAGE_ORDER.filter((stage) => stage !== 'init')
export const ORCHESTRATION_ROLE_ORDER = ['worker', 'integrator', 'validator', 'persistence']

const ROUTE_INVENTORY_WAIT = 'waiting for route inventory'
const stageOrderIndex = Object.fromEntries(ORCHESTRATION_STAGE_ORDER.map((stage, index) => [stage, index]))
const roleAggregateScore = {
  failed: 7,
  running: 6,
  starting: 5,
  paused: 4,
  ready: 3,
  completed: 2,
  pending: 1,
  skipped: 1,
}
const subtaskDisplayPriority = {
  starting: 0,
  running: 1,
  blocked: 2,
  paused: 3,
  failed: 4,
  ready: 5,
  completed: 6,
}

export function buildStageNodes(snapshot, diagnostics, t) {
  const progressByStage = new Map((snapshot?.run?.stage_progress || []).map((item) => [item.stage, item]))
  const subtasksByStage = groupByStage(snapshot?.subtasks || [])

  return ORCHESTRATION_STAGE_ORDER.map((stage) => buildStageOverview(stage, progressByStage, subtasksByStage, diagnostics, t))
}

export function buildInitStageCard(snapshot, diagnostics, t) {
  const progressByStage = new Map((snapshot?.run?.stage_progress || []).map((item) => [item.stage, item]))
  const subtasksByStage = groupByStage(snapshot?.subtasks || [])
  return buildStageOverview('init', progressByStage, subtasksByStage, diagnostics, t)
}

export function buildStageMatrixRows(snapshot, diagnostics, t) {
  const progressByStage = new Map((snapshot?.run?.stage_progress || []).map((item) => [item.stage, item]))
  const subtasksByStage = groupByStage(snapshot?.subtasks || [])

  return ORCHESTRATION_AUDIT_STAGE_ORDER.map((stage) => {
    const overview = buildStageOverview(stage, progressByStage, subtasksByStage, diagnostics, t)
    const subtasks = subtasksByStage.get(stage) || []

    return {
      ...overview,
      roleCells: ORCHESTRATION_ROLE_ORDER.map((role) => ({
        role,
        label: t(`orchestration.roles.${role}`),
        status: aggregateRoleStatus(subtasks, role),
      })),
    }
  })
}

export function pickDefaultStage(entries = []) {
  const ordered = Array.isArray(entries) ? entries : []
  const priorities = [
    ['blocked', 'stalled'],
    ['starting', 'running'],
    ['failed'],
  ]

  for (const states of priorities) {
    const match = ordered.find((node) => states.includes(node.status))
    if (match) return match.stage
  }

  const incomplete = ordered.find((node) => node.status !== 'completed')
  return incomplete?.stage || ordered[0]?.stage || 'init'
}

export function sortStageSubtasks(subtasks = [], diagnostics = null) {
  return [...(Array.isArray(subtasks) ? subtasks : [])].sort((left, right) => {
    const leftScore = subtaskSortScore(left, diagnostics)
    const rightScore = subtaskSortScore(right, diagnostics)
    if (leftScore !== rightScore) return leftScore - rightScore

    const leftStage = stageOrderIndex[left.stage] ?? Number.MAX_SAFE_INTEGER
    const rightStage = stageOrderIndex[right.stage] ?? Number.MAX_SAFE_INTEGER
    if (leftStage !== rightStage) return leftStage - rightStage

    return Number(left.priority || 0) - Number(right.priority || 0)
  })
}

export function buildRolePipeline(subtask, t) {
  return ORCHESTRATION_ROLE_ORDER.map((role) => ({
    role,
    label: t(`orchestration.roles.${role}`),
    status: String(subtask?.[`${role}_status`] || 'pending').toLowerCase(),
  }))
}

export function resolveSubtaskDisplayStatus(subtask) {
  const status = normalizeStageStateLabel(subtask?.status)
  if (['failed', 'paused', 'blocked', 'completed', 'stalled'].includes(status)) {
    return status
  }

  const statuses = roleStatuses(subtask)
  if (statuses.includes('starting')) return 'starting'
  if (status === 'running' || statuses.includes('running')) return 'running'
  return status
}

export function buildActivityFeed(events = [], subtasks = [], t) {
  const subtaskStageMap = new Map((Array.isArray(subtasks) ? subtasks : []).map((subtask) => [subtask.id, subtask.stage]))
  const orderedEvents = [...(Array.isArray(events) ? events : [])].sort((left, right) => {
    return Number(left.sequence || 0) - Number(right.sequence || 0)
  })

  const lastSubtaskState = new Map()
  const activities = []

  for (const event of orderedEvents) {
    const activity = summarizeEvent(event, { subtaskStageMap, lastSubtaskState, t })
    if (activity) {
      activities.push(activity)
    }
  }

  return activities.sort((left, right) => Number(right.sequence || 0) - Number(left.sequence || 0))
}

export function formatReplanReason(reason, t) {
  const normalized = String(reason || '').trim().toLowerCase()
  if (!normalized) return t('orchestration.replan.unknown')

  const known = new Set([
    'run_start',
    'resume',
    'queue_idle',
    'worker_completed',
    'worker_failed',
    'validator_completed',
    'validator_failed',
    'integrator_completed',
    'integrator_failed',
    'persistence_completed',
    'persistence_failed',
  ])

  if (!known.has(normalized)) {
    return normalized
  }

  return t(`orchestration.replan.${normalized}`)
}

export function findingSummary(finding, fallback) {
  const payload = normalizeJsonValue(finding?.payload_json)
  if (payload && typeof payload === 'object') {
    return payload.description || payload.execution_logic || fallback
  }
  return fallback
}

function resolveStageStatus(stage, subtasks, diagnostics) {
  if (diagnostics?.stalled && diagnostics?.current_stage === stage) {
    return 'stalled'
  }

  if (!Array.isArray(subtasks) || subtasks.length === 0) {
    return 'not_started'
  }

  if (subtasks.some((subtask) => String(subtask.status).toLowerCase() === 'failed')) {
    return 'failed'
  }

  if (subtasks.some((subtask) => String(subtask.status).toLowerCase() === 'paused')) {
    return 'paused'
  }

  const blocked = subtasks.find((subtask) => String(subtask.status).toLowerCase() === 'blocked')
  if (blocked) {
    if (subtasks.some((subtask) => hasMeaningfulBlock(subtask.blocked_reason)) || subtasks.some(hasSubtaskStarted)) {
      return 'blocked'
    }
    return 'not_started'
  }

  if (subtasks.every((subtask) => String(subtask.status).toLowerCase() === 'completed')) {
    return 'completed'
  }

  if (subtasks.some((subtask) => String(subtask.status).toLowerCase() === 'starting' || roleStatuses(subtask).includes('starting'))) {
    return 'starting'
  }

  if (subtasks.some((subtask) => String(subtask.status).toLowerCase() === 'running' || roleStatuses(subtask).includes('running'))) {
    return 'running'
  }

  if (diagnostics?.focus_status === 'starting' && diagnostics?.current_stage === stage) {
    return 'starting'
  }

  if (diagnostics?.focus_status === 'running' && diagnostics?.current_stage === stage) {
    return 'running'
  }

  if (subtasks.some((subtask) => String(subtask.status).toLowerCase() === 'ready')) {
    if (subtasks.some(hasSubtaskStarted)) {
      return 'running'
    }
    return 'not_started'
  }

  return 'not_started'
}

function subtaskSortScore(subtask, diagnostics) {
  if (subtask?.id && subtask.id === diagnostics?.focus_subtask_id) {
    return -100
  }

  const status = resolveSubtaskDisplayStatus(subtask)
  const base = subtaskDisplayPriority[status] ?? 50
  return base * 100 + Number(subtask?.priority || 0)
}

function summarizeEvent(event, { subtaskStageMap, lastSubtaskState, t }) {
  const payload = normalizeJsonValue(event?.payload_json)
  const stage = normalizeString(payload?.stage) || normalizeString(subtaskStageMap.get(event?.subtask_id))
  const role = normalizeString(payload?.role)
  const stageLabel = stage ? t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`) : ''
  const roleLabel = role ? t(`orchestration.roles.${role}`) : ''
  const eventType = String(event?.event_type || '').trim()

  switch (eventType) {
    case 'run.started':
      return buildActivity(event, stage, role, 'info', t('orchestration.activities.runStarted'), t('orchestration.activities.runStartedDetail', {
        count: Array.isArray(payload?.subtask_ids) ? payload.subtask_ids.length : 0,
      }))
    case 'run.paused':
      return buildActivity(event, stage, role, 'warn', t('orchestration.activities.runPaused'), '')
    case 'run.resumed':
      return buildActivity(event, stage, role, 'info', t('orchestration.activities.runResumed'), '')
    case 'run.completed':
      return buildActivity(event, stage, role, 'success', t('orchestration.activities.runCompleted'), '')
    case 'run.failed':
      return buildActivity(event, stage, role, 'error', t('orchestration.activities.runFailed'), normalizeString(event?.message))
    case 'planner.revised':
      return buildActivity(
        event,
        stage,
        role,
        'info',
        t('orchestration.activities.plannerRevised'),
        t('orchestration.activities.plannerRevisedDetail', {
          reason: formatReplanReason(payload?.reason, t),
          created: listCount(payload?.created),
          unlocked: listCount(payload?.unlocked),
          terminated: listCount(payload?.terminated),
        }),
      )
    case 'agent.started':
      return buildActivity(event, stage, role, 'info', t('orchestration.activities.agentStarted', { role: roleLabel, stage: stageLabel }), '')
    case 'agent.completed':
      return buildActivity(event, stage, role, 'success', t('orchestration.activities.agentCompleted', { role: roleLabel, stage: stageLabel }), '')
    case 'agent.paused':
      return buildActivity(event, stage, role, 'warn', t('orchestration.activities.agentPaused', { role: roleLabel, stage: stageLabel }), '')
    case 'agent.failed':
      return buildActivity(
        event,
        stage,
        role,
        'error',
        t('orchestration.activities.agentFailed', { role: roleLabel, stage: stageLabel }),
        normalizeString(payload?.error) || normalizeString(event?.message),
      )
    case 'routes.materialized':
      return buildActivity(
        event,
        stage,
        role,
        normalizeString(payload?.mode) === 'final' ? 'success' : 'info',
        t('orchestration.activities.routesMaterialized', { stage: stageLabel || t('stage.init.label'), count: Number(payload?.count || 0) }),
        t('orchestration.activities.materializedMode', {
          mode: t(`orchestration.materialized.${normalizeString(payload?.mode) === 'final' ? 'final' : 'provisional'}`),
        }),
      )
    case 'findings.materialized':
      return buildActivity(
        event,
        stage,
        role,
        normalizeString(payload?.mode) === 'final' ? 'success' : 'info',
        t('orchestration.activities.findingsMaterialized', { stage: stageLabel, count: Number(payload?.count || 0) }),
        t('orchestration.activities.materializedMode', {
          mode: t(`orchestration.materialized.${normalizeString(payload?.mode) === 'final' ? 'final' : 'provisional'}`),
        }),
      )
    case 'subtask.updated':
      return summarizeSubtaskUpdated(event, stage, payload, lastSubtaskState, t)
    default:
      return buildActivity(event, stage, role, normalizeLevel(event?.level), t('orchestration.activities.generic', { eventType }), normalizeString(event?.message))
  }
}

function summarizeSubtaskUpdated(event, stage, payload, lastSubtaskState, t) {
  const nextState = {
    status: normalizeString(payload?.status),
    blocked_reason: normalizeString(payload?.blocked_reason),
    error_message: normalizeString(payload?.error_message),
  }
  const previousState = {
    status: normalizeString(payload?.previous_status),
    blocked_reason: normalizeString(payload?.previous_blocked_reason),
    error_message: normalizeString(payload?.previous_error_message),
  }

  const stageLabel = stage ? t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`) : ''
  const lastKnown = lastSubtaskState.get(event?.subtask_id) || {}
  const statusChanged = Boolean(nextState.status && nextState.status !== (previousState.status || lastKnown.status || ''))
  const blockedChanged = nextState.blocked_reason !== (previousState.blocked_reason || lastKnown.blocked_reason || '')
  const errorChanged = nextState.error_message !== (previousState.error_message || lastKnown.error_message || '')

  lastSubtaskState.set(event?.subtask_id, { ...lastKnown, ...nextState })

  if (!statusChanged && !blockedChanged && !errorChanged) {
    return null
  }

  if (errorChanged && nextState.error_message) {
    return buildActivity(event, stage, '', 'error', t('orchestration.activities.subtaskError', { stage: stageLabel }), nextState.error_message)
  }
  if (blockedChanged && nextState.blocked_reason) {
    return buildActivity(event, stage, '', 'warn', t('orchestration.activities.subtaskBlocked', { stage: stageLabel }), nextState.blocked_reason)
  }
  if (statusChanged && nextState.status) {
    return buildActivity(event, stage, '', normalizeStageTone(nextState.status), t('orchestration.activities.subtaskStatusChanged', {
      stage: stageLabel,
      status: t(`orchestration.state.${normalizeStageStateLabel(nextState.status)}`),
    }), '')
  }

  return null
}

function buildActivity(event, stage, role, tone, headline, summary) {
  return {
    id: event?.id || `event-${event?.sequence || Math.random().toString(16).slice(2)}`,
    sequence: Number(event?.sequence || 0),
    stage,
    role,
    tone,
    headline,
    summary,
    eventType: String(event?.event_type || ''),
    rawMessage: normalizeString(event?.message),
    createdAt: event?.created_at || '',
    payload: normalizeJsonValue(event?.payload_json),
  }
}

function groupByStage(subtasks) {
  const grouped = new Map()
  for (const subtask of Array.isArray(subtasks) ? subtasks : []) {
    const stage = String(subtask?.stage || '').trim()
    if (!grouped.has(stage)) {
      grouped.set(stage, [])
    }
    grouped.get(stage).push(subtask)
  }
  return grouped
}

function buildStageOverview(stage, progressByStage, subtasksByStage, diagnostics, t) {
  const subtasks = subtasksByStage.get(stage) || []
  const progress = progressByStage.get(stage) || null

  return {
    stage,
    label: t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.shortLabel`),
    fullLabel: t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`),
    status: resolveStageStatus(stage, subtasks, diagnostics),
    provisionalCount: Number(progress?.provisional_count || 0),
    validatedCount: Number(progress?.validated_count || 0),
    runningCount: Number(progress?.running_count || 0),
    failedCount: Number(progress?.failed_count || 0),
    completedCount: Number(progress?.completed_count || 0),
    subtaskCount: Number(progress?.subtask_count || subtasks.length || 0),
    isCurrent: diagnostics?.current_stage === stage,
  }
}

function roleStatuses(subtask) {
  return ORCHESTRATION_ROLE_ORDER.map((role) => String(subtask?.[`${role}_status`] || '').toLowerCase()).filter(Boolean)
}

function aggregateRoleStatus(subtasks, role) {
  if (!Array.isArray(subtasks) || subtasks.length === 0) {
    return 'pending'
  }

  const statuses = subtasks.map((subtask) => normalizeRoleState(subtask?.[`${role}_status`]))
  let winner = 'pending'
  let highestScore = 0
  let hasPending = false
  let hasSkipped = false

  for (const status of statuses) {
    if (status === 'pending') hasPending = true
    if (status === 'skipped') hasSkipped = true

    const score = roleAggregateScore[status] || 0
    if (score > highestScore) {
      highestScore = score
      winner = status
    }
  }

  if (highestScore <= roleAggregateScore.pending) {
    if (hasPending) return 'pending'
    if (hasSkipped) return 'skipped'
  }

  return winner
}

function hasSubtaskStarted(subtask) {
  if (subtask?.started_at || subtask?.completed_at) return true
  return roleStatuses(subtask).some((status) => ['starting', 'running', 'paused', 'completed', 'failed'].includes(status))
}

function hasMeaningfulBlock(reason) {
  const normalized = normalizeString(reason).toLowerCase()
  return normalized !== '' && normalized !== ROUTE_INVENTORY_WAIT
}

function normalizeJsonValue(value) {
  if (!value) return null
  if (typeof value === 'object') return value
  try {
    return JSON.parse(value)
  } catch {
    return null
  }
}

function normalizeString(value) {
  return String(value || '').trim()
}

function normalizeRoleState(value) {
  switch (String(value || '').trim().toLowerCase()) {
    case 'starting':
      return 'starting'
    case 'running':
      return 'running'
    case 'ready':
      return 'ready'
    case 'paused':
      return 'paused'
    case 'skipped':
      return 'skipped'
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    default:
      return 'pending'
  }
}

function normalizeStageTone(status) {
  switch (normalizeStageStateLabel(status)) {
    case 'failed':
      return 'error'
    case 'blocked':
    case 'paused':
    case 'stalled':
      return 'warn'
    case 'completed':
      return 'success'
    default:
      return 'info'
  }
}

function normalizeStageStateLabel(status) {
  switch (String(status || '').trim().toLowerCase()) {
    case 'starting':
      return 'starting'
    case 'running':
      return 'running'
    case 'blocked':
      return 'blocked'
    case 'paused':
      return 'paused'
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    case 'stalled':
      return 'stalled'
    default:
      return 'not_started'
  }
}

function normalizeLevel(level) {
  switch (String(level || '').trim().toLowerCase()) {
    case 'error':
      return 'error'
    case 'warn':
    case 'warning':
      return 'warn'
    case 'success':
      return 'success'
    default:
      return 'info'
  }
}

function listCount(value) {
  return Array.isArray(value) ? value.length : 0
}
