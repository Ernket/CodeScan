<script setup>
import { computed } from 'vue'
import {
  Activity,
  Building2,
  CheckCircle,
  ChevronRight,
  FileCode,
  FolderOpen,
  RefreshCw,
  Server,
  ShieldAlert,
  Trash2,
} from 'lucide-vue-next'

const props = defineProps({
  stats: {
    type: Object,
    required: true,
  },
  displayStats: {
    type: Object,
    required: true,
  },
  tasks: {
    type: Array,
    default: () => [],
  },
  organizations: {
    type: Array,
    default: () => [],
  },
  stageDefinitions: {
    type: Array,
    default: () => [],
  },
  loading: {
    type: Boolean,
    default: false,
  },
  selectedTaskId: {
    type: String,
    default: '',
  },
  selectedOrganizationId: {
    type: String,
    default: '',
  },
  locale: {
    type: String,
    default: 'zh',
  },
  t: {
    type: Function,
    required: true,
  },
  canDelete: {
    type: Boolean,
    default: false,
  },
})

const emit = defineEmits(['refresh', 'select-organization', 'open-task', 'delete-task'])

const statCards = computed(() => {
  const runningCount = props.tasks.filter(task => task.status === 'running').length
  const completedCount = props.tasks.filter(task => task.status === 'completed').length

  return [
    {
      key: 'projects',
      label: props.t('dashboard.totalProjects'),
      value: props.displayStats.projects || 0,
      detail: props.t('dashboard.runningCompleted', { running: runningCount, completed: completedCount }),
      icon: FileCode,
      panelClass: 'hover:border-cyan-400/40',
      glowClass: 'bg-cyan-500/10 group-hover:bg-cyan-500/20',
      iconClass: 'bg-cyan-500/10 text-cyan-300',
      chipClass: 'text-cyan-200',
    },
    {
      key: 'interfaces',
      label: props.t('dashboard.interfacesFound'),
      value: props.displayStats.interfaces || 0,
      detail: props.t('dashboard.routesIndexed', { count: formatCompactNumber(props.stats.interfaces || 0) }),
      icon: Server,
      panelClass: 'hover:border-indigo-400/40',
      glowClass: 'bg-indigo-500/10 group-hover:bg-indigo-500/20',
      iconClass: 'bg-indigo-500/10 text-indigo-300',
      chipClass: 'text-indigo-200',
    },
    {
      key: 'vulns',
      label: props.t('dashboard.vulnerabilities'),
      value: props.displayStats.vulns || 0,
      detail: props.stats.severity_breakdown?.length
        ? props.t('dashboard.dominantSeverity', { severity: props.stats.severity_breakdown[0].label })
        : props.t('dashboard.noStructuredFindings'),
      icon: ShieldAlert,
      panelClass: 'hover:border-rose-400/40',
      glowClass: 'bg-rose-500/10 group-hover:bg-rose-500/20',
      iconClass: 'bg-rose-500/10 text-rose-300',
      chipClass: 'text-rose-200',
    },
    {
      key: 'completed_audits',
      label: props.t('dashboard.completedAudits'),
      value: props.displayStats.completed_audits || 0,
      detail: props.t('dashboard.stageResultsReady', { count: props.tasks.reduce((sum, task) => sum + (task.completed_stage_count || 0), 0) }),
      icon: CheckCircle,
      panelClass: 'hover:border-emerald-400/40',
      glowClass: 'bg-emerald-500/10 group-hover:bg-emerald-500/20',
      iconClass: 'bg-emerald-500/10 text-emerald-300',
      chipClass: 'text-emerald-200',
    },
  ]
})

