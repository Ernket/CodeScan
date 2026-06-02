export const ORCHESTRATION_STAGE_ORDER = ['init', 'rce', 'injection', 'auth', 'access', 'xss', 'config', 'fileop', 'logic']
export const ORCHESTRATION_AUDIT_STAGE_ORDER = ORCHESTRATION_STAGE_ORDER.filter((stage) => stage !== 'init')
export const ORCHESTRATION_ROLE_ORDER = ['worker', 'integrator', 'validator', 'persistence']
export const TASK_MAIN_VIEWS = ['task-detail', 'task-orchestration', 'task-report']

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
  failed: 3,
  paused: 4,
  ready: 5,
  waiting: 6,
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

export function buildOrchestrationFlow(snapshot, diagnostics, t) {
  const nodes = buildStageNodes(snapshot, diagnostics, t)
  const subtasksByStage = groupByStage(snapshot?.subtasks || [])
  const init = enrichFlowNode(nodes.find((node) => node.stage === 'init') || buildFallbackStageNode('init', t), subtasksByStage, diagnostics, t)
  const initComplete = init.status === 'completed'
  const auditStages = nodes
    .filter((node) => node.stage !== 'init')
    .map((node) => enrichFlowNode(node, subtasksByStage, diagnostics, t, { initComplete }))

  const activeStageCount = auditStages.filter((node) => ['starting', 'running', 'stalled'].includes(node.status)).length + (['starting', 'running', 'stalled'].includes(init.status) ? 1 : 0)
  const queuedUnitCount = auditStages.reduce((total, node) => total + node.readyCount, 0) + init.readyCount
  const waitingUnitCount = auditStages.reduce((total, node) => total + node.waitingCount, 0) + init.waitingCount
  const completedStageCount = auditStages.filter((node) => node.status === 'completed').length + (init.status === 'completed' ? 1 : 0)

  return {
    init,
    auditStages,
    hasRun: Boolean(snapshot?.run),
    activeStageCount,
    queuedUnitCount,
    waitingUnitCount,
    completedStageCount,
    totalStageCount: ORCHESTRATION_STAGE_ORDER.length,
  }
}

export function buildExecutionQueues(snapshot, diagnostics, t) {
  const subtasks = sortStageSubtasks(snapshot?.subtasks || [], diagnostics)
  const active = []
  const ready = []
  const waiting = []
  const blocked = []

  for (const subtask of subtasks) {
    const stageStatus = normalizeStageStateLabel(subtask?.status)
    if (isIssueSubtask(subtask)) {
      blocked.push(buildExecutionUnit(subtask, '', stageStatus === 'failed' ? 'failed' : 'blocked', diagnostics, t))
      continue
    }
    if (isDependencyWaitingSubtask(subtask)) {
      waiting.push(buildExecutionUnit(subtask, '', 'waiting', diagnostics, t))
      continue
    }

    let hasVisibleRoleUnit = false
    for (const role of ORCHESTRATION_ROLE_ORDER) {
      const status = normalizeRoleState(subtask?.[`${role}_status`])
      if (isActiveRoleStatus(status)) {
        active.push(buildExecutionUnit(subtask, role, status, diagnostics, t))
        hasVisibleRoleUnit = true
      } else if (isQueuedRoleStatus(role, status)) {
        ready.push(buildExecutionUnit(subtask, role, status, diagnostics, t))
        hasVisibleRoleUnit = true
      }
    }

    if (!hasVisibleRoleUnit && stageStatus !== 'completed') {
      waiting.push(buildExecutionUnit(subtask, '', 'waiting', diagnostics, t))
    }
  }

  active.sort(executionUnitSort)
  ready.sort(executionUnitSort)
  waiting.sort(executionUnitSort)
  blocked.sort(executionUnitSort)
  ready.forEach((unit, index) => {
    unit.position = index + 1
  })
  waiting.forEach((unit, index) => {
    unit.position = index + 1
  })

  const roleQueues = ['planner', ...ORCHESTRATION_ROLE_ORDER].map((role) => {
    if (role === 'planner') {
      const plannerActive = Boolean(diagnostics?.planner_pending || snapshot?.run?.run?.planner_pending)
      return {
        role,
        label: t('orchestration.roles.planner'),
        parallelism: Number(diagnostics?.parallelism?.planner || 0),
        active: plannerActive ? [buildPlannerUnit(snapshot, diagnostics, t)] : [],
        ready: [],
        waiting: [],
        blocked: [],
      }
    }

    return {
      role,
      label: t(`orchestration.roles.${role}`),
      parallelism: Number(diagnostics?.parallelism?.[role] || 0),
      active: active.filter((unit) => unit.role === role),
      ready: ready.filter((unit) => unit.role === role),
      waiting: waiting.filter((unit) => unit.role === role),
      blocked: blocked.filter((unit) => unit.role === role || unit.stageStatus === 'blocked' || unit.stageStatus === 'failed'),
    }
  })

  return {
    roles: roleQueues,
    active: roleQueues.flatMap((role) => role.active),
    ready,
    waiting,
    blocked,
    totals: {
      active: roleQueues.reduce((total, role) => total + role.active.length, 0),
      ready: ready.length,
      waiting: waiting.length,
      blocked: blocked.length,
    },
  }
}

