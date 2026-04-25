<script setup>
import { computed, ref } from 'vue'
import { Activity, CheckCircle, LayoutDashboard, RefreshCw, Terminal } from 'lucide-vue-next'
import {
  effectiveSeverity,
  formatLocation,
  formatTriggerLabel,
  normalizeSeverity,
  splitFindings,
  verificationStatus,
} from '../utils/findings'

const props = defineProps({
  task: {
    type: Object,
    required: true,
  },
  stageDefinition: {
    type: Object,
    required: true,
  },
  logs: {
    type: Array,
    default: () => [],
  },
  results: {
    type: Array,
    default: null,
  },
  rawResult: {
    type: String,
    default: '',
  },
  stageMeta: {
    type: Object,
    default: () => ({}),
  },
  isRepairing: {
    type: Boolean,
    default: false,
  },
  activeTab: {
    type: String,
    default: 'console',
  },
  locale: {
    type: String,
    default: 'zh',
  },
  t: {
    type: Function,
    required: true,
  },
  taskRunning: {
    type: Boolean,
    default: false,
  },
  gapCheckPending: {
    type: Boolean,
    default: false,
  },
  revalidatePending: {
    type: Boolean,
    default: false,
  },
  canGapCheck: {
    type: Boolean,
    default: false,
  },
  canRevalidate: {
    type: Boolean,
    default: false,
  },
})

const emit = defineEmits(['back', 'run', 'gap-check', 'revalidate', 'repair', 'update:activeTab'])

const expandedKey = ref('')

const expectedTypeByStage = {
  rce: 'RCE',
  injection: 'Injection',
  auth: 'Authentication',
  access: 'Authorization',
  xss: 'XSS',
  config: 'Configuration',
  fileop: 'FileOperation',
  logic: 'BusinessLogic',
}

const normalizedResults = computed(() => Array.isArray(props.results) ? props.results : [])
const expectedType = computed(() => expectedTypeByStage[props.stageDefinition.key] || '')
const resultsMatchExpectedType = computed(() => {
  if (!normalizedResults.value.length) return true
  return normalizedResults.value[0]?.type === expectedType.value
})
const findingGroups = computed(() => splitFindings(normalizedResults.value))
const activeFindings = computed(() => findingGroups.value.active)
const rejectedFindings = computed(() => findingGroups.value.rejected)
const hasStructuredResults = computed(() => Array.isArray(props.results))
const reviewSummary = computed(() => props.stageMeta?.review_summary || '')
const reviewCounters = computed(() => {
  const items = [
    { key: 'confirmed', label: props.t('verification.confirmed'), value: props.stageMeta?.confirmed_count || 0 },
    { key: 'uncertain', label: props.t('verification.uncertain'), value: props.stageMeta?.uncertain_count || 0 },
    { key: 'rejected', label: props.t('verification.rejected'), value: props.stageMeta?.rejected_count || 0 },
  ]
  return items.filter(item => item.value > 0)
})

function displaySeverity(item) {
  return effectiveSeverity(item)
}

function severityBadgeClass(severity) {
  switch (normalizeSeverity(severity)) {
    case 'CRITICAL':
      return 'bg-red-500/15 text-red-300 border border-red-500/30'
    case 'HIGH':
      return 'bg-orange-500/15 text-orange-300 border border-orange-500/30'
    case 'MEDIUM':
      return 'bg-yellow-500/15 text-yellow-300 border border-yellow-500/30'
    case 'LOW':
      return 'bg-blue-500/15 text-blue-300 border border-blue-500/30'
    case 'INFO':
      return 'bg-slate-500/15 text-slate-300 border border-slate-500/30'
    case 'UNKNOWN':
      return 'bg-zinc-500/15 text-zinc-300 border border-zinc-500/30'
    default:
      return 'bg-slate-500/15 text-slate-300 border border-slate-500/30'
  }
}