const statusItems = computed(() => {
  const total = totalStatusCount(props.stats.status_breakdown)
  return [
    { key: 'running', label: props.t('status.running'), value: props.stats.status_breakdown?.running || 0, tone: 'bg-amber-400', textClass: 'text-amber-300' },
    { key: 'completed', label: props.t('status.completed'), value: props.stats.status_breakdown?.completed || 0, tone: 'bg-emerald-400', textClass: 'text-emerald-300' },
    { key: 'pending', label: props.t('status.pending'), value: props.stats.status_breakdown?.pending || 0, tone: 'bg-slate-400', textClass: 'text-slate-300' },
    { key: 'paused', label: props.t('status.paused'), value: props.stats.status_breakdown?.paused || 0, tone: 'bg-sky-400', textClass: 'text-sky-300' },
    { key: 'failed', label: props.t('status.failed'), value: props.stats.status_breakdown?.failed || 0, tone: 'bg-rose-400', textClass: 'text-rose-300' },
  ].map(item => ({
    ...item,
    percent: total === 0 ? 0 : Math.round((item.value / total) * 100),
  }))
})

const severityItems = computed(() => {
  const items = Array.isArray(props.stats.severity_breakdown) ? props.stats.severity_breakdown : []
  const maxCount = Math.max(1, ...items.map(item => item.count || 0))
  return items.map(item => ({
    ...item,
    width: `${Math.max(12, Math.round(((item.count || 0) / maxCount) * 100))}%`,
    toneClass: severityToneClass(item.label),
    textClass: severityTextClass(item.label),
  }))
})

const coverageItems = computed(() => {
  return props.stageDefinitions.map(definition => {
    const matched = (props.stats.stage_breakdown || []).find(item => item.key === definition.key)
    return {
      ...definition,
      count: matched?.count || 0,
    }
  })
})

const organizationById = computed(() => {
  return new Map(props.organizations.map(organization => [String(organization.id), organization]))
})

const selectedOrganization = computed(() => {
  if (!props.selectedOrganizationId) return null
  return organizationById.value.get(String(props.selectedOrganizationId)) || null
})

const organizationFilterOptions = computed(() => [
  {
    id: '',
    label: props.t('dashboard.allVisibleOrganizations'),
    depth: 0,
    count: props.tasks.length,
  },
  ...props.organizations.map(organization => ({
    id: String(organization.id),
    label: organization.name,
    displayName: organization.displayName || organization.name,
    depth: organization.treeDepth ?? organization.depth ?? 0,
    count: props.tasks.filter(task => taskBelongsToOrganizationScope(task, organization)).length,
  })),
])

const selectedOrganizationScopeLabel = computed(() => {
  if (!selectedOrganization.value) return props.t('dashboard.allVisibleOrganizations')
  return selectedOrganization.value.displayName?.trim() || selectedOrganization.value.name
})

const taskGroups = computed(() => {
  const groups = new Map()

  props.tasks.forEach((task) => {
    const organizationID = String(task.organization_id || task.organization?.id || '')
    const organization = organizationById.value.get(organizationID) || task.organization || null
    const key = organizationID || 'unassigned'
    if (!groups.has(key)) {
      groups.set(key, {
        key,
        organization,
        label: organization?.displayName?.trim() || organization?.name || organizationLabel(task),
        depth: organization?.treeDepth ?? organization?.depth ?? 0,
        tasks: [],
      })
    }
    groups.get(key).tasks.push(task)
  })

  const order = new Map(props.organizations.map((organization, index) => [String(organization.id), index]))
  return Array.from(groups.values()).sort((left, right) => {
    const leftOrder = order.has(left.key) ? order.get(left.key) : Number.MAX_SAFE_INTEGER
    const rightOrder = order.has(right.key) ? order.get(right.key) : Number.MAX_SAFE_INTEGER
    if (leftOrder !== rightOrder) return leftOrder - rightOrder
    return left.label.localeCompare(right.label)
  })
})

function groupRunningCount(group) {
  return group.tasks.filter(task => String(task.status || '').trim().toLowerCase() === 'running').length
}

function taskBelongsToOrganizationScope(task, organization) {
  if (!organization) return false
  const taskOrganizationID = String(task.organization_id || task.organization?.id || '')
  if (taskOrganizationID === String(organization.id)) return true
  const taskPath = task.organization?.path || organizationById.value.get(taskOrganizationID)?.path || ''
  return Boolean(organization.path && taskPath && taskPath.startsWith(organization.path))
}

