<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import axios from 'axios'
import {
  Activity,
  AlertTriangle,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  Clock3,
  ListTree,
  Play,
  Radar,
  RefreshCw,
  Route,
  ShieldAlert,
  ShieldCheck,
  XCircle,
} from 'lucide-vue-next'

import {
  buildActivityFeed,
  buildExecutionQueues,
  buildOrchestrationFlow,
  buildRolePipeline,
  findingSummary,
  parseRunScope,
  pickDefaultStage,
  resolveSubtaskDisplayStatus,
  sortStageSubtasks,
} from '../utils/orchestration'

const props = defineProps({
  taskId: {
    type: String,
    required: true,
  },
  task: {
    type: Object,
    required: true,
  },
  apiUrl: {
    type: String,
    default: '/api',
  },
  authToken: {
    type: String,
    required: true,
  },
  locale: {
    type: String,
    default: 'zh',
  },
  t: {
    type: Function,
    required: true,
  },
  startPending: {
    type: Boolean,
    default: false,
  },
  stageIssueSummaries: {
    type: Array,
    default: () => [],
  },
  canWrite: {
    type: Boolean,
    default: false,
  },
})

const emit = defineEmits(['refresh-task', 'request-start', 'open-stage-console'])

const POLL_INTERVAL_MS = 15000

const snapshot = ref(createEmptySnapshot())
const isLoading = ref(false)
const error = ref('')
const selectedStage = ref('init')
const stageSelectionMode = ref('auto')
const activityScope = ref('selected')
const expandedActivities = ref({})
const lastSequence = ref(0)

let eventSource = null
let refreshTimer = null
let pollingTimer = null
let reconnectTimer = null

const runSummary = computed(() => snapshot.value?.run || null)
const diagnostics = computed(() => snapshot.value?.diagnostics || null)
const runScope = computed(() => parseRunScope(runSummary.value?.run?.summary_json))
const orchestrationFlow = computed(() => buildOrchestrationFlow(snapshot.value, diagnostics.value, props.t))
const executionQueues = computed(() => buildExecutionQueues(snapshot.value, diagnostics.value, props.t))
const activeExecutionUnits = computed(() => executionQueues.value.active.slice(0, 8))
const readyExecutionUnits = computed(() => executionQueues.value.ready.slice(0, 10))
const waitingExecutionUnits = computed(() => executionQueues.value.waiting.slice(0, 8))
const blockedExecutionUnits = computed(() => executionQueues.value.blocked.slice(0, 4))
const focusEntries = computed(() => [orchestrationFlow.value.init, ...orchestrationFlow.value.auditStages])
const selectedStageNode = computed(() => {
  return focusEntries.value.find((node) => node.stage === selectedStage.value) || focusEntries.value[0] || null
})
const selectedStageSubtasks = computed(() => {
  const subtasks = (snapshot.value?.subtasks || []).filter((subtask) => subtask.stage === selectedStage.value)
  return sortStageSubtasks(subtasks, diagnostics.value)
})
const selectedStageRoutes = computed(() => {
  return (snapshot.value?.routes || []).filter((route) => route.origin_stage === selectedStage.value)
})
const selectedStageFindings = computed(() => {
  return (snapshot.value?.findings || []).filter((finding) => finding.origin_stage === selectedStage.value)
})
const activities = computed(() => buildActivityFeed(snapshot.value?.events || [], snapshot.value?.subtasks || [], props.t))
const visibleActivities = computed(() => {
  if (activityScope.value !== 'selected' || !selectedStage.value) {
    return activities.value
  }
  return activities.value.filter((activity) => !activity.stage || activity.stage === selectedStage.value)
})
const runScopeDetails = computed(() => {
  if (runScope.value.mode !== 'rerun_selected') {
    return null
  }
  return {
    selected: formatStageList(runScope.value.selectedStages),
    carried: formatStageList(runScope.value.carriedOverStages),
    routes: props.locale === 'en'
      ? (runScope.value.reusedRouteInventory ? 'Reused current route inventory' : 'Reran route inventory')
      : (runScope.value.reusedRouteInventory ? '复用当前路由结果' : '本轮重新扫描路由'),
  }
})

function createEmptySnapshot() {
  return {
    run: null,
    diagnostics: null,
    subtasks: [],
    agents: [],
    routes: [],
    findings: [],
    events: [],
    updated_at: '',
  }
}

function authConfig() {
  return {
    headers: {
      Authorization: `Bearer ${props.authToken}`,
    },
  }
}

async function loadSnapshot(silent = false) {
  if (!props.taskId) return
  if (!silent) isLoading.value = true
  error.value = ''

  try {
    const { data } = await axios.get(`${props.apiUrl}/tasks/${props.taskId}/orchestration`, authConfig())
    const nextSnapshot = normalizeSnapshot(data)
    snapshot.value = nextSnapshot
    lastSequence.value = nextSnapshot.events.reduce((max, event) => Math.max(max, Number(event.sequence) || 0), 0)
    syncPolling()
    syncSelectedStage()
  } catch (requestError) {
    error.value = requestError.response?.data?.error || requestError.message
  } finally {
    if (!silent) isLoading.value = false
  }
}

function normalizeSnapshot(data) {
  const nextSnapshot = data || {}
  return {
    run: nextSnapshot.run || null,
    diagnostics: nextSnapshot.diagnostics || null,
    subtasks: Array.isArray(nextSnapshot.subtasks) ? nextSnapshot.subtasks : [],
    agents: Array.isArray(nextSnapshot.agents) ? nextSnapshot.agents : [],
    routes: Array.isArray(nextSnapshot.routes) ? nextSnapshot.routes : [],
    findings: Array.isArray(nextSnapshot.findings) ? nextSnapshot.findings : [],
    events: Array.isArray(nextSnapshot.events) ? nextSnapshot.events : [],
    updated_at: nextSnapshot.updated_at || '',
  }
}