function verificationBadgeClass(status) {
  switch (verificationStatus({ verification_status: status })) {
    case 'confirmed':
      return 'bg-emerald-500/15 text-emerald-300 border border-emerald-500/30'
    case 'uncertain':
      return 'bg-amber-500/15 text-amber-300 border border-amber-500/30'
    case 'rejected':
      return 'bg-rose-500/15 text-rose-300 border border-rose-500/30'
    default:
      return 'bg-slate-500/15 text-slate-300 border border-slate-500/30'
  }
}

function originBadgeClass(origin) {
  switch (String(origin || '').trim().toLowerCase()) {
    case 'gap_check':
      return 'bg-cyan-500/15 text-cyan-300 border border-cyan-500/30'
    default:
      return 'bg-white/10 text-slate-200 border border-white/10'
  }
}

function findingLocation(item) {
  return formatLocation(item?.location, props.t('auditView.locationNotProvided'))
}

function findingTrigger(item) {
  return formatTriggerLabel(item?.trigger, props.t('auditView.staticFinding'))
}

function displayVerification(status) {
  return props.t(`verification.${verificationStatus({ verification_status: status })}`)
}

function displayOrigin(origin) {
  return props.t(`origin.${String(origin || 'initial').trim().toLowerCase() || 'initial'}`)
}

function panelClasses() {
  switch (props.stageDefinition.key) {
    case 'rce':
      return {
        shell: 'border-red-500/20 shadow-[0_0_30px_rgba(239,68,68,0.1)]',
        action: 'bg-red-500 hover:bg-red-600 text-white shadow-[0_0_20px_rgba(239,68,68,0.4)]',
        accent: 'text-red-500',
        finding: 'bg-red-500/5 border-red-500/20',
        code: 'text-green-400',
      }
    case 'injection':
      return {
        shell: 'border-amber-500/20 shadow-[0_0_30px_rgba(245,158,11,0.1)]',
        action: 'bg-amber-500 hover:bg-amber-600 text-black shadow-[0_0_20px_rgba(245,158,11,0.4)]',
        accent: 'text-amber-500',
        finding: 'bg-amber-500/5 border-amber-500/20',
        code: 'text-amber-400',
      }
    case 'auth':
      return {
        shell: 'border-sky-500/20 shadow-[0_0_30px_rgba(14,165,233,0.1)]',
        action: 'bg-sky-500 hover:bg-sky-600 text-black shadow-[0_0_20px_rgba(14,165,233,0.35)]',
        accent: 'text-sky-400',
        finding: 'bg-sky-500/5 border-sky-500/20',
        code: 'text-sky-300',
      }
    case 'access':
      return {
        shell: 'border-indigo-500/20 shadow-[0_0_30px_rgba(99,102,241,0.1)]',
        action: 'bg-indigo-500 hover:bg-indigo-600 text-white shadow-[0_0_20px_rgba(99,102,241,0.35)]',
        accent: 'text-indigo-400',
        finding: 'bg-indigo-500/5 border-indigo-500/20',
        code: 'text-indigo-300',
      }
    case 'xss':
      return {
        shell: 'border-emerald-500/20 shadow-[0_0_30px_rgba(16,185,129,0.1)]',
        action: 'bg-emerald-500 hover:bg-emerald-600 text-black shadow-[0_0_20px_rgba(16,185,129,0.35)]',
        accent: 'text-emerald-400',
        finding: 'bg-emerald-500/5 border-emerald-500/20',
        code: 'text-emerald-300',
      }
    case 'config':
      return {
        shell: 'border-cyan-500/20 shadow-[0_0_30px_rgba(6,182,212,0.1)]',
        action: 'bg-cyan-500 hover:bg-cyan-600 text-black shadow-[0_0_20px_rgba(6,182,212,0.35)]',
        accent: 'text-cyan-400',
        finding: 'bg-cyan-500/5 border-cyan-500/20',
        code: 'text-cyan-300',
      }
    case 'fileop':
      return {
        shell: 'border-orange-500/20 shadow-[0_0_30px_rgba(249,115,22,0.1)]',
        action: 'bg-orange-500 hover:bg-orange-600 text-black shadow-[0_0_20px_rgba(249,115,22,0.35)]',
        accent: 'text-orange-400',
        finding: 'bg-orange-500/5 border-orange-500/20',
        code: 'text-orange-300',
      }
    default:
      return {
        shell: 'border-rose-500/20 shadow-[0_0_30px_rgba(244,63,94,0.1)]',
        action: 'bg-rose-500 hover:bg-rose-600 text-white shadow-[0_0_20px_rgba(244,63,94,0.35)]',
        accent: 'text-rose-400',
        finding: 'bg-rose-500/5 border-rose-500/20',
        code: 'text-rose-300',
      }
  }
}