function totalStatusCount(breakdown = {}) {
  return ['pending', 'running', 'paused', 'completed', 'failed'].reduce((sum, key) => sum + (breakdown?.[key] || 0), 0)
}

function formatCompactNumber(value) {
  const intlLocale = props.locale === 'en' ? 'en-US' : 'zh-CN'
  return new Intl.NumberFormat(intlLocale).format(value || 0)
}

function displayStatus(status) {
  return props.t(`status.${String(status || '').trim().toLowerCase() || 'pending'}`)
}

function statusBadgeClass(status) {
  switch (status) {
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-300 border border-emerald-500/30'
    case 'running':
      return 'bg-amber-500/10 text-amber-300 border border-amber-500/30'
    case 'failed':
      return 'bg-rose-500/10 text-rose-300 border border-rose-500/30'
    case 'paused':
      return 'bg-sky-500/10 text-sky-300 border border-sky-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border border-slate-500/30'
  }
}

function severityBadgeClass(severity) {
  switch (severity) {
    case 'CRITICAL':
      return 'bg-red-500/15 text-red-200 border border-red-500/30'
    case 'HIGH':
      return 'bg-orange-500/15 text-orange-200 border border-orange-500/30'
    case 'MEDIUM':
      return 'bg-amber-500/15 text-amber-200 border border-amber-500/30'
    case 'LOW':
      return 'bg-blue-500/15 text-blue-200 border border-blue-500/30'
    case 'INFO':
      return 'bg-slate-500/15 text-slate-200 border border-slate-500/30'
    case 'UNKNOWN':
      return 'bg-fuchsia-500/15 text-fuchsia-200 border border-fuchsia-500/30'
    default:
      return 'bg-emerald-500/15 text-emerald-200 border border-emerald-500/30'
  }
}

function severityToneClass(severity) {
  switch (severity) {
    case 'CRITICAL':
      return 'bg-red-400'
    case 'HIGH':
      return 'bg-orange-400'
    case 'MEDIUM':
      return 'bg-amber-400'
    case 'LOW':
      return 'bg-sky-400'
    default:
      return 'bg-slate-400'
  }
}

function severityTextClass(severity) {
  switch (severity) {
    case 'CRITICAL':
      return 'text-red-300'
    case 'HIGH':
      return 'text-orange-300'
    case 'MEDIUM':
      return 'text-amber-300'
    case 'LOW':
      return 'text-sky-300'
    default:
      return 'text-slate-300'
  }
}

function coveragePercent(task) {
  const total = task.total_stage_count || 0
  if (total === 0) return 0
  return Math.round(((task.completed_stage_count || 0) / total) * 100)
}

function coverageLabel(task) {
  return `${task.completed_stage_count || 0}/${task.total_stage_count || 0}`
}

function organizationLabel(task) {
  return task.organization?.name || props.t('common.unassigned')
}

function submittedLabel(value) {
  const intlLocale = props.locale === 'en' ? 'en-US' : 'zh-CN'
  return new Date(value).toLocaleString(intlLocale)
}
</script>