function connectEvents() {
  closeRealtime()
  if (!props.taskId || !props.authToken) return

  const url = `${props.apiUrl}/tasks/${props.taskId}/orchestration/events?token=${encodeURIComponent(props.authToken)}&after=${lastSequence.value}`
  eventSource = new EventSource(url)

  eventSource.onmessage = (message) => {
    try {
      const payload = JSON.parse(message.data)
      const sequence = Number(payload.sequence) || 0
      if (sequence > lastSequence.value) {
        lastSequence.value = sequence
      }

      const known = new Set((snapshot.value.events || []).map((event) => Number(event.sequence) || 0))
      if (!known.has(sequence)) {
        snapshot.value = {
          ...snapshot.value,
          events: [...(snapshot.value.events || []), payload],
        }
      }

      scheduleRefresh()
      if (payload.event_type === 'run.completed' || payload.event_type === 'run.failed') {
        emit('refresh-task')
      }
    } catch {
      scheduleRefresh()
    }
  }

  eventSource.onerror = () => {
    closeRealtime()
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      connectEvents()
      syncPolling()
    }, 1500)
  }

  syncPolling()
}

function closeRealtime() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
  if (refreshTimer) {
    clearTimeout(refreshTimer)
    refreshTimer = null
  }
  if (pollingTimer) {
    clearInterval(pollingTimer)
    pollingTimer = null
  }
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
}

function scheduleRefresh() {
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    loadSnapshot(true)
  }, 250)
}

function syncPolling() {
  if (pollingTimer) {
    clearInterval(pollingTimer)
    pollingTimer = null
  }
  if (runSummary.value?.run?.status !== 'running') {
    return
  }

  pollingTimer = setInterval(() => {
    loadSnapshot(true)
  }, POLL_INTERVAL_MS)
}

function requestStart() {
  if (!props.canWrite || !props.taskId || props.task?.status === 'running' || props.startPending) return
  error.value = ''
  emit('request-start')
}

function syncSelectedStage() {
  const nextStage = pickDefaultStage(focusEntries.value)
  const currentExists = focusEntries.value.some((node) => node.stage === selectedStage.value)
  if (stageSelectionMode.value === 'auto' || !currentExists) {
    selectedStage.value = nextStage
  }
}

function chooseStage(stage) {
  selectedStage.value = stage
  stageSelectionMode.value = 'manual'
}

function resetStageFocus() {
  stageSelectionMode.value = 'auto'
  syncSelectedStage()
}

function openStageConsole(view) {
  emit('open-stage-console', view)
}

function toggleActivity(id) {
  expandedActivities.value = {
    ...expandedActivities.value,
    [id]: !expandedActivities.value[id],
  }
}

function formatDate(value) {
  if (!value) return '--'
  try {
    return new Date(value).toLocaleString(props.locale === 'en' ? 'en-US' : 'zh-CN')
  } catch {
    return value
  }
}

function formatDuration(seconds) {
  const value = Number(seconds || 0)
  if (value <= 0) return props.t('orchestration.duration.justNow')

  if (value < 60) {
    return props.t('orchestration.duration.seconds', { count: value })
  }
  if (value < 3600) {
    const minutes = Math.floor(value / 60)
    const remainder = value % 60
    return props.t('orchestration.duration.minutes', { count: minutes, remainder })
  }
  if (value < 86400) {
    const hours = Math.floor(value / 3600)
    const minutes = Math.floor((value % 3600) / 60)
    return props.t('orchestration.duration.hours', { count: hours, remainder: minutes })
  }

  const days = Math.floor(value / 86400)
  const hours = Math.floor((value % 86400) / 3600)
  return props.t('orchestration.duration.days', { count: days, remainder: hours })
}

function stageLabel(stage) {
  return props.t(stage === 'init' ? 'stage.init.label' : `stage.${stage}.label`)
}

function stageStateLabel(state) {
  return props.t(`orchestration.state.${normalizeStateKey(state)}`)
}

function roleStateLabel(state) {
  return props.t(`orchestration.roleState.${normalizeRoleStateKey(state)}`)
}

function roleLabel(role) {
  return props.t(`orchestration.roles.${String(role || '').trim().toLowerCase() || 'worker'}`)
}

function stateBadgeClass(state) {
  switch (normalizeStateKey(state)) {
    case 'starting':
    case 'running':
      return 'bg-cyan-500/15 text-cyan-200 border-cyan-500/30'
    case 'waiting':
      return 'bg-slate-500/15 text-slate-200 border-slate-500/30'
    case 'blocked':
    case 'paused':
      return 'bg-amber-500/15 text-amber-200 border-amber-500/30'
    case 'stalled':
      return diagnostics.value?.stalled
        ? 'bg-amber-500/15 text-amber-200 border-amber-500/30'
        : 'bg-slate-500/15 text-slate-200 border-slate-500/30'
    case 'completed':
      return 'bg-emerald-500/15 text-emerald-200 border-emerald-500/30'
    case 'failed':
      return 'bg-rose-500/15 text-rose-200 border-rose-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border-slate-500/20'
  }
}

function stageIssueStatusClass(status) {
  switch (String(status || '').trim().toLowerCase()) {
    case 'running':
      return 'bg-amber-500/10 text-amber-300 border-amber-500/30'
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-300 border-emerald-500/30'
    case 'failed':
      return 'bg-rose-500/10 text-rose-300 border-rose-500/30'
    case 'paused':
      return 'bg-sky-500/10 text-sky-300 border-sky-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border-slate-500/30'
  }
}

function roleBadgeClass(state) {
  switch (normalizeRoleStateKey(state)) {
    case 'starting':
    case 'running':
      return 'bg-cyan-500/10 text-cyan-200 border-cyan-500/25'
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-200 border-emerald-500/25'
    case 'paused':
      return 'bg-amber-500/10 text-amber-200 border-amber-500/25'
    case 'failed':
      return 'bg-rose-500/10 text-rose-200 border-rose-500/25'
    case 'ready':
      return 'bg-blue-500/10 text-blue-200 border-blue-500/25'
    case 'skipped':
      return 'bg-slate-700/40 text-slate-300 border-slate-600/40'
    default:
      return 'bg-slate-500/10 text-slate-300 border-slate-500/20'
  }
}

