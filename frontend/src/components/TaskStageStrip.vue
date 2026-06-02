<script setup>
import { computed } from 'vue'
import { hasStagePayload, parseResultArray, splitFindings } from '../utils/findings'

const props = defineProps({
  task: {
    type: Object,
    required: true,
  },
  stageDefinitions: {
    type: Array,
    default: () => [],
  },
  currentView: {
    type: String,
    default: '',
  },
  t: {
    type: Function,
    required: true,
  },
})

const emit = defineEmits(['select-stage'])

const stageCards = computed(() => {
  return props.stageDefinitions.map(definition => {
    const stage = props.task?.stages?.find(item => item.name === definition.key) || null
    const parsed = parseResultArray(stage?.output_json || stage?.result)
    const findingGroups = splitFindings(parsed || [])
    const findingCount = Array.isArray(parsed) ? findingGroups.active.length : null
    const rejectedCount = Array.isArray(parsed) ? findingGroups.rejected.length : 0
    const status = stage?.status || 'pending'

    let detail = props.t('taskStrip.waitingToRun')
    if (status === 'running') {
      detail = props.t('taskStrip.auditInProgress')
    } else if (status === 'failed') {
      detail = props.t('taskStrip.lastRunFailed')
    } else if (status === 'completed' && findingCount > 0) {
      detail = props.t('taskStrip.findingsCount', { count: findingCount })
    } else if (status === 'completed' && findingCount === 0 && rejectedCount > 0) {
      detail = props.t('taskStrip.allRejected')
    } else if (status === 'completed' && findingCount === 0) {
      detail = props.t('taskStrip.cleanResult')
    } else if (status === 'completed' && hasStagePayload(stage)) {
      detail = props.t('taskStrip.rawExportReady')
    }

    return {
      ...definition,
      stage,
      status,
      detail,
      active: props.currentView === definition.view,
    }
  })
})

function displayStatus(status) {
  return props.t(`status.${String(status || '').trim().toLowerCase() || 'pending'}`)
}

function statusBadgeClass(status) {
  switch (status) {
    case 'running':
      return 'bg-amber-500/10 text-amber-300 border-amber-500/30'
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-300 border-emerald-500/30'
    case 'failed':
      return 'bg-rose-500/10 text-rose-300 border-rose-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border-slate-500/30'
  }
}
</script>

<template>
  <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
    <button
      v-for="card in stageCards"
      :key="card.key"
      type="button"
      @click="emit('select-stage', card.view)"
      :title="card.label"
      :class="[
        'group relative min-w-0 overflow-hidden rounded-xl border bg-black/25 p-4 text-left transition-all duration-300 hover:-translate-y-0.5 hover:bg-white/5',
        card.cardClass,
        card.active ? 'ring-1 ring-white/20 border-white/20' : 'border-white/10'
      ]"
    >
      <div :class="['absolute inset-0 bg-gradient-to-br opacity-80', card.gradientClass]"></div>
      <div class="relative z-10 min-w-0">
        <div class="flex min-w-0 flex-wrap items-start gap-3">
          <div :class="['shrink-0 rounded-lg border p-2.5', card.iconClass]">
            <component :is="card.icon" class="h-4 w-4" />
          </div>
          <div class="min-w-0 flex-1">
            <div class="break-words text-sm font-semibold leading-5 text-white">
              {{ card.shortLabel || card.label }}
            </div>
            <div class="mt-1 text-xs leading-5 text-slate-300 break-words">
              {{ card.detail }}
            </div>
          </div>
          <span :class="['max-w-full rounded-full border px-2.5 py-1 text-[11px] font-semibold uppercase leading-none tracking-wide break-words', statusBadgeClass(card.status)]">
            {{ displayStatus(card.status) }}
          </span>
        </div>
      </div>
    </button>
  </div>
</template>