<template>
  <div class="space-y-8 max-w-7xl mx-auto">
    <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-5">
      <div
        v-for="card in statCards"
        :key="card.key"
        :class="['group relative p-6 glass-panel rounded-2xl overflow-hidden border border-white/10 transition-all duration-500 hover:-translate-y-1', card.panelClass]"
      >
        <div :class="['absolute inset-y-0 right-0 w-32 blur-3xl transition-all duration-500', card.glowClass]"></div>
        <div class="relative z-10 flex items-start justify-between gap-4">
          <div>
            <p class="text-slate-400 text-sm uppercase tracking-[0.18em]">{{ card.label }}</p>
            <div class="mt-3 text-4xl font-bold font-mono text-white">{{ formatCompactNumber(card.value) }}</div>
            <p :class="['mt-3 text-sm leading-6', card.chipClass]">{{ card.detail }}</p>
          </div>
          <div :class="['shrink-0 p-4 rounded-2xl border border-white/10', card.iconClass]">
            <component :is="card.icon" class="w-7 h-7" />
          </div>
        </div>
      </div>
    </div>

    <section class="glass-panel rounded-2xl border border-white/10 p-5">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
            <Building2 class="w-4 h-4 text-cyan-300" />
            {{ t('dashboard.organizationScope') }}
          </div>
          <h3 class="mt-2 text-xl font-bold text-white">{{ selectedOrganizationScopeLabel }}</h3>
          <p class="mt-1 text-sm text-slate-400">
            {{ t('dashboard.organizationScopeSummary', { count: formatCompactNumber(tasks.length) }) }}
          </p>
        </div>

        <button
          @click="emit('refresh')"
          class="inline-flex items-center justify-center gap-2 rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm font-semibold text-slate-200 transition-colors hover:bg-white/10"
        >
          <RefreshCw class="w-4 h-4" />
          {{ t('common.refresh') }}
        </button>
      </div>

      <div class="mt-4 flex gap-2 overflow-x-auto pb-1">
        <button
          v-for="option in organizationFilterOptions"
          :key="option.id || 'all-organizations'"
          type="button"
          :class="[
            'inline-flex shrink-0 items-center gap-2 rounded-lg border px-3 py-2 text-sm font-semibold transition-colors',
            String(selectedOrganizationId || '') === String(option.id)
              ? 'border-cyan-400/60 bg-cyan-500/15 text-cyan-100'
              : 'border-white/10 bg-white/5 text-slate-300 hover:bg-white/10 hover:text-white'
          ]"
          :style="{ marginLeft: option.id ? `${Math.min(option.depth, 4) * 10}px` : '0px' }"
          @click="emit('select-organization', option.id)"
        >
          <Building2 class="w-4 h-4" />
          <span class="max-w-56 truncate">{{ option.displayName || option.label }}</span>
          <span class="rounded-full bg-black/20 px-2 py-0.5 text-xs font-mono text-slate-400">{{ option.count }}</span>
        </button>
      </div>
    </section>

    <div class="grid grid-cols-1 xl:grid-cols-3 gap-6">
      <section class="glass-panel rounded-2xl p-6 border border-white/10">
        <div class="flex items-center justify-between gap-3">
          <div>
            <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('dashboard.taskStatus') }}</p>
            <h3 class="mt-2 text-xl font-bold text-white">{{ t('dashboard.queueSnapshot') }}</h3>
          </div>
          <div class="text-sm text-slate-500">{{ t('dashboard.total', { count: totalStatusCount(stats.status_breakdown) }) }}</div>
        </div>

        <div class="mt-6 space-y-4">
          <div v-for="item in statusItems" :key="item.key" class="space-y-2">
            <div class="flex items-center justify-between text-sm">
              <div class="flex items-center gap-2">
                <span :class="['w-2.5 h-2.5 rounded-full', item.tone]"></span>
                <span :class="item.textClass">{{ item.label }}</span>
              </div>
              <span class="text-slate-400 font-mono">{{ item.value }}</span>
            </div>
            <div class="h-2 rounded-full bg-white/5 overflow-hidden">
              <div :class="[item.tone, 'h-full rounded-full transition-all duration-500']" :style="{ width: `${item.percent}%` }"></div>
            </div>
          </div>
        </div>
      </section>

      <section class="glass-panel rounded-2xl p-6 border border-white/10">
        <div>
          <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('dashboard.severityBreakdown') }}</p>
          <h3 class="mt-2 text-xl font-bold text-white">{{ t('dashboard.liveRiskMix') }}</h3>
        </div>

        <div v-if="severityItems.length > 0" class="mt-6 space-y-4">
          <div v-for="item in severityItems" :key="item.label" class="space-y-2">
            <div class="flex items-center justify-between text-sm">
              <span :class="item.textClass">{{ item.label }}</span>
              <span class="text-slate-400 font-mono">{{ item.count }}</span>
            </div>
            <div class="h-2 rounded-full bg-white/5 overflow-hidden">
              <div :class="[item.toneClass, 'h-full rounded-full']" :style="{ width: item.width }"></div>
            </div>
          </div>
        </div>
        <div v-else class="mt-10 flex min-h-40 flex-col items-center justify-center text-center text-slate-500">
          <ShieldAlert class="w-10 h-10 opacity-50" />
          <p class="mt-4 text-sm leading-6">{{ t('dashboard.noStructuredFindings') }}</p>
        </div>
      </section>

      <section class="glass-panel rounded-2xl p-6 border border-white/10">
        <div>
          <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('dashboard.auditCoverage') }}</p>
          <h3 class="mt-2 text-xl font-bold text-white">{{ t('dashboard.stageCompletion') }}</h3>
        </div>

        <div class="mt-6 grid grid-cols-2 gap-3">
          <button
            v-for="stage in coverageItems"
            :key="stage.key"
            type="button"
            class="relative overflow-hidden rounded-2xl border border-white/10 bg-black/20 px-4 py-4 text-left transition-all hover:border-white/15 hover:bg-white/5"
          >
            <div :class="['absolute inset-0 bg-gradient-to-br opacity-80', stage.gradientClass]"></div>
            <div class="relative z-10 flex items-start justify-between gap-3">
              <div>
                <div class="text-sm font-semibold text-white">{{ stage.shortLabel }}</div>
                <div class="mt-1 text-xs text-slate-400">{{ stage.label }}</div>
              </div>
              <div :class="['rounded-xl border px-2.5 py-1 text-sm font-mono', stage.iconClass]">{{ stage.count }}</div>
            </div>
          </button>
        </div>
      </section>
    </div>

    <section class="glass-panel rounded-2xl overflow-hidden border border-white/10">
      <div class="p-6 border-b border-white/5 flex items-center justify-between gap-4">
        <div>
          <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('dashboard.projectQueue') }}</p>
          <h3 class="mt-2 text-xl font-bold flex items-center gap-2 text-white">
            <Activity class="w-5 h-5 text-cyber-primary" />
            {{ t('dashboard.summaryTable') }}
          </h3>
        </div>
        <button @click="emit('refresh')" class="p-2 hover:bg-white/10 rounded-lg text-slate-400 hover:text-white transition-colors">
          <RefreshCw class="w-5 h-5" />
        </button>
      </div>

      <div class="overflow-x-auto">
        <table class="w-full text-left min-w-[1080px]">
          <thead class="bg-black/20 text-slate-400 uppercase text-xs font-semibold tracking-wider">
            <tr>
              <th class="px-6 py-4">{{ t('common.project') }}</th>
              <th class="px-6 py-4">{{ t('dashboard.organization') }}</th>
              <th class="px-6 py-4">{{ t('common.status') }}</th>
              <th class="px-6 py-4">{{ t('common.routes') }}</th>
              <th class="px-6 py-4">{{ t('common.findings') }}</th>
              <th class="px-6 py-4">{{ t('common.coverage') }}</th>
              <th class="px-6 py-4">{{ t('common.topSeverity') }}</th>
              <th class="px-6 py-4">{{ t('common.submitted') }}</th>
              <th class="px-6 py-4 text-right">{{ t('common.action') }}</th>
            </tr>
          </thead>
          <tbody v-if="tasks.length > 0" class="divide-y divide-white/5">
            <template v-for="group in taskGroups" :key="group.key">
              <tr class="bg-white/[0.03]">
                <td colspan="9" class="px-6 py-3">
                  <div class="flex flex-wrap items-center justify-between gap-3">
                    <div class="flex min-w-0 items-center gap-3">
                      <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg border border-cyan-500/20 bg-cyan-500/10 text-cyan-200">
                        <Building2 class="w-4 h-4" />
                      </div>
                      <div class="min-w-0">
                        <div class="truncate text-sm font-semibold text-white">{{ group.label }}</div>
                        <div class="text-xs text-slate-500">
                          {{ t('dashboard.organizationGroupSummary', { count: group.tasks.length, running: groupRunningCount(group) }) }}
                        </div>
                      </div>
                    </div>
                    <span class="rounded-full border border-white/10 bg-black/20 px-3 py-1 text-xs font-mono text-slate-400">
                      {{ group.tasks.length }}
                    </span>
                  </div>
                </td>
              </tr>

              <tr
                v-for="(task, index) in group.tasks"
                :key="task.id"
                :class="['group transition-colors duration-200', selectedTaskId === task.id ? 'bg-white/5' : 'hover:bg-white/5']"
                :style="{ animation: `fadeInUp 0.5s ease-out ${index * 0.04}s forwards`, opacity: 0 }"
              >
                <td class="px-6 py-4">
                  <div class="flex items-center gap-3">
                    <div class="p-2 rounded-xl bg-slate-800/70 border border-white/5">
                      <FolderOpen class="w-5 h-5 text-slate-300" />
                    </div>
                    <div>
                      <div class="font-medium text-white">{{ task.name || t('dashboard.untitledProject') }}</div>
                      <div class="text-xs text-slate-500">{{ task.remark || t('dashboard.noRemarks') }}</div>
                    </div>
                  </div>
                </td>
                <td class="px-6 py-4">
                  <span class="inline-flex max-w-44 items-center rounded-full border border-white/10 bg-white/5 px-3 py-1 text-xs font-semibold text-slate-300">
                    <span class="truncate">{{ organizationLabel(task) }}</span>
                  </span>
                </td>
                <td class="px-6 py-4">
                  <div :class="['inline-flex items-center gap-2 px-3 py-1 rounded-full text-xs font-bold uppercase', statusBadgeClass(task.status)]">
                    <span v-if="task.status === 'running'" class="w-1.5 h-1.5 rounded-full bg-current animate-pulse"></span>
                    {{ displayStatus(task.status) }}
                  </div>
                </td>
                <td class="px-6 py-4 text-slate-200 font-mono">{{ formatCompactNumber(task.route_count) }}</td>
                <td class="px-6 py-4 text-slate-200 font-mono">{{ formatCompactNumber(task.finding_count) }}</td>
                <td class="px-6 py-4">
                  <div class="flex items-center gap-3">
                    <div class="w-28 h-2 rounded-full bg-white/5 overflow-hidden">
                      <div class="h-full rounded-full bg-cyber-primary/80" :style="{ width: `${coveragePercent(task)}%` }"></div>
                    </div>
                    <span class="text-sm text-slate-300 font-mono">{{ coverageLabel(task) }}</span>
                  </div>
                </td>
                <td class="px-6 py-4">
                  <span :class="['inline-flex px-3 py-1 rounded-full text-xs font-bold uppercase', severityBadgeClass(task.highest_severity)]">
                    {{ task.highest_severity }}
                  </span>
                </td>
                <td class="px-6 py-4 text-slate-400 text-sm font-mono">{{ submittedLabel(task.created_at) }}</td>
                <td class="px-6 py-4 text-right">
                  <div class="flex items-center justify-end gap-2 opacity-0 group-hover:opacity-100 transition-opacity duration-200">
                    <button @click="emit('open-task', task)" class="p-2 hover:bg-cyan-500/20 text-cyan-300 rounded-lg transition-colors" :title="t('dashboard.viewDetails')">
                      <ChevronRight class="w-4 h-4" />
                    </button>
                    <button v-if="canDelete" @click="emit('delete-task', task.id)" class="p-2 hover:bg-rose-500/20 text-rose-300 rounded-lg transition-colors" :title="t('common.delete')">
                      <Trash2 class="w-4 h-4" />
                    </button>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
          <tbody v-else>
            <tr>
              <td colspan="9" class="px-6 py-16 text-center text-slate-500">
                <div v-if="loading" class="flex flex-col items-center gap-4">
                  <div class="w-12 h-12 rounded-full border-2 border-cyber-primary/20 border-t-cyber-primary animate-spin"></div>
                  <p>{{ t('dashboard.loadingProjectSummaries') }}</p>
                </div>
                <div v-else class="flex flex-col items-center gap-4">
                  <div class="p-4 bg-slate-800/40 rounded-full border border-slate-700/70">
                    <FolderOpen class="w-8 h-8 opacity-60" />
                  </div>
                  <p>{{ t('dashboard.noProjectsYet') }}</p>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>
  </div>
</template>