function flowNodeClass(node) {
  const state = normalizeStateKey(node?.status)
  const classes = ['relative w-full min-h-[136px] rounded-xl border px-4 py-4 text-left transition-all']

  if (selectedStage.value === node?.stage) {
    classes.push('border-cyan-300/55 bg-cyan-500/10 shadow-[0_0_0_1px_rgba(34,211,238,0.22)]')
  } else if (node?.locked) {
    classes.push('border-white/8 bg-slate-950/25 opacity-70 hover:bg-white/[0.04]')
  } else if (state === 'stalled') {
    classes.push('border-amber-500/35 bg-amber-500/[0.07] hover:bg-amber-500/10')
  } else if (node?.isCurrent || ['starting', 'running'].includes(state)) {
    classes.push('border-cyan-500/35 bg-cyan-500/[0.08] hover:bg-cyan-500/12')
  } else if (state === 'failed') {
    classes.push('border-rose-500/35 bg-rose-500/[0.07] hover:bg-rose-500/10')
  } else if (['blocked', 'paused'].includes(state)) {
    classes.push('border-amber-500/35 bg-amber-500/[0.07] hover:bg-amber-500/10')
  } else if (state === 'waiting') {
    classes.push('border-slate-500/30 bg-slate-500/[0.07] hover:bg-slate-500/10')
  } else if (state === 'completed') {
    classes.push('border-emerald-500/30 bg-emerald-500/[0.06] hover:bg-emerald-500/10')
  } else {
    classes.push('border-white/10 bg-slate-950/40 hover:bg-white/5')
  }

  return classes.join(' ')
}

function flowProgressStyle(node) {
  const value = Math.max(0, Math.min(100, Number(node?.progressPercent || 0)))
  return { width: `${value}%` }
}

function flowConnectorClass() {
  const initStatus = normalizeStateKey(orchestrationFlow.value?.init?.status)
  if (initStatus === 'completed') {
    return 'from-emerald-400/70 via-cyan-400/45 to-cyan-400/15'
  }
  if (['starting', 'running'].includes(initStatus)) {
    return 'from-cyan-400/70 via-cyan-400/35 to-cyan-400/10'
  }
  if (['blocked', 'failed', 'paused', 'stalled'].includes(initStatus)) {
    return 'from-amber-400/60 via-amber-400/30 to-amber-400/10'
  }
  return 'from-slate-600 via-slate-700/60 to-slate-800/30'
}

function queueUnitClass(unit) {
  if (unit?.isFocus) {
    return 'border-cyan-400/40 bg-cyan-500/10'
  }
  const status = normalizeRoleStateKey(unit?.status)
  if (status === 'running' || status === 'starting') {
    return 'border-cyan-500/25 bg-cyan-500/10'
  }
  if (status === 'ready') {
    return 'border-blue-500/25 bg-blue-500/10'
  }
  if (unit?.status === 'waiting' || unit?.stageStatus === 'waiting') {
    return 'border-slate-500/25 bg-slate-500/10'
  }
  if (status === 'paused') {
    return 'border-amber-500/25 bg-amber-500/10'
  }
  if (unit?.stageStatus === 'blocked' || status === 'failed') {
    return 'border-rose-500/25 bg-rose-500/10'
  }
  return 'border-white/10 bg-slate-950/50'
}

function queueUnitPrimary(unit) {
  if (!unit) return '--'
  if (unit.role === 'planner') return roleLabel('planner')
  return [unit.stageLabel, unit.roleLabel].filter(Boolean).join(' / ') || unit.title || '--'
}

function queueUnitSecondary(unit) {
  if (!unit) return '--'
  if (unit.role === 'planner') return unit.title || '--'
  return unit.title || unit.subtaskId || '--'
}

function queueUnitBadgeClass(unit) {
  if (!unit) return stateBadgeClass('not_started')
  if (!unit.role || unit.status === 'waiting' || unit.status === 'blocked' || unit.status === 'failed') {
    return stateBadgeClass(unit.status)
  }
  if (unit.role === 'planner') {
    return stateBadgeClass(unit.status)
  }
  return roleBadgeClass(unit.status)
}

function queueUnitStatusLabel(unit) {
  if (!unit) return '--'
  if (!unit.role || unit.status === 'waiting' || unit.status === 'blocked' || unit.status === 'failed') {
    return stageStateLabel(unit.status)
  }
  return unit.role === 'planner' ? stageStateLabel(unit.status) : roleStateLabel(unit.status)
}

function roleQueueLoadLabel(queue) {
  return `${queue?.active?.length || 0}/${Number(queue?.parallelism || 0)}`
}

function activityToneClass(tone) {
  switch (tone) {
    case 'success':
      return 'text-emerald-300 bg-emerald-500/10 border-emerald-500/25'
    case 'warn':
      return 'text-amber-200 bg-amber-500/10 border-amber-500/25'
    case 'error':
      return 'text-rose-200 bg-rose-500/10 border-rose-500/25'
    default:
      return 'text-cyan-200 bg-cyan-500/10 border-cyan-500/25'
  }
}

function activityIcon(tone) {
  switch (tone) {
    case 'success':
      return CheckCircle2
    case 'warn':
      return AlertTriangle
    case 'error':
      return XCircle
    default:
      return Bot
  }
}

function formatFindingSummary(item) {
  return findingSummary(item, props.t('common.noDescription'))
}

function formatPayload(payload) {
  if (!payload) return '{}'
  try {
    return JSON.stringify(payload, null, 2)
  } catch {
    return '{}'
  }
}

function shortRunId(value) {
  return value ? String(value).slice(0, 10) : '--'
}

function formatStageList(stages = []) {
  if (!Array.isArray(stages) || stages.length === 0) {
    return '--'
  }
  return stages.map((stage) => stageLabel(stage)).join(' / ')
}

function startActionLabel() {
  if (props.startPending) {
    return props.locale === 'en' ? 'Starting...' : '正在启动...'
  }
  if (String(props.task?.status || '').trim().toLowerCase() === 'pending') {
    return props.t('orchestration.start')
  }
  return props.locale === 'en' ? 'Select Stages to Rerun' : '选择重跑阶段'
}

function normalizeStateKey(value) {
  const normalized = String(value || '').trim().toLowerCase()
  switch (normalized) {
    case 'starting':
      return 'starting'
    case 'running':
    case 'blocked':
    case 'stalled':
    case 'completed':
    case 'failed':
    case 'paused':
    case 'waiting':
      return normalized
    default:
      return 'not_started'
  }
}

function normalizeRoleStateKey(value) {
  const normalized = String(value || '').trim().toLowerCase()
  switch (normalized) {
    case 'starting':
      return 'starting'
    case 'running':
    case 'ready':
    case 'paused':
    case 'skipped':
    case 'completed':
    case 'failed':
      return normalized
    default:
      return 'pending'
  }
}

watch(
  () => props.taskId,
  async (taskId) => {
    closeRealtime()
    snapshot.value = createEmptySnapshot()
    error.value = ''
    lastSequence.value = 0
    selectedStage.value = 'init'
    stageSelectionMode.value = 'auto'
    expandedActivities.value = {}
    if (!taskId) return
    await loadSnapshot()
    connectEvents()
  },
  { immediate: true },
)