export function pickDefaultStage(entries = []) {
  const ordered = Array.isArray(entries) ? entries : []
  const priorities = [
    ['blocked', 'stalled'],
    ['starting', 'running'],
    ['failed'],
    ['waiting'],
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
  if (status === 'blocked' && !hasMeaningfulBlock(subtask?.blocked_reason) && !subtask?.error_message) {
    return 'waiting'
  }
  if (['failed', 'paused', 'blocked', 'completed', 'stalled', 'waiting'].includes(status)) {
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

export function pickDefaultRerunStages(task = null) {
  const stages = Array.isArray(task?.stages) ? task.stages : []
  const failedStages = stages
    .map((stage) => ({
      name: normalizeString(stage?.name).toLowerCase(),
      status: normalizeString(stage?.status).toLowerCase(),
    }))
    .filter((stage) => stage.status === 'failed' && ORCHESTRATION_AUDIT_STAGE_ORDER.includes(stage.name))
    .sort((left, right) => stageOrderIndex[left.name] - stageOrderIndex[right.name])
    .map((stage) => stage.name)

  const selected = [...new Set(failedStages)]
  const taskStatus = normalizeString(task?.status).toLowerCase()
  const routes = normalizeJsonValue(task?.output_json)
  const hasRoutes = Array.isArray(routes) && routes.length > 0

  if (taskStatus === 'failed' && !hasRoutes && !selected.includes('init')) {
    selected.unshift('init')
  }

  return selected
}

export function parseRunScope(summaryJson) {
  const payload = normalizeJsonValue(summaryJson)
  const defaultScope = {
    mode: 'full',
    selectedStages: [...ORCHESTRATION_STAGE_ORDER],
    carriedOverStages: [],
    reusedRouteInventory: false,
  }

  if (!payload || typeof payload !== 'object') {
    return defaultScope
  }

  const normalizeStages = (value) => {
    const input = Array.isArray(value) ? value : []
    const stages = input
      .map((stage) => normalizeString(stage).toLowerCase())
      .filter((stage) => ORCHESTRATION_STAGE_ORDER.includes(stage))
    return [...new Set(stages)].sort((left, right) => stageOrderIndex[left] - stageOrderIndex[right])
  }

  const mode = normalizeString(payload.mode).toLowerCase() || 'full'
  const selectedStages = normalizeStages(payload.selected_stages)
  const carriedOverStages = normalizeStages(payload.carried_over_stages)

  if (mode !== 'rerun_selected') {
    return defaultScope
  }

  return {
    mode,
    selectedStages,
    carriedOverStages,
    reusedRouteInventory: Boolean(payload.reused_route_inventory),
  }
}

export function isTaskMainView(view) {
  return TASK_MAIN_VIEWS.includes(normalizeString(view))
}

export function buildTaskOrchestrationPreview(summary = null) {
  if (!summary || typeof summary !== 'object') {
    return {
      hasRun: false,
      focusStatus: 'not_started',
      currentStage: '',
      activeSubtaskCount: 0,
      lastProgressAt: '',
      latestEventAt: '',
      latestEventMessage: '',
      lastRunStatus: '',
    }
  }

  return {
    hasRun: true,
    focusStatus: normalizeFocusStatus(summary.focus_status, summary.last_run_status),
    currentStage: normalizeString(summary.current_stage),
    activeSubtaskCount: Number(summary.active_subtask_count || 0),
    lastProgressAt: normalizeTimestamp(summary.last_progress_at),
    latestEventAt: normalizeTimestamp(summary.latest_event_at),
    latestEventMessage: normalizeString(summary.latest_event_message),
    lastRunStatus: normalizeRunStatus(summary.last_run_status),
  }
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
    if (subtasks.some(isIssueSubtask)) {
      return 'blocked'
    }
    return 'waiting'
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

function buildFallbackStageNode(stage, t) {
  return {
    stage,
    label: t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.shortLabel`),
    fullLabel: t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`),
    status: 'not_started',
    provisionalCount: 0,
    validatedCount: 0,
    runningCount: 0,
    failedCount: 0,
    completedCount: 0,
    subtaskCount: 0,
    isCurrent: false,
  }
}

function enrichFlowNode(node, subtasksByStage, diagnostics, t, options = {}) {
  const subtasks = subtasksByStage.get(node.stage) || []
  const activeUnits = []
  const readyUnits = []
  const waitingUnits = []
  let pendingCount = 0
  let blockedCount = 0

  for (const subtask of sortStageSubtasks(subtasks, diagnostics)) {
    const subtaskStatus = normalizeStageStateLabel(subtask?.status)
    if (isIssueSubtask(subtask)) {
      blockedCount += 1
      continue
    }
    if (isDependencyWaitingSubtask(subtask)) {
      waitingUnits.push(buildExecutionUnit(subtask, '', 'waiting', diagnostics, t))
      continue
    }

    let hasVisibleRoleUnit = false
    for (const role of ORCHESTRATION_ROLE_ORDER) {
      const status = normalizeRoleState(subtask?.[`${role}_status`])
      if (isActiveRoleStatus(status)) {
        activeUnits.push(buildExecutionUnit(subtask, role, status, diagnostics, t))
        hasVisibleRoleUnit = true
      } else if (isQueuedRoleStatus(role, status)) {
        readyUnits.push(buildExecutionUnit(subtask, role, status, diagnostics, t))
        hasVisibleRoleUnit = true
      } else if (status === 'pending') {
        pendingCount += 1
      }
    }

    if (!hasVisibleRoleUnit && subtaskStatus !== 'completed') {
      waitingUnits.push(buildExecutionUnit(subtask, '', 'waiting', diagnostics, t))
    }
  }

  const locked = node.stage !== 'init' && !options.initComplete && ['not_started', 'waiting'].includes(node.status)
  const progressPercent = node.subtaskCount > 0
    ? Math.round((node.completedCount / node.subtaskCount) * 100)
    : node.status === 'completed'
      ? 100
      : 0

  return {
    ...node,
    locked,
    progressPercent,
    activeUnits,
    readyUnits,
    waitingUnits,
    activeCount: activeUnits.length,
    readyCount: readyUnits.length,
    waitingCount: waitingUnits.length,
    pendingCount,
    blockedCount,
    statusLabel: t(`orchestration.state.${normalizeStageStateLabel(node.status)}`),
  }
}

function buildExecutionUnit(subtask, role, status, diagnostics, t) {
  const stage = normalizeString(subtask?.stage)
  const stageLabel = stage ? t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`) : ''
  const roleLabel = role ? t(`orchestration.roles.${role}`) : ''
  const requestedStatus = normalizeStageStateLabel(status)
  const stageStatus = requestedStatus === 'waiting' || isDependencyWaitingSubtask(subtask)
    ? 'waiting'
    : normalizeStageStateLabel(subtask?.status)

  return {
    id: [subtask?.id || 'subtask', role || stageStatus].filter(Boolean).join(':'),
    subtaskId: subtask?.id || '',
    title: normalizeString(subtask?.title) || stageLabel || subtask?.id || '',
    stage,
    stageLabel,
    role,
    roleLabel,
    status,
    statusLabel: role ? t(`orchestration.roleState.${status}`) : t(`orchestration.state.${stageStatus}`),
    stageStatus,
    priority: Number(subtask?.priority || 0),
    isFocus: Boolean(subtask?.id && subtask.id === diagnostics?.focus_subtask_id),
    blockedReason: normalizeString(subtask?.blocked_reason),
    errorMessage: normalizeString(subtask?.error_message),
    startedAt: normalizeString(subtask?.started_at),
    updatedAt: normalizeString(subtask?.updated_at),
    position: 0,
  }
}

function buildPlannerUnit(snapshot, diagnostics, t) {
  return {
    id: 'planner:pending',
    subtaskId: '',
    title: diagnostics?.last_replan_reason
      ? formatReplanReason(diagnostics.last_replan_reason, t)
      : t('orchestration.queue.plannerPending'),
    stage: diagnostics?.current_stage || '',
    stageLabel: diagnostics?.current_stage ? t(diagnostics.current_stage === 'init' ? 'stage.init.label' : `stage.${diagnostics.current_stage}.label`) : '',
    role: 'planner',
    roleLabel: t('orchestration.roles.planner'),
    status: 'running',
    statusLabel: t('orchestration.state.running'),
    stageStatus: normalizeFocusStatus(diagnostics?.focus_status, snapshot?.run?.run?.status),
    priority: -1,
    isFocus: diagnostics?.current_role === 'planner',
    blockedReason: '',
    errorMessage: '',
    startedAt: '',
    updatedAt: normalizeString(diagnostics?.latest_event_at),
    position: 0,
  }
}

function executionUnitSort(left, right) {
  if (left.isFocus !== right.isFocus) return left.isFocus ? -1 : 1

  const leftStage = stageOrderIndex[left.stage] ?? Number.MAX_SAFE_INTEGER
  const rightStage = stageOrderIndex[right.stage] ?? Number.MAX_SAFE_INTEGER
  if (leftStage !== rightStage) return leftStage - rightStage

  if (left.priority !== right.priority) return left.priority - right.priority

  const leftRole = ORCHESTRATION_ROLE_ORDER.indexOf(left.role)
  const rightRole = ORCHESTRATION_ROLE_ORDER.indexOf(right.role)
  const leftRoleIndex = leftRole === -1 ? Number.MAX_SAFE_INTEGER : leftRole
  const rightRoleIndex = rightRole === -1 ? Number.MAX_SAFE_INTEGER : rightRole
  if (leftRoleIndex !== rightRoleIndex) return leftRoleIndex - rightRoleIndex

  return left.id.localeCompare(right.id)
}

function isActiveRoleStatus(status) {
  return status === 'starting' || status === 'running'
}

function isQueuedRoleStatus(role, status) {
  return status === 'ready' || ((role === 'worker' || role === 'validator') && status === 'paused')
}

function isIssueSubtask(subtask) {
  const status = normalizeStageStateLabel(subtask?.status)
  if (status === 'failed') return true
  if (normalizeString(subtask?.error_message)) return true
  return status === 'blocked' && hasMeaningfulBlock(subtask?.blocked_reason)
}

function isDependencyWaitingSubtask(subtask) {
  const status = normalizeStageStateLabel(subtask?.status)
  return status === 'blocked' && !isIssueSubtask(subtask)
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

function normalizeTimestamp(value) {
  return normalizeString(value)
}

function normalizeRunStatus(value) {
  switch (normalizeString(value).toLowerCase()) {
    case 'running':
      return 'running'
    case 'paused':
      return 'paused'
    case 'completed':
      return 'completed'
    case 'failed':
      return 'failed'
    default:
      return ''
  }
}

function normalizeFocusStatus(value, fallbackStatus) {
  switch (normalizeString(value).toLowerCase()) {
    case 'starting':
      return 'starting'
    case 'running':
      return 'running'
    case 'blocked':
      return 'blocked'
    case 'stalled':
      return 'stalled'
    case 'failed':
      return 'failed'
    case 'paused':
      return 'paused'
    case 'completed':
      return 'completed'
    case 'waiting':
      return 'waiting'
    default:
      return normalizeRunStatus(fallbackStatus) || 'not_started'
  }
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
    case 'waiting':
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
    case 'waiting':
      return 'waiting'
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