function toggleDetails(key) {
  expandedKey.value = expandedKey.value === key ? '' : key
}
</script>

<template>
  <div class="space-y-6 max-w-7xl mx-auto animate-slide-up">
    <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
      <div>
        <div class="flex items-center gap-2 mb-1">
          <button @click="emit('back')" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
            <LayoutDashboard class="w-3 h-3" /> {{ t('common.backToTask') }}
          </button>
          <span class="text-slate-600">/</span>
          <span :class="[panelClasses().accent, 'text-sm font-mono']">{{ stageDefinition.label }}</span>
        </div>
        <h1 class="text-3xl font-bold text-white">{{ stageDefinition.label }}</h1>
        <p class="text-slate-400 mt-1">{{ stageDefinition.description }}</p>
      </div>

      <div class="flex flex-wrap gap-3">
        <button
          type="button"
          @click="emit('run')"
          :disabled="taskRunning"
          :class="['px-5 py-2.5 font-bold rounded-lg transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed', panelClasses().action]"
        >
          <component :is="stageDefinition.icon" class="w-4 h-4" />
          {{ taskRunning ? t('auditView.auditInProgress') : t('auditView.runAudit', { stage: stageDefinition.shortLabel }) }}
        </button>
        <button
          type="button"
          @click="emit('gap-check')"
          :disabled="!canGapCheck || gapCheckPending || taskRunning"
          class="px-5 py-2.5 bg-white/5 hover:bg-white/10 text-slate-100 border border-white/10 rounded-lg font-bold flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <RefreshCw :class="['w-4 h-4', gapCheckPending ? 'animate-spin' : '']" />
          {{ gapCheckPending ? t('auditView.gapChecking') : t('auditView.gapCheck') }}
        </button>
        <button
          type="button"
          @click="emit('revalidate')"
          :disabled="!canRevalidate || revalidatePending || taskRunning"
          class="px-5 py-2.5 bg-white/5 hover:bg-white/10 text-slate-100 border border-white/10 rounded-lg font-bold flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
        >
          <CheckCircle class="w-4 h-4" />
          {{ revalidatePending ? t('auditView.revalidating') : t('auditView.revalidateFindings') }}
        </button>
      </div>
    </div>

    <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[680px] border" :class="panelClasses().shell">
      <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
        <div class="flex items-center gap-4">
          <button
            @click="emit('update:activeTab', 'console')"
            :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
          >
            <Terminal class="w-4 h-4" />
            {{ t('common.console') }}
          </button>
          <button
            @click="emit('update:activeTab', 'results')"
            :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
          >
            <Activity class="w-4 h-4" />
            {{ t('common.results') }}
          </button>
        </div>
      </div>

      <div v-if="activeTab === 'console'" class="flex-1 bg-slate-950 p-6 overflow-auto font-mono text-sm relative group">
        <div class="absolute inset-0 pointer-events-none bg-scan-lines opacity-5"></div>
        <div v-if="logs && logs.length > 0" class="space-y-1">
          <div v-for="(log, idx) in logs" :key="idx" class="text-slate-400 break-all hover:bg-white/5 px-1 rounded flex gap-3 animate-fade-in">
            <span class="text-slate-600 select-none whitespace-nowrap text-xs pt-0.5">{{ log.substring(1, 9) }}</span>
            <span :class="{
              'text-cyber-primary': log.includes('AI:'),
              'text-yellow-400': log.includes('Executing tool'),
              'text-red-400': log.includes('Error') || log.includes('failed'),
              'text-green-400': log.includes('completed')
            }">{{ log.substring(11) }}</span>
          </div>
        </div>
        <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
          <p>{{ t('auditView.readyToStart') }}</p>
        </div>
      </div>

      <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto space-y-4">
        <div v-if="reviewSummary || reviewCounters.length" class="rounded-xl border border-white/10 bg-black/20 px-4 py-4">
          <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('auditView.reviewSummary') }}</div>
          <p v-if="reviewSummary" class="mt-2 text-sm text-slate-300">{{ reviewSummary }}</p>
          <div v-if="reviewCounters.length" class="mt-3 flex flex-wrap gap-2">
            <span v-for="counter in reviewCounters" :key="counter.key" :class="['px-2.5 py-1 rounded-full text-xs font-bold', verificationBadgeClass(counter.key)]">
              {{ counter.label }} / {{ counter.value }}
            </span>
          </div>
        </div>

        <div v-if="hasStructuredResults && resultsMatchExpectedType" class="space-y-5">
          <section>
            <div class="flex items-center justify-between gap-3 mb-3">
              <h3 class="text-lg font-bold text-white">{{ t('auditView.activeFindings') }}</h3>
              <span class="px-2.5 py-1 rounded-full bg-white/10 text-xs font-bold text-slate-300">{{ activeFindings.length }}</span>
            </div>

            <div v-if="activeFindings.length > 0" class="space-y-4">
              <div v-for="(vuln, idx) in activeFindings" :key="`active-${idx}`" :class="['border rounded-lg p-4', panelClasses().finding]">
                <div class="flex justify-between items-start mb-2 gap-4">
                  <div>
                    <div class="flex items-center gap-2 flex-wrap">
                      <span :class="['px-2 py-0.5 text-xs font-bold rounded uppercase', severityBadgeClass(displaySeverity(vuln))]">
                        {{ displaySeverity(vuln) }}
                      </span>
                      <span :class="['px-2 py-0.5 text-xs font-bold rounded uppercase', verificationBadgeClass(vuln.verification_status)]">
                        {{ displayVerification(vuln.verification_status) }}
                      </span>
                      <span :class="['px-2 py-0.5 text-xs font-bold rounded uppercase', originBadgeClass(vuln.origin)]">
                        {{ displayOrigin(vuln.origin) }}
                      </span>
                      <span class="text-white font-bold text-lg">{{ vuln.subtype }}</span>
                    </div>
                    <div class="text-slate-400 text-sm mt-1 font-mono">{{ findingLocation(vuln) }}</div>
                  </div>
                  <button class="text-slate-400 hover:text-white" @click="toggleDetails(`active-${idx}`)">
                    {{ expandedKey === `active-${idx}` ? t('auditView.collapse') : t('auditView.details') }}
                  </button>
                </div>
                <p class="text-slate-300 mb-3">{{ vuln.description }}</p>
                <p v-if="vuln.verification_reason" class="text-sm text-slate-400 mb-3">{{ vuln.verification_reason }}</p>
                <div v-if="expandedKey === `active-${idx}`" class="mt-4 pt-4 border-t border-white/10 space-y-4 animate-fade-in">
                  <div class="bg-black/30 p-3 rounded border border-white/5">
                    <div class="text-xs text-slate-500 uppercase mb-1">{{ t('auditView.triggerEndpoint') }}</div>
                    <div class="font-mono text-sm text-cyber-primary">{{ findingTrigger(vuln) }}</div>
                  </div>

                  <div v-if="vuln.execution_logic">
                    <div class="text-xs text-slate-500 uppercase mb-1">{{ t('auditView.executionLogic') }}</div>
                    <p class="text-sm text-slate-300">{{ vuln.execution_logic }}</p>
                  </div>

                  <div v-if="vuln.vulnerable_code">
                    <div class="text-xs text-slate-500 uppercase mb-1">{{ t('auditView.vulnerableCode') }}</div>
                    <pre class="bg-slate-950 p-4 rounded text-xs font-mono overflow-x-auto border border-white/5 text-blue-300">{{ vuln.vulnerable_code }}</pre>
                  </div>

                  <div v-if="vuln.poc || vuln.poc_http">
                    <div class="text-xs text-slate-500 uppercase mb-1">{{ t('auditView.httpPocPayload') }}</div>
                    <pre :class="['bg-slate-950 p-4 rounded text-xs font-mono overflow-x-auto border border-white/5', panelClasses().code]">{{ vuln.poc || vuln.poc_http }}</pre>
                  </div>
                </div>
              </div>
            </div>

            <div v-else-if="normalizedResults.length === 0" class="text-center py-10 text-green-400">
              <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
              <p>{{ t('auditView.noVulnsFound', { stage: stageDefinition.shortLabel }) }}</p>
              <button
                v-if="rawResult && rawResult.length > 50"
                @click="emit('repair')"
                :disabled="isRepairing"
                class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
              >
                <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                {{ t('auditView.suspectParsingError') }}
              </button>
            </div>

            <div v-else class="rounded-xl border border-rose-500/20 bg-rose-500/10 px-4 py-4 text-sm text-rose-200">
              {{ t('auditView.allRejected') }}
            </div>
          </section>

          <section v-if="rejectedFindings.length > 0">
            <div class="flex items-center justify-between gap-3 mb-3">
              <h3 class="text-lg font-bold text-white">{{ t('auditView.rejectedFindings') }}</h3>
              <span class="px-2.5 py-1 rounded-full bg-rose-500/15 text-xs font-bold text-rose-300 border border-rose-500/30">{{ rejectedFindings.length }}</span>
            </div>
            <div class="space-y-4">
              <div v-for="(vuln, idx) in rejectedFindings" :key="`rejected-${idx}`" class="border border-rose-500/20 bg-rose-500/5 rounded-lg p-4">
                <div class="flex items-center gap-2 flex-wrap">
                  <span :class="['px-2 py-0.5 text-xs font-bold rounded uppercase', severityBadgeClass(displaySeverity(vuln))]">{{ displaySeverity(vuln) }}</span>
                  <span :class="['px-2 py-0.5 text-xs font-bold rounded uppercase', verificationBadgeClass(vuln.verification_status)]">{{ displayVerification(vuln.verification_status) }}</span>
                  <span class="text-white font-bold text-lg">{{ vuln.subtype }}</span>
                </div>
                <div class="text-slate-400 text-sm mt-1 font-mono">{{ findingLocation(vuln) }}</div>
                <p class="text-slate-300 mt-3">{{ vuln.description }}</p>
                <p v-if="vuln.verification_reason" class="text-sm text-rose-200 mt-3">{{ vuln.verification_reason }}</p>
              </div>
            </div>
          </section>
        </div>

        <div v-else-if="hasStructuredResults && normalizedResults.length > 0 && !resultsMatchExpectedType" class="h-full flex flex-col items-center justify-center text-slate-600">
          <div class="flex flex-col items-center gap-4 max-w-lg text-center">
            <component :is="stageDefinition.icon" class="w-12 h-12 opacity-50 mb-2" />
            <p class="text-lg text-slate-400">{{ t('auditView.resultFormatMismatch') }}</p>
            <p class="text-sm">{{ t('auditView.resultFormatMismatchDesc', { stage: stageDefinition.label }) }}</p>

            <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
              <p class="text-xs uppercase text-slate-500 mb-2 font-bold">{{ t('auditView.previewParsedData') }}</p>
              <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(normalizedResults, null, 2) }}</pre>
            </div>

            <button
              @click="emit('repair')"
              :disabled="isRepairing"
              class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
            >
              <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
              {{ isRepairing ? t('auditView.aiRepairing') : t('auditView.fixFormatWithAI') }}
            </button>
          </div>
        </div>

        <div v-else-if="rawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
          {{ rawResult }}
          <div class="mt-4 pt-4 border-t border-white/10">
            <button
              @click="emit('repair')"
              :disabled="isRepairing"
              class="px-4 py-2 bg-white/10 hover:bg-white/20 text-white border border-white/10 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
            >
              <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
              {{ isRepairing ? t('common.repairingJson') : t('common.repairJsonFormat') }}
            </button>
          </div>
        </div>

        <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
          <p>{{ t('auditView.noResultsAvailable', { stage: stageDefinition.shortLabel }) }}</p>
        </div>
      </div>
    </div>
  </div>
</template>