watch(
  () => props.authToken,
  () => {
    if (!props.taskId) return
    connectEvents()
  },
)

watch(
  () => [runSummary.value?.run?.status, diagnostics.value?.current_stage, focusEntries.value.map((node) => `${node.stage}:${node.status}`).join('|')],
  () => {
    syncSelectedStage()
    syncPolling()
  },
)

watch(
  () => props.task?.status,
  (status, previousStatus) => {
    if (!props.taskId || status === previousStatus) return
    loadSnapshot(true)
  },
)

onBeforeUnmount(() => {
  closeRealtime()
})
</script>

<template>
  <section class="glass-panel rounded-3xl border border-cyan-500/15 p-6 space-y-6">
    <div class="flex flex-col xl:flex-row xl:items-start xl:justify-between gap-5">
      <div>
        <div class="flex items-center gap-2 text-cyan-300 text-xs uppercase tracking-[0.28em]">
          <Radar class="w-4 h-4" />
          {{ t('orchestration.title') }}
        </div>
        <h2 class="mt-3 text-2xl font-bold text-white">{{ t('orchestration.title') }}</h2>
        <p class="mt-2 text-sm text-slate-400 max-w-3xl">{{ t('orchestration.subtitle') }}</p>
      </div>

      <div class="flex flex-wrap gap-3">
        <button
          type="button"
          class="px-4 py-2.5 rounded-xl border border-white/10 bg-white/5 hover:bg-white/10 text-slate-100 text-sm font-semibold transition-colors inline-flex items-center gap-2"
          @click="loadSnapshot()"
        >
          <RefreshCw class="w-4 h-4" />
          {{ t('orchestration.refresh') }}
        </button>
        <button
          v-if="canWrite"
          type="button"
          class="px-4 py-2.5 rounded-xl border border-cyan-500/30 bg-cyan-500 text-slate-950 text-sm font-bold shadow-[0_0_24px_rgba(34,211,238,0.28)] disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center gap-2"
          :disabled="startPending || task?.status === 'running'"
          @click="requestStart"
        >
          <Play class="w-4 h-4" />
          {{ startActionLabel() }}
        </button>
      </div>
    </div>

    <div v-if="error" class="rounded-2xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
      {{ error }}
    </div>

    <div v-if="isLoading" class="rounded-2xl border border-white/10 bg-white/5 px-4 py-6 text-sm text-slate-300">
      {{ t('orchestration.loading') }}
    </div>

    <template v-else>
      <div v-if="stageIssueSummaries.length" class="rounded-2xl border border-rose-500/20 bg-rose-500/5 p-4">
        <div class="flex items-center gap-2 text-rose-200">
          <ShieldAlert class="w-4 h-4" />
          <h3 class="text-sm font-bold uppercase tracking-[0.18em]">{{ t('orchestration.stageIssuesTitle') }}</h3>
        </div>

        <div class="mt-3 grid gap-3">
          <button
            v-for="summary in stageIssueSummaries"
            :key="summary.key"
            type="button"
            class="group rounded-xl border border-rose-500/20 bg-black/25 px-4 py-3 text-left transition-colors hover:bg-rose-500/10"
            @click="openStageConsole(summary.view)"
          >
            <div class="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-semibold text-white">{{ summary.label }}</span>
                  <span :class="['rounded-full border px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide', stageIssueStatusClass(summary.status)]">
                    {{ summary.statusLabel }}
                  </span>
                </div>
                <p class="mt-2 text-sm text-rose-100 break-words">{{ summary.message }}</p>
                <div v-if="summary.updatedAt" class="mt-2 text-xs text-slate-500">{{ summary.updatedAt }}</div>
              </div>

              <span class="inline-flex shrink-0 items-center gap-1 text-xs font-semibold text-rose-200 transition-colors group-hover:text-white">
                {{ t('orchestration.viewStageLogs') }}
                <ChevronRight class="w-3 h-3" />
              </span>
            </div>
          </button>
        </div>
      </div>

      <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
        <div class="flex items-start justify-between gap-4 flex-wrap">
          <div>
            <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
              <Route class="w-4 h-4 text-cyan-300" />
              {{ t('orchestration.flowTitle') }}
            </div>
            <p class="mt-2 max-w-3xl text-sm text-slate-400">{{ t('orchestration.flowSubtitle') }}</p>
          </div>

          <div class="flex flex-wrap items-center gap-2">
            <span :class="['px-2.5 py-1 rounded-full border text-xs font-semibold uppercase', stateBadgeClass(diagnostics?.focus_status || runSummary?.run?.status)]">
              {{ stageStateLabel(diagnostics?.focus_status || runSummary?.run?.status) }}
            </span>
            <button
              type="button"
              class="px-3 py-1.5 rounded-full border text-xs font-semibold transition-colors bg-white/5 text-slate-300 border-white/10 hover:bg-white/10"
              @click="resetStageFocus"
            >
              {{ t('orchestration.resetFocus') }}
            </button>
          </div>
        </div>

        <div class="mt-5 grid grid-cols-2 md:grid-cols-5 gap-3">
          <div class="rounded-2xl border border-white/10 bg-slate-950/45 px-4 py-3">
            <div class="text-[11px] uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.runId') }}</div>
            <div class="mt-1 font-mono text-sm font-semibold text-white">{{ shortRunId(runSummary?.run?.id) }}</div>
          </div>
          <div class="rounded-2xl border border-white/10 bg-slate-950/45 px-4 py-3">
            <div class="text-[11px] uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.flow.activeStages') }}</div>
            <div class="mt-1 text-sm font-semibold text-white">{{ orchestrationFlow.activeStageCount }}</div>
          </div>
          <div class="rounded-2xl border border-white/10 bg-slate-950/45 px-4 py-3">
            <div class="text-[11px] uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.flow.readyUnits') }}</div>
            <div class="mt-1 text-sm font-semibold text-white">{{ orchestrationFlow.queuedUnitCount }}</div>
          </div>
          <div class="rounded-2xl border border-white/10 bg-slate-950/45 px-4 py-3">
            <div class="text-[11px] uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.flow.waitingUnits') }}</div>
            <div class="mt-1 text-sm font-semibold text-white">{{ orchestrationFlow.waitingUnitCount }}</div>
          </div>
          <div class="rounded-2xl border border-white/10 bg-slate-950/45 px-4 py-3">
            <div class="text-[11px] uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.completed') }}</div>
            <div class="mt-1 text-sm font-semibold text-white">{{ orchestrationFlow.completedStageCount }} / {{ orchestrationFlow.totalStageCount }}</div>
          </div>
        </div>

        <div v-if="runScopeDetails" class="mt-4 rounded-2xl border border-cyan-500/20 bg-cyan-500/5 px-4 py-3 text-sm text-slate-200">
          <span class="font-semibold text-cyan-200">{{ t('orchestration.flow.rerunScope') }}</span>
          <span class="ml-2 text-slate-400">{{ t('orchestration.flow.selectedStages') }}</span>
          <span class="ml-1 text-white">{{ runScopeDetails.selected }}</span>
          <span class="mx-2 text-slate-600">/</span>
          <span class="text-slate-400">{{ t('orchestration.flow.carriedStages') }}</span>
          <span class="ml-1 text-white">{{ runScopeDetails.carried }}</span>
        </div>

        <div class="mt-5 grid grid-cols-1 2xl:grid-cols-[minmax(0,1.35fr)_minmax(360px,0.65fr)] gap-5">
          <div class="rounded-3xl border border-white/10 bg-slate-950/35 p-4">
            <button
              type="button"
              :class="flowNodeClass(orchestrationFlow.init)"
              @click="chooseStage(orchestrationFlow.init.stage)"
            >
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="rounded-full border border-cyan-500/25 bg-cyan-500/10 px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-cyan-200">
                      {{ t('orchestration.initGateLabel') }}
                    </span>
                    <span v-if="orchestrationFlow.init.isCurrent" class="rounded-full border border-white/10 bg-white/5 px-2.5 py-1 text-[10px] font-semibold uppercase tracking-[0.16em] text-slate-300">
                      {{ t('orchestration.currentStageChip') }}
                    </span>
                  </div>
                  <div class="mt-3 text-xl font-semibold text-white truncate">{{ orchestrationFlow.init.fullLabel }}</div>
                </div>
                <span :class="['shrink-0 rounded-full border px-2.5 py-1 text-xs font-semibold uppercase', stateBadgeClass(orchestrationFlow.init.status)]">
                  {{ stageStateLabel(orchestrationFlow.init.status) }}
                </span>
              </div>

              <div class="mt-4 h-1.5 overflow-hidden rounded-full bg-white/10">
                <div class="h-full rounded-full bg-cyan-300 transition-all" :style="flowProgressStyle(orchestrationFlow.init)" />
              </div>

              <div class="mt-4 grid grid-cols-4 gap-2 text-xs">
                <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                  <div class="text-slate-500">{{ t('orchestration.flow.running') }}</div>
                  <div class="mt-1 font-semibold text-white">{{ orchestrationFlow.init.activeCount }}</div>
                </div>
                <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                  <div class="text-slate-500">{{ t('orchestration.flow.ready') }}</div>
                  <div class="mt-1 font-semibold text-white">{{ orchestrationFlow.init.readyCount }}</div>
                </div>
                <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                  <div class="text-slate-500">{{ t('orchestration.flow.waiting') }}</div>
                  <div class="mt-1 font-semibold text-white">{{ orchestrationFlow.init.waitingCount }}</div>
                </div>
                <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                  <div class="text-slate-500">{{ t('orchestration.flow.done') }}</div>
                  <div class="mt-1 font-semibold text-white">{{ orchestrationFlow.init.completedCount }} / {{ orchestrationFlow.init.subtaskCount }}</div>
                </div>
              </div>
            </button>

            <div class="flex justify-center py-4">
              <div :class="['h-12 w-px bg-gradient-to-b', flowConnectorClass()]" />
            </div>

            <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-4 gap-3">
              <button
                v-for="node in orchestrationFlow.auditStages"
                :key="node.stage"
                type="button"
                :class="flowNodeClass(node)"
                @click="chooseStage(node.stage)"
              >
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0">
                    <div class="flex items-center gap-2 flex-wrap">
                      <span class="text-base font-semibold text-white">{{ node.label }}</span>
                      <span v-if="node.isCurrent" class="rounded-full border border-white/10 bg-white/5 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-slate-300">
                        {{ t('orchestration.currentStageChip') }}
                      </span>
                      <span v-if="node.locked" class="rounded-full border border-slate-600/50 bg-slate-800/50 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.16em] text-slate-400">
                        {{ t('orchestration.flow.locked') }}
                      </span>
                    </div>
                  </div>
                  <span :class="['shrink-0 rounded-full border px-2 py-0.5 text-[11px] font-semibold uppercase', stateBadgeClass(node.status)]">
                    {{ stageStateLabel(node.status) }}
                  </span>
                </div>

                <div class="mt-4 h-1.5 overflow-hidden rounded-full bg-white/10">
                  <div class="h-full rounded-full bg-cyan-300 transition-all" :style="flowProgressStyle(node)" />
                </div>

                <div class="mt-4 grid grid-cols-4 gap-2 text-xs">
                  <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                    <div class="text-slate-500">{{ t('orchestration.flow.running') }}</div>
                    <div class="mt-1 font-semibold text-white">{{ node.activeCount }}</div>
                  </div>
                  <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                    <div class="text-slate-500">{{ t('orchestration.flow.ready') }}</div>
                    <div class="mt-1 font-semibold text-white">{{ node.readyCount }}</div>
                  </div>
                  <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                    <div class="text-slate-500">{{ t('orchestration.flow.waiting') }}</div>
                    <div class="mt-1 font-semibold text-white">{{ node.waitingCount }}</div>
                  </div>
                  <div class="rounded-xl border border-white/10 bg-black/25 px-3 py-2">
                    <div class="text-slate-500">{{ t('orchestration.flow.done') }}</div>
                    <div class="mt-1 font-semibold text-white">{{ node.completedCount }} / {{ node.subtaskCount }}</div>
                  </div>
                </div>
              </button>
            </div>
          </div>

          <div class="space-y-4">
            <div class="rounded-xl border border-white/10 bg-slate-950/35 p-4">
              <div class="flex items-center justify-between gap-3">
                <div class="text-sm font-semibold text-white">{{ t('orchestration.queue.activeNow') }}</div>
                <span class="rounded-full border border-cyan-500/25 bg-cyan-500/10 px-2.5 py-1 text-xs font-semibold text-cyan-200">
                  {{ executionQueues.totals.active }}
                </span>
              </div>

              <div class="mt-3 space-y-2">
                <div
                  v-for="unit in activeExecutionUnits"
                  :key="unit.id"
                  :class="['rounded-2xl border px-3 py-3', queueUnitClass(unit)]"
                >
                  <div class="flex items-start justify-between gap-3">
                    <div class="min-w-0">
                      <div class="text-sm font-semibold text-white truncate">{{ queueUnitPrimary(unit) }}</div>
                      <div class="mt-1 text-xs text-slate-400 truncate">{{ queueUnitSecondary(unit) }}</div>
                    </div>
                    <span :class="['shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase', queueUnitBadgeClass(unit)]">
                      {{ queueUnitStatusLabel(unit) }}
                    </span>
                  </div>
                </div>

                <div v-if="!activeExecutionUnits.length" class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-6 text-sm text-slate-400">
                  {{ t('orchestration.queue.emptyActive') }}
                </div>
              </div>
            </div>

            <div class="rounded-xl border border-white/10 bg-slate-950/35 p-4">
              <div class="flex items-center justify-between gap-3">
                <div class="text-sm font-semibold text-white">{{ t('orchestration.queue.readyQueue') }}</div>
                <span class="rounded-full border border-blue-500/25 bg-blue-500/10 px-2.5 py-1 text-xs font-semibold text-blue-200">
                  {{ executionQueues.totals.ready }}
                </span>
              </div>

              <div class="mt-3 space-y-2 max-h-[360px] overflow-auto pr-1">
                <div
                  v-for="unit in readyExecutionUnits"
                  :key="unit.id"
                  :class="['rounded-2xl border px-3 py-3', queueUnitClass(unit)]"
                >
                  <div class="flex items-start gap-3">
                    <div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-blue-500/25 bg-blue-500/10 text-xs font-bold text-blue-200">
                      {{ unit.position }}
                    </div>
                    <div class="min-w-0 flex-1">
                      <div class="flex items-center justify-between gap-3">
                        <div class="text-sm font-semibold text-white truncate">{{ queueUnitPrimary(unit) }}</div>
                        <span :class="['shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase', queueUnitBadgeClass(unit)]">
                          {{ queueUnitStatusLabel(unit) }}
                        </span>
                      </div>
                      <div class="mt-1 text-xs text-slate-400 truncate">{{ queueUnitSecondary(unit) }}</div>
                    </div>
                  </div>
                </div>

                <div v-if="!readyExecutionUnits.length" class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-6 text-sm text-slate-400">
                  {{ t('orchestration.queue.emptyReady') }}
                </div>
              </div>
            </div>

            <div class="rounded-xl border border-white/10 bg-slate-950/35 p-4">
              <div class="flex items-center justify-between gap-3">
                <div class="text-sm font-semibold text-white">{{ t('orchestration.queue.waitingQueue') }}</div>
                <span class="rounded-full border border-slate-500/25 bg-slate-500/10 px-2.5 py-1 text-xs font-semibold text-slate-200">
                  {{ executionQueues.totals.waiting }}
                </span>
              </div>

              <div class="mt-3 space-y-2 max-h-[300px] overflow-auto pr-1">
                <div
                  v-for="unit in waitingExecutionUnits"
                  :key="unit.id"
                  :class="['rounded-2xl border px-3 py-3', queueUnitClass(unit)]"
                >
                  <div class="flex items-start gap-3">
                    <div class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border border-slate-500/25 bg-slate-500/10 text-xs font-bold text-slate-200">
                      {{ unit.position }}
                    </div>
                    <div class="min-w-0 flex-1">
                      <div class="flex items-center justify-between gap-3">
                        <div class="text-sm font-semibold text-white truncate">{{ queueUnitPrimary(unit) }}</div>
                        <span :class="['shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase', queueUnitBadgeClass(unit)]">
                          {{ queueUnitStatusLabel(unit) }}
                        </span>
                      </div>
                      <div class="mt-1 text-xs text-slate-400 truncate">{{ queueUnitSecondary(unit) }}</div>
                    </div>
                  </div>
                </div>

                <div v-if="!waitingExecutionUnits.length" class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-6 text-sm text-slate-400">
                  {{ t('orchestration.queue.emptyWaiting') }}
                </div>
              </div>
            </div>

            <div class="rounded-xl border border-white/10 bg-slate-950/35 p-4">
              <div class="flex items-center justify-between gap-3">
                <div class="text-sm font-semibold text-white">{{ t('orchestration.queue.blockedQueue') }}</div>
                <span class="rounded-full border border-rose-500/25 bg-rose-500/10 px-2.5 py-1 text-xs font-semibold text-rose-200">
                  {{ executionQueues.totals.blocked }}
                </span>
              </div>
              <div class="mt-3 space-y-2 max-h-[280px] overflow-auto pr-1">
                <div
                  v-for="unit in blockedExecutionUnits"
                  :key="unit.id"
                  :class="['rounded-2xl border px-3 py-3', queueUnitClass(unit)]"
                >
                  <div class="flex items-start justify-between gap-3">
                    <div class="min-w-0">
                      <div class="text-sm font-semibold text-white truncate">{{ queueUnitPrimary(unit) }}</div>
                      <div class="mt-1 max-h-10 overflow-hidden text-xs text-rose-100 break-words">{{ unit.errorMessage || unit.blockedReason || t('orchestration.queue.noBlockedReason') }}</div>
                    </div>
                    <span :class="['shrink-0 rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase', queueUnitBadgeClass(unit)]">
                      {{ queueUnitStatusLabel(unit) }}
                    </span>
                  </div>
                </div>

                <div v-if="!blockedExecutionUnits.length" class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-6 text-sm text-slate-400">
                  {{ t('orchestration.queue.emptyBlocked') }}
                </div>
              </div>
            </div>

            <div class="rounded-xl border border-white/10 bg-slate-950/35 p-4">
              <div class="text-sm font-semibold text-white">{{ t('orchestration.queue.roleLoads') }}</div>
              <div class="mt-3 grid grid-cols-2 gap-2">
                <div
                  v-for="roleQueue in executionQueues.roles"
                  :key="roleQueue.role"
                  class="rounded-2xl border border-white/10 bg-black/25 px-3 py-3"
                >
                  <div class="text-xs text-slate-500">{{ roleQueue.label }}</div>
                  <div class="mt-1 text-sm font-semibold text-white">{{ roleQueueLoadLabel(roleQueue) }}</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 xl:grid-cols-[0.92fr_1.08fr] gap-5">
        <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
          <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
            <Clock3 class="w-4 h-4 text-cyan-300" />
            {{ t('orchestration.diagnosticsTitle') }}
          </div>

          <template v-if="runSummary && diagnostics">
            <div class="mt-4 flex items-start justify-between gap-4">
              <div>
                <div class="text-lg font-semibold text-white">{{ diagnostics.current_stage ? stageLabel(diagnostics.current_stage) : t('orchestration.noFocusedStage') }}</div>
                <div class="mt-2 text-sm text-slate-400">
                  {{ diagnostics.current_role ? roleLabel(diagnostics.current_role) : t('orchestration.noFocusedRole') }}
                </div>
              </div>
              <span :class="['px-2.5 py-1 rounded-full border text-xs font-semibold uppercase', stateBadgeClass(diagnostics.focus_status)]">
                {{ stageStateLabel(diagnostics.focus_status) }}
              </span>
            </div>

            <div class="mt-5 grid grid-cols-1 md:grid-cols-2 gap-4 text-sm">
              <div v-if="diagnostics.stalled" class="rounded-2xl border border-amber-500/25 bg-amber-500/10 p-4 md:col-span-2">
                <div class="text-xs uppercase tracking-[0.18em] text-amber-200">{{ t('orchestration.longNoProgress') }}</div>
                <div class="mt-2 text-white font-semibold">{{ formatDuration(diagnostics.focus_silence_seconds) }}</div>
                <div class="mt-1 text-xs text-amber-100">
                  {{ t('orchestration.stallThreshold', { value: formatDuration(diagnostics.stall_threshold_seconds) }) }}
                </div>
              </div>
              <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
                <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.focusSubtask') }}</div>
                <div class="mt-2 text-white font-semibold">{{ diagnostics.focus_subtask_title || '--' }}</div>
                <div class="mt-1 text-xs text-slate-500 font-mono">{{ diagnostics.focus_subtask_id || '--' }}</div>
                <div v-if="diagnostics.active_agent_run_id" class="mt-1 text-xs text-slate-500 font-mono">{{ diagnostics.active_agent_run_id }}</div>
              </div>
              <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
                <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.focusReason') }}</div>
                <div class="mt-2 text-white font-semibold">{{ diagnostics.focus_reason ? t(`orchestration.focusReasonLabel.${diagnostics.focus_reason}`) : '--' }}</div>
              </div>
              <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
                <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.blockedOrFailedReason') }}</div>
                <div class="mt-2 text-slate-200">{{ diagnostics.error_message || diagnostics.blocked_reason || '--' }}</div>
              </div>
              <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
                <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.latestEvent') }}</div>
                <div class="mt-2 text-white font-semibold">{{ diagnostics.latest_event_type || '--' }}</div>
                <div class="mt-1 text-xs text-slate-400">{{ diagnostics.latest_event_message || '--' }}</div>
                <div class="mt-2 text-xs text-slate-500">{{ formatDate(diagnostics.latest_event_at) }}</div>
              </div>
            </div>
          </template>

          <div v-else class="mt-4 rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-8 text-sm text-slate-400">
            {{ t('orchestration.noRun') }}
          </div>
        </div>

        <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
          <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
            <ShieldCheck class="w-4 h-4 text-cyan-300" />
            {{ t('orchestration.recordsTitle') }}
          </div>

          <div class="mt-4 grid grid-cols-2 gap-3">
            <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
              <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('common.routes') }}</div>
              <div class="mt-2 text-lg font-semibold text-white">{{ selectedStageRoutes.length }}</div>
              <div class="mt-1 text-xs text-slate-400">{{ selectedStageNode?.fullLabel || '--' }}</div>
            </div>
            <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
              <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('common.findings') }}</div>
              <div class="mt-2 text-lg font-semibold text-white">{{ selectedStageFindings.length }}</div>
              <div class="mt-1 text-xs text-slate-400">{{ selectedStageNode?.fullLabel || '--' }}</div>
            </div>
          </div>

          <div class="mt-4 space-y-4 max-h-[340px] overflow-auto pr-1">
            <template v-if="selectedStageRoutes.length || selectedStageFindings.length">
              <div v-if="selectedStageRoutes.length" class="space-y-3">
                <div class="text-sm font-semibold text-white">{{ t('common.routes') }}</div>
                <div
                  v-for="route in selectedStageRoutes.slice(0, 10)"
                  :key="route.id"
                  class="rounded-2xl border border-white/10 bg-slate-950/50 p-4"
                >
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="px-2 py-0.5 rounded-full border border-cyan-500/25 bg-cyan-500/10 text-cyan-200 text-xs font-semibold">
                      {{ route.method }}
                    </span>
                    <span class="text-sm font-semibold text-white font-mono break-all">{{ route.path }}</span>
                  </div>
                  <div class="mt-2 text-xs text-slate-400">
                    {{ t('common.sourceFile') }}: {{ route.source_file || '--' }}
                  </div>
                </div>
              </div>

              <div v-if="selectedStageFindings.length" class="space-y-3">
                <div class="text-sm font-semibold text-white">{{ t('common.findings') }}</div>
                <div
                  v-for="finding in selectedStageFindings.slice(0, 12)"
                  :key="finding.id"
                  class="rounded-2xl border border-white/10 bg-slate-950/50 p-4"
                >
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="px-2 py-0.5 rounded-full border border-white/10 text-xs font-semibold uppercase text-slate-200">
                      {{ finding.reviewed_severity || finding.severity || 'HIGH' }}
                    </span>
                    <span class="text-white font-semibold">{{ finding.subtype || finding.type || t('common.noDescription') }}</span>
                  </div>
                  <div class="mt-2 text-sm text-slate-300">
                    {{ formatFindingSummary(finding) }}
                  </div>
                </div>
              </div>
            </template>

            <div v-else class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-8 text-sm text-slate-400">
              {{ t('orchestration.noRecords') }}
            </div>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 2xl:grid-cols-[1.04fr_0.96fr] gap-5">
        <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
          <div class="flex items-center justify-between gap-4 flex-wrap">
            <div>
              <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
                <ListTree class="w-4 h-4 text-cyan-300" />
                {{ t('orchestration.stageDetailsTitle') }}
              </div>
              <div class="mt-2 flex items-center gap-3 flex-wrap">
                <div class="text-lg font-semibold text-white">{{ selectedStageNode?.fullLabel || t('orchestration.noFocusedStage') }}</div>
                <span :class="['px-2.5 py-1 rounded-full border text-xs font-semibold uppercase', stateBadgeClass(selectedStageNode?.status)]">
                  {{ stageStateLabel(selectedStageNode?.status) }}
                </span>
              </div>
            </div>
            <div class="text-sm text-slate-400">
              {{ diagnostics?.current_stage === selectedStage ? t('orchestration.currentStageMarker') : t('orchestration.filteredStageMarker') }}
            </div>
          </div>

          <div class="mt-5 space-y-4 max-h-[620px] overflow-auto pr-1">
            <template v-if="selectedStageSubtasks.length">
              <div
                v-for="subtask in selectedStageSubtasks"
                :key="subtask.id"
                class="rounded-2xl border border-white/10 bg-slate-950/50 p-4"
              >
                <div class="flex items-start justify-between gap-4">
                  <div>
                    <div class="text-base font-semibold text-white">{{ subtask.title }}</div>
                    <div class="mt-1 text-xs text-slate-500 font-mono">{{ subtask.id }}</div>
                  </div>
                  <span :class="['px-2.5 py-1 rounded-full border text-[11px] font-semibold uppercase', stateBadgeClass(resolveSubtaskDisplayStatus(subtask))]">
                    {{ stageStateLabel(resolveSubtaskDisplayStatus(subtask)) }}
                  </span>
                </div>

                <div class="mt-4 flex flex-wrap items-center gap-2">
                  <template v-for="(step, index) in buildRolePipeline(subtask, t)" :key="`${subtask.id}-${step.role}`">
                    <div class="rounded-2xl border px-3 py-3 min-w-[130px]" :class="roleBadgeClass(step.status)">
                      <div class="text-[11px] uppercase tracking-[0.18em]">{{ step.label }}</div>
                      <div class="mt-1 text-sm font-semibold">{{ roleStateLabel(step.status) }}</div>
                    </div>
                    <div v-if="index < 3" class="text-slate-600 text-xs font-semibold uppercase tracking-[0.2em]">/</div>
                  </template>
                </div>

                <div v-if="subtask.blocked_reason || subtask.error_message" class="mt-4 rounded-2xl border border-white/10 bg-black/25 px-4 py-3 text-sm text-slate-300">
                  {{ subtask.error_message || subtask.blocked_reason }}
                </div>
              </div>
            </template>

            <div v-else class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-8 text-sm text-slate-400">
              {{ t('orchestration.noStageDetails') }}
            </div>
          </div>
        </div>

        <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
          <div class="flex items-center justify-between gap-4 flex-wrap">
            <div>
              <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
                <Activity class="w-4 h-4 text-cyan-300" />
                {{ t('orchestration.activitiesTitle') }}
              </div>
              <p class="mt-2 text-sm text-slate-400">{{ t('orchestration.activitiesSubtitle') }}</p>
            </div>

            <div class="flex flex-wrap gap-2">
              <button
                type="button"
                :class="[
                  'px-3 py-1.5 rounded-full border text-xs font-semibold transition-colors',
                  activityScope === 'selected'
                    ? 'bg-cyan-500/15 text-cyan-200 border-cyan-500/30'
                    : 'bg-white/5 text-slate-300 border-white/10 hover:bg-white/10',
                ]"
                @click="activityScope = 'selected'"
              >
                {{ t('orchestration.filters.currentStage') }}
              </button>
              <button
                type="button"
                :class="[
                  'px-3 py-1.5 rounded-full border text-xs font-semibold transition-colors',
                  activityScope === 'all'
                    ? 'bg-cyan-500/15 text-cyan-200 border-cyan-500/30'
                    : 'bg-white/5 text-slate-300 border-white/10 hover:bg-white/10',
                ]"
                @click="activityScope = 'all'"
              >
                {{ t('orchestration.filters.allStages') }}
              </button>
            </div>
          </div>

          <div class="mt-4 space-y-3 max-h-[620px] overflow-auto pr-1">
            <div
              v-for="activity in visibleActivities"
              :key="activity.id"
              class="rounded-2xl border border-white/10 bg-slate-950/50 px-4 py-4"
            >
              <div class="flex items-start justify-between gap-4">
                <div class="flex items-start gap-3 min-w-0">
                  <div :class="['rounded-2xl border p-2 shrink-0', activityToneClass(activity.tone)]">
                    <component :is="activityIcon(activity.tone)" class="w-4 h-4" />
                  </div>
                  <div class="min-w-0">
                    <div class="text-sm font-semibold text-white break-words">{{ activity.headline }}</div>
                    <div v-if="activity.summary" class="mt-1 text-sm text-slate-300 break-words">{{ activity.summary }}</div>
                    <div class="mt-2 flex items-center gap-3 flex-wrap text-[11px] text-slate-500">
                      <span>{{ formatDate(activity.createdAt) }}</span>
                      <span v-if="activity.stage">{{ stageLabel(activity.stage) }}</span>
                      <span v-if="activity.role">{{ roleLabel(activity.role) }}</span>
                    </div>
                  </div>
                </div>

                <button
                  type="button"
                  class="text-slate-400 hover:text-white transition-colors shrink-0"
                  @click="toggleActivity(activity.id)"
                >
                  <ChevronUp v-if="expandedActivities[activity.id]" class="w-4 h-4" />
                  <ChevronDown v-else class="w-4 h-4" />
                </button>
              </div>

              <div
                v-if="expandedActivities[activity.id]"
                class="mt-4 rounded-2xl border border-white/10 bg-black/25 p-4 space-y-3 text-sm"
              >
                <div>
                  <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.rawEventType') }}</div>
                  <div class="mt-1 font-mono text-cyan-200 break-all">{{ activity.eventType }}</div>
                </div>
                <div>
                  <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.rawEventMessage') }}</div>
                  <div class="mt-1 text-slate-200 break-words">{{ activity.rawMessage || '--' }}</div>
                </div>
                <div>
                  <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.rawPayload') }}</div>
                  <pre class="mt-1 rounded-xl border border-white/10 bg-slate-950/80 p-3 text-xs text-slate-200 overflow-x-auto">{{ formatPayload(activity.payload) }}</pre>
                </div>
              </div>
            </div>

            <div v-if="!visibleActivities.length" class="rounded-2xl border border-dashed border-white/10 bg-black/20 px-4 py-8 text-sm text-slate-400">
              {{ t('orchestration.noEvents') }}
            </div>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>
