<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import axios from 'axios'
import {
  Activity,
  AlertTriangle,
  Bot,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Clock3,
  GitBranch,
  ListTree,
  Play,
  Radar,
  RefreshCw,
  Route,
  ShieldCheck,
  XCircle,
} from 'lucide-vue-next'

import {
  buildActivityFeed,
  buildInitStageCard,
  buildRolePipeline,
  buildStageMatrixRows,
  findingSummary,
  formatReplanReason,
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
})

const emit = defineEmits(['refresh-task'])

const POLL_INTERVAL_MS = 15000

const snapshot = ref(createEmptySnapshot())
const isLoading = ref(false)
const isStarting = ref(false)
const error = ref('')
const selectedStage = ref('init')
const stageSelectionMode = ref('auto')
const activityScope = ref('selected')
const expandedActivities = ref({})
const lastSequence = ref(0)
const matrixQuotaRoles = ['worker', 'integrator', 'validator', 'persistence']

let eventSource = null
let refreshTimer = null
let pollingTimer = null

const runSummary = computed(() => snapshot.value?.run || null)
const diagnostics = computed(() => snapshot.value?.diagnostics || null)
const initStageCard = computed(() => buildInitStageCard(snapshot.value, diagnostics.value, props.t))
const stageMatrixRows = computed(() => buildStageMatrixRows(snapshot.value, diagnostics.value, props.t))
const focusEntries = computed(() => [initStageCard.value, ...stageMatrixRows.value])
const parallelismBadges = computed(() => {
  return matrixQuotaRoles.map((role) => ({
    role,
    label: roleLabel(role),
    value: diagnostics.value?.parallelism?.[role],
  }))
})
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
      Authorization: props.authToken,
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
    setTimeout(() => {
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

async function startOrchestration() {
  if (!props.taskId || props.task?.status === 'running') return
  isStarting.value = true
  error.value = ''

  try {
    const { data } = await axios.post(`${props.apiUrl}/tasks/${props.taskId}/orchestration/start`, {}, authConfig())
    snapshot.value = normalizeSnapshot(data)
    lastSequence.value = 0
    stageSelectionMode.value = 'auto'
    syncSelectedStage()
    emit('refresh-task')
    await loadSnapshot(true)
    connectEvents()
  } catch (requestError) {
    error.value = requestError.response?.data?.error || requestError.message
  } finally {
    isStarting.value = false
  }
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
      return 'bg-blue-500/15 text-blue-200 border-blue-500/30'
    case 'blocked':
    case 'stalled':
    case 'paused':
      return 'bg-amber-500/15 text-amber-200 border-amber-500/30'
    case 'completed':
      return 'bg-emerald-500/15 text-emerald-200 border-emerald-500/30'
    case 'failed':
      return 'bg-rose-500/15 text-rose-200 border-rose-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border-slate-500/20'
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

function matrixRowClass(row) {
  if (selectedStage.value === row.stage) {
    return 'border-cyan-400/40 bg-cyan-500/10 shadow-[0_0_0_1px_rgba(34,211,238,0.18)]'
  }
  if (row?.isCurrent) {
    return 'border-white/15 bg-white/10'
  }
  return 'border-white/10 bg-slate-950/40 hover:bg-white/5'
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

function formatParallelism(value) {
  const numeric = Number(value)
  return Number.isFinite(numeric) && numeric >= 0 ? `x${numeric}` : 'x--'
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
          type="button"
          class="px-4 py-2.5 rounded-xl border border-cyan-500/30 bg-cyan-500 text-slate-950 text-sm font-bold shadow-[0_0_24px_rgba(34,211,238,0.28)] disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center gap-2"
          :disabled="isStarting || task?.status === 'running'"
          @click="startOrchestration"
        >
          <Play class="w-4 h-4" />
          {{ isStarting ? t('orchestration.starting') : t('orchestration.start') }}
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
      <div class="grid grid-cols-1 xl:grid-cols-[1.15fr_0.85fr] gap-5">
        <div class="rounded-2xl border border-white/10 bg-black/25 p-5">
          <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
            <GitBranch class="w-4 h-4 text-cyan-300" />
            {{ t('orchestration.summary') }}
          </div>

          <div class="mt-4 grid grid-cols-2 lg:grid-cols-4 gap-3">
            <div class="rounded-2xl border border-white/8 bg-white/5 p-4">
              <div class="text-xs text-slate-500 uppercase">{{ t('orchestration.runId') }}</div>
              <div class="mt-2 text-lg font-semibold text-white font-mono">{{ shortRunId(runSummary?.run?.id) }}</div>
            </div>
            <div class="rounded-2xl border border-white/8 bg-white/5 p-4">
              <div class="text-xs text-slate-500 uppercase">{{ t('orchestration.revision') }}</div>
              <div class="mt-2 text-lg font-semibold text-white">{{ runSummary?.planner_revision || 0 }}</div>
            </div>
            <div class="rounded-2xl border border-white/8 bg-white/5 p-4">
              <div class="text-xs text-slate-500 uppercase">{{ t('orchestration.activeSubtasks') }}</div>
              <div class="mt-2 text-lg font-semibold text-white">{{ runSummary?.active_subtask_count || 0 }}</div>
            </div>
            <div class="rounded-2xl border border-white/8 bg-white/5 p-4">
              <div class="text-xs text-slate-500 uppercase">{{ t('orchestration.completed') }}</div>
              <div class="mt-2 text-lg font-semibold text-white">{{ runSummary?.completed_count || 0 }}</div>
            </div>
          </div>
        </div>

        <div class="rounded-2xl border border-white/10 bg-black/25 p-5">
          <div class="flex items-center justify-between gap-3">
            <div class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('common.status') }}</div>
            <span :class="['px-2.5 py-1 rounded-full border text-xs font-semibold uppercase', stateBadgeClass(diagnostics?.focus_status || runSummary?.run?.status)]">
              {{ stageStateLabel(diagnostics?.focus_status || runSummary?.run?.status) }}
            </span>
          </div>

          <div class="mt-4 space-y-3 text-sm">
            <div class="flex items-center justify-between gap-4">
              <span class="text-slate-400">{{ t('orchestration.plannerPending') }}</span>
              <span class="text-white font-semibold">{{ diagnostics?.planner_pending ? t('orchestration.boolean.yes') : t('orchestration.boolean.no') }}</span>
            </div>
            <div class="flex items-center justify-between gap-4">
              <span class="text-slate-400">{{ t('orchestration.lastReplanReason') }}</span>
              <span class="text-white text-right">{{ diagnostics?.last_replan_reason ? formatReplanReason(diagnostics.last_replan_reason, t) : '--' }}</span>
            </div>
            <div class="flex items-center justify-between gap-4">
              <span class="text-slate-400">{{ t('orchestration.lastProgressAt') }}</span>
              <span class="text-white text-right">{{ formatDate(diagnostics?.last_progress_at) }}</span>
            </div>
            <div class="flex items-center justify-between gap-4">
              <span class="text-slate-400">{{ t('orchestration.silenceDuration') }}</span>
              <span class="text-white text-right">{{ formatDuration(diagnostics?.silence_seconds) }}</span>
            </div>
          </div>
        </div>
      </div>

      <div class="rounded-2xl border border-white/10 bg-black/20 p-5">
        <div class="flex items-center justify-between gap-4 flex-wrap">
          <div>
            <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
              <Route class="w-4 h-4 text-cyan-300" />
              {{ t('orchestration.overviewTitle') }}
            </div>
            <p class="mt-2 text-sm text-slate-400">{{ t('orchestration.overviewSubtitle') }}</p>
            <p class="mt-2 text-xs text-slate-500">{{ t('orchestration.matrixRoleHint') }}</p>
          </div>

          <button
            type="button"
            class="px-3 py-1.5 rounded-full border text-xs font-semibold transition-colors bg-white/5 text-slate-300 border-white/10 hover:bg-white/10"
            @click="resetStageFocus"
          >
            {{ t('orchestration.resetFocus') }}
          </button>
        </div>

        <div class="mt-5 flex flex-wrap items-center gap-2">
          <span class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('orchestration.parallelismLabel') }}</span>
          <span
            v-for="badge in parallelismBadges"
            :key="badge.role"
            class="inline-flex items-center gap-2 rounded-full border border-white/10 bg-white/5 px-3 py-1.5 text-xs font-semibold text-slate-200"
          >
            <span>{{ badge.label }}</span>
            <span class="text-cyan-200">{{ formatParallelism(badge.value) }}</span>
          </span>
          <span class="text-xs text-slate-500">
            {{ t('orchestration.parallelismPlanner', { value: formatParallelism(diagnostics?.parallelism?.planner) }) }}
          </span>
        </div>

        <div class="mt-6">
          <button
            type="button"
            :class="[
              'w-full rounded-3xl border px-5 py-5 text-left transition-all',
              selectedStage === 'init'
                ? 'border-cyan-400/40 bg-cyan-500/10 shadow-[0_0_0_1px_rgba(34,211,238,0.18)]'
                : 'border-white/10 bg-slate-950/45 hover:bg-white/5',
            ]"
            @click="chooseStage('init')"
          >
            <div class="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
              <div class="min-w-0">
                <div class="flex items-center gap-3 flex-wrap">
                  <span class="rounded-full border border-cyan-500/25 bg-cyan-500/10 px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.22em] text-cyan-200">
                    {{ t('orchestration.initGateLabel') }}
                  </span>
                  <span v-if="initStageCard.isCurrent" class="rounded-full border border-white/10 bg-white/5 px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-slate-300">
                    {{ t('orchestration.currentStageChip') }}
                  </span>
                </div>
                <div class="mt-3 text-xl font-semibold text-white">{{ initStageCard.fullLabel }}</div>
                <p class="mt-2 max-w-3xl text-sm text-slate-300">
                  {{ initStageCard.status === 'completed' ? t('orchestration.initGateUnlocked') : t('orchestration.initGateLocked') }}
                </p>
              </div>

              <div class="flex flex-wrap items-center gap-3">
                <span :class="['px-2.5 py-1 rounded-full border text-xs font-semibold uppercase', stateBadgeClass(initStageCard.status)]">
                  {{ stageStateLabel(initStageCard.status) }}
                </span>
                <div class="rounded-2xl border border-white/10 bg-black/25 px-4 py-3 text-xs text-slate-300">
                  {{ t('orchestration.provisional') }} {{ initStageCard.provisionalCount }}
                  <span class="mx-2 text-slate-600">/</span>
                  {{ t('orchestration.validated') }} {{ initStageCard.validatedCount }}
                </div>
              </div>
            </div>
          </button>

          <div class="flex justify-center py-3">
            <div class="h-10 w-px bg-gradient-to-b from-cyan-400/60 via-cyan-400/15 to-transparent" />
          </div>

          <div class="rounded-3xl border border-white/10 bg-slate-950/35">
            <div class="flex items-start justify-between gap-4 border-b border-white/10 px-5 py-4 flex-wrap">
              <div>
                <div class="text-sm font-semibold text-white">{{ t('orchestration.matrixTitle') }}</div>
                <div class="mt-1 text-xs text-slate-500">{{ t('orchestration.matrixSubtitle') }}</div>
              </div>
              <div class="text-xs text-slate-500">{{ t('orchestration.matrixFanoutHint') }}</div>
            </div>

            <div class="overflow-x-auto">
              <div class="min-w-[840px]">
                <div class="grid grid-cols-[1.2fr_repeat(5,minmax(0,0.78fr))] gap-3 px-5 py-3 text-[11px] uppercase tracking-[0.18em] text-slate-500">
                  <div>{{ t('orchestration.matrix.columns.stage') }}</div>
                  <div>{{ t('orchestration.matrix.columns.overall') }}</div>
                  <div>{{ t('orchestration.matrix.columns.worker') }}</div>
                  <div>{{ t('orchestration.matrix.columns.integrator') }}</div>
                  <div>{{ t('orchestration.matrix.columns.validator') }}</div>
                  <div>{{ t('orchestration.matrix.columns.persistence') }}</div>
                </div>

                <div class="space-y-2 px-3 pb-3">
                  <button
                    v-for="row in stageMatrixRows"
                    :key="row.stage"
                    type="button"
                    class="grid w-full grid-cols-[1.2fr_repeat(5,minmax(0,0.78fr))] gap-3 rounded-2xl border px-3 py-3 text-left transition-all"
                    :class="matrixRowClass(row)"
                    @click="chooseStage(row.stage)"
                  >
                    <div class="min-w-0">
                      <div class="flex items-center gap-3 flex-wrap">
                        <div class="text-sm font-semibold text-white">{{ row.label }}</div>
                        <span v-if="row.isCurrent" class="rounded-full border border-white/10 bg-white/5 px-2.5 py-0.5 text-[10px] font-semibold uppercase tracking-[0.18em] text-slate-300">
                          {{ t('orchestration.currentStageChip') }}
                        </span>
                      </div>
                      <div class="mt-1 text-xs text-slate-500">{{ row.fullLabel }}</div>
                    </div>

                    <div class="flex items-center">
                      <span :class="['inline-flex rounded-full border px-2.5 py-1 text-xs font-semibold uppercase', stateBadgeClass(row.status)]">
                        {{ stageStateLabel(row.status) }}
                      </span>
                    </div>

                    <div
                      v-for="cell in row.roleCells"
                      :key="`${row.stage}-${cell.role}`"
                      class="flex items-center"
                    >
                      <span :class="['inline-flex rounded-full border px-2.5 py-1 text-xs font-semibold uppercase', roleBadgeClass(cell.status)]">
                        {{ roleStateLabel(cell.status) }}
                      </span>
                    </div>
                  </button>
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
              <div class="rounded-2xl border border-white/10 bg-slate-950/50 p-4">
                <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('orchestration.focusSubtask') }}</div>
                <div class="mt-2 text-white font-semibold">{{ diagnostics.focus_subtask_title || '--' }}</div>
                <div class="mt-1 text-xs text-slate-500 font-mono">{{ diagnostics.focus_subtask_id || '--' }}</div>
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
