<script setup>
import { computed, onMounted, ref } from 'vue'
import axios from 'axios'
import { Building2, ChevronRight, Plus, RefreshCw, Save, Trash2 } from 'lucide-vue-next'

const props = defineProps({
  apiUrl: {
    type: String,
    default: '/api',
  },
  authToken: {
    type: String,
    required: true,
  },
  t: {
    type: Function,
    required: true,
  },
})

const emit = defineEmits(['auth-expired', 'updated'])

const organizations = ref([])
const editForms = ref({})
const createForm = ref({
  name: '',
  parent_id: '',
})
const createMode = ref('child')
const selectedOrganizationId = ref(null)
const isLoading = ref(false)
const isCreating = ref(false)
const pendingId = ref('')
const error = ref('')

const flatOrganizations = computed(() => flattenOrganizationTree(organizations.value))

const selectedOrganization = computed(() => {
  if (!selectedOrganizationId.value) return null
  return flatOrganizations.value.find((organization) => organization.id === selectedOrganizationId.value) || null
})

const selectedPath = computed(() => {
  if (!selectedOrganizationId.value) return []
  return findOrganizationPath(organizations.value, selectedOrganizationId.value) || []
})

const levelColumns = computed(() => {
  const columns = []
  const path = selectedPath.value
  let currentNodes = organizations.value
  let parent = null
  let depth = 0

  while (currentNodes.length > 0) {
    const selectedAtDepth = path[depth] || null
    columns.push({
      key: parent ? `children:${parent.id}` : 'roots',
      depth,
      parent,
      organizations: currentNodes,
      selectedId: selectedAtDepth?.id || null,
    })

    if (!selectedAtDepth) break

    parent = selectedAtDepth
    currentNodes = selectedAtDepth.children || []
    depth += 1
  }

  return columns
})

const createParentOrganization = computed(() => {
  if (createMode.value !== 'child') return null
  return selectedOrganization.value
})

const createTitle = computed(() => {
  if (createMode.value === 'root' || !createParentOrganization.value) {
    return props.t('organizations.createRoot')
  }
  return props.t('organizations.createChildOf', { name: createParentOrganization.value.name })
})

const organizationErrorKeys = {
  'Failed to load organizations': 'failedLoad',
  'Organization name is required': 'organizationNameRequired',
  'Parent organization not found': 'parentNotFound',
  'Failed to create organization': 'failedCreate',
  'Organization cannot be moved under itself or a descendant': 'invalidMove',
  'Organization not found': 'organizationNotFound',
  'Failed to update organization': 'failedUpdate',
  'Organization has child organizations': 'hasChildren',
  'Organization has projects': 'hasProjects',
  'Organization has members': 'hasMembers',
  'Failed to delete organization': 'failedDelete',
  'Invalid organization id': 'invalidId',
}

function authConfig() {
  return {
    headers: {
      Authorization: `Bearer ${props.authToken}`,
    },
  }
}

function flattenOrganizationTree(nodes = [], depth = 0) {
  return nodes.flatMap((node) => {
    const treeDepth = Number.isInteger(node.depth) ? node.depth : depth
    const current = {
      ...node,
      treeDepth,
      displayName: `${'  '.repeat(treeDepth)}${node.name}`,
    }
    return [current, ...flattenOrganizationTree(node.children || [], treeDepth + 1)]
  })
}

function findOrganizationPath(nodes = [], organizationId, trail = []) {
  for (const node of nodes) {
    const currentTrail = [...trail, node]
    if (node.id === organizationId) {
      return currentTrail
    }

    const childTrail = findOrganizationPath(node.children || [], organizationId, currentTrail)
    if (childTrail) return childTrail
  }

  return null
}

function childCount(organization) {
  return Array.isArray(organization?.children) ? organization.children.length : 0
}

function syncEditForms() {
  editForms.value = Object.fromEntries(flatOrganizations.value.map((organization) => [
    organization.id,
    {
      name: organization.name || '',
      parent_id: organization.parent_id ? String(organization.parent_id) : '',
    },
  ]))
}

function normalizedOrganizationId(value) {
  const id = Number(value)
  return Number.isFinite(id) && id > 0 ? id : null
}

function normalizeSelection(preferredId = selectedOrganizationId.value) {
  const preferredOrganizationId = normalizedOrganizationId(preferredId)
  const hasPreferred = preferredOrganizationId && flatOrganizations.value.some((organization) => organization.id === preferredOrganizationId)
  selectedOrganizationId.value = hasPreferred ? preferredOrganizationId : (flatOrganizations.value[0]?.id || null)

  if (!selectedOrganizationId.value) {
    createMode.value = 'root'
    createForm.value.parent_id = ''
    return
  }

  if (createMode.value === 'child') {
    createForm.value.parent_id = String(selectedOrganizationId.value)
  }
}

function selectOrganization(organization) {
  selectedOrganizationId.value = organization.id
  if (createMode.value === 'child') {
    createForm.value.parent_id = String(organization.id)
  }
}

function prepareRoot() {
  createMode.value = 'root'
  createForm.value.parent_id = ''
}

function prepareChild(parent = selectedOrganization.value) {
  if (!parent) return
  selectOrganization(parent)
  createMode.value = 'child'
  createForm.value.parent_id = String(parent.id)
}

function availableParents(organization) {
  return flatOrganizations.value.filter((candidate) => {
    if (candidate.id === organization.id) return false
    if (organization.path && candidate.path?.startsWith(organization.path)) return false
    return true
  })
}

function requestBodyFromForm(form) {
  const body = {
    name: String(form.name || '').trim(),
  }
  body.parent_id = form.parent_id ? Number(form.parent_id) : null
  return body
}

function handleRequestError(requestError, fallback) {
  if (requestError.response?.status === 401) {
    emit('auth-expired')
    return
  }
  error.value = translateOrganizationError(requestError.response?.data?.error, fallback)
}

function translateOrganizationError(message, fallback) {
  const trimmed = String(message || '').trim()
  const key = organizationErrorKeys[trimmed]
  if (key) {
    const translated = props.t(`organizations.${key}`)
    if (translated !== `organizations.${key}`) return translated
  }
  return trimmed || fallback
}

async function loadOrganizations(options = {}) {
  isLoading.value = true
  error.value = ''
  const preferredSelectedId = options.preferredSelectedId ?? selectedOrganizationId.value
  try {
    const { data } = await axios.get(`${props.apiUrl}/organizations`, authConfig())
    organizations.value = Array.isArray(data) ? data : []
    syncEditForms()
    normalizeSelection(preferredSelectedId)
  } catch (requestError) {
    handleRequestError(requestError, props.t('organizations.failedLoad'))
  } finally {
    isLoading.value = false
  }
}

async function refreshAfterMutation(preferredSelectedId = selectedOrganizationId.value) {
  await loadOrganizations({ preferredSelectedId })
  emit('updated')
}

async function createOrganization() {
  const name = String(createForm.value.name || '').trim()
  if (!name) {
    error.value = props.t('organizations.organizationNameRequired')
    return
  }

  const parentId = createMode.value === 'child' && createParentOrganization.value ? createParentOrganization.value.id : null

  isCreating.value = true
  error.value = ''
  try {
    const body = { name }
    if (parentId) {
      body.parent_id = parentId
    }
    const { data } = await axios.post(`${props.apiUrl}/organizations`, body, authConfig())
    createForm.value = {
      name: '',
      parent_id: parentId ? String(parentId) : '',
    }
    await refreshAfterMutation(data?.id || parentId || selectedOrganizationId.value)
  } catch (requestError) {
    handleRequestError(requestError, props.t('organizations.failedCreate'))
  } finally {
    isCreating.value = false
  }
}

async function updateOrganization(organization) {
  const form = editForms.value[organization.id]
  if (!form) return

  if (!String(form.name || '').trim()) {
    error.value = props.t('organizations.organizationNameRequired')
    return
  }

  pendingId.value = `update:${organization.id}`
  error.value = ''
  try {
    await axios.patch(`${props.apiUrl}/organizations/${organization.id}`, requestBodyFromForm(form), authConfig())
    await refreshAfterMutation(organization.id)
  } catch (requestError) {
    handleRequestError(requestError, props.t('organizations.failedUpdate'))
  } finally {
    pendingId.value = ''
  }
}

async function deleteOrganization(organization) {
  if (!confirm(props.t('organizations.confirmDelete', { name: organization.name }))) return

  pendingId.value = `delete:${organization.id}`
  error.value = ''
  try {
    await axios.delete(`${props.apiUrl}/organizations/${organization.id}`, authConfig())
    await refreshAfterMutation(organization.parent_id || null)
  } catch (requestError) {
    handleRequestError(requestError, props.t('organizations.failedDelete'))
  } finally {
    pendingId.value = ''
  }
}

onMounted(loadOrganizations)
</script>

<template>
  <div class="space-y-6 max-w-7xl mx-auto animate-slide-up">
    <div class="glass-panel rounded-2xl p-6 border border-white/10 flex flex-col xl:flex-row xl:items-start xl:justify-between gap-5">
      <div>
        <div class="flex items-center gap-2 text-cyan-300 text-xs uppercase tracking-[0.22em]">
          <Building2 class="w-4 h-4" />
          {{ t('organizations.accessEyebrow') }}
        </div>
        <h1 class="mt-3 text-3xl font-bold text-white">{{ t('organizations.title') }}</h1>
        <p class="mt-2 text-sm text-slate-400">{{ t('organizations.subtitle') }}</p>
      </div>

      <button
        type="button"
        class="px-4 py-2.5 rounded-xl border border-white/10 bg-white/5 hover:bg-white/10 text-slate-100 text-sm font-semibold transition-colors inline-flex items-center gap-2"
        @click="loadOrganizations"
      >
        <RefreshCw :class="['w-4 h-4', isLoading ? 'animate-spin' : '']" />
        {{ t('accountManagement.refresh') }}
      </button>
    </div>

    <div v-if="error" class="rounded-2xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
      {{ error }}
    </div>

    <div class="grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_390px] gap-6 items-start">
      <section class="glass-panel rounded-2xl overflow-hidden border border-white/10 min-w-0">
        <div class="p-6 border-b border-white/5 flex flex-col md:flex-row md:items-center md:justify-between gap-4">
          <div>
            <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('organizations.hierarchyView') }}</p>
            <h2 class="mt-2 text-xl font-bold text-white">{{ t('organizations.count', { count: flatOrganizations.length }) }}</h2>
          </div>

          <button
            type="button"
            class="inline-flex items-center justify-center gap-2 rounded-lg border border-cyan-500/30 bg-cyan-500/10 px-3 py-2 text-sm font-semibold text-cyan-200 transition-colors hover:bg-cyan-500/20"
            @click="prepareRoot"
          >
            <Plus class="w-4 h-4" />
            {{ t('organizations.newRoot') }}
          </button>
        </div>

        <div v-if="isLoading" class="px-6 py-10 text-sm text-slate-400">
          {{ t('organizations.loading') }}
        </div>

        <div v-else-if="flatOrganizations.length === 0" class="px-6 py-12">
          <div class="flex min-h-48 flex-col items-center justify-center text-center">
            <div class="rounded-full border border-white/10 bg-white/5 p-4 text-slate-400">
              <Building2 class="w-8 h-8" />
            </div>
            <p class="mt-5 text-sm text-slate-400">{{ t('organizations.empty') }}</p>
            <button
              type="button"
              class="mt-5 inline-flex items-center justify-center gap-2 rounded-lg bg-cyber-primary px-4 py-2.5 text-sm font-bold text-black transition-colors hover:bg-cyan-400"
              @click="prepareRoot"
            >
              <Plus class="w-4 h-4" />
              {{ t('organizations.emptyAction') }}
            </button>
          </div>
        </div>

        <div v-else class="p-6 space-y-5">
          <div class="rounded-lg border border-white/10 bg-black/20 px-4 py-3">
            <div class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('organizations.currentPath') }}</div>
            <div v-if="selectedPath.length" class="mt-2 flex flex-wrap items-center gap-2 text-sm text-slate-300">
              <template v-for="(organization, index) in selectedPath" :key="organization.id">
                <span :class="index === selectedPath.length - 1 ? 'font-semibold text-white' : 'text-slate-400'">
                  {{ organization.name }}
                </span>
                <ChevronRight v-if="index < selectedPath.length - 1" class="w-4 h-4 text-slate-600" />
              </template>
            </div>
            <div v-else class="mt-2 text-sm text-slate-500">
              {{ t('organizations.noSelection') }}
            </div>
          </div>

          <div class="overflow-x-auto pb-2">
            <div class="flex min-h-[420px] gap-4">
              <div
                v-for="column in levelColumns"
                :key="column.key"
                class="w-[280px] flex-none rounded-xl border border-white/10 bg-black/20 p-3"
              >
                <div class="mb-3 flex items-center justify-between gap-3 px-1">
                  <div class="min-w-0">
                    <p class="text-xs uppercase tracking-[0.18em] text-slate-500">
                      {{ column.depth === 0 ? t('organizations.rootLevel') : t('organizations.levelLabel', { level: column.depth + 1 }) }}
                    </p>
                    <p v-if="column.parent" class="mt-1 truncate text-sm font-semibold text-slate-200">
                      {{ column.parent.name }}
                    </p>
                  </div>
                  <span class="shrink-0 rounded-full border border-white/10 bg-white/5 px-2 py-1 text-xs font-mono text-slate-400">
                    {{ column.organizations.length }}
                  </span>
                </div>

                <div class="max-h-[520px] space-y-2 overflow-y-auto pr-1">
                  <button
                    v-for="organization in column.organizations"
                    :key="organization.id"
                    type="button"
                    :aria-pressed="selectedOrganizationId === organization.id"
                    :class="[
                      'group w-full rounded-lg border px-3 py-3 text-left transition-all',
                      selectedOrganizationId === organization.id
                        ? 'border-cyan-400/70 bg-cyan-500/15 text-white shadow-[0_0_20px_rgba(0,243,255,0.12)]'
                        : 'border-white/10 bg-slate-950/50 text-slate-300 hover:border-cyan-500/30 hover:bg-white/5'
                    ]"
                    @click="selectOrganization(organization)"
                  >
                    <div class="flex items-start justify-between gap-3">
                      <div class="min-w-0">
                        <div class="truncate text-sm font-semibold">{{ organization.name }}</div>
                        <div class="mt-1 text-xs text-slate-500">
                          {{ t('organizations.childrenSummary', { count: childCount(organization) }) }}
                        </div>
                      </div>
                      <ChevronRight
                        v-if="childCount(organization) > 0"
                        :class="[
                          'mt-0.5 h-4 w-4 shrink-0 transition-colors',
                          column.selectedId === organization.id ? 'text-cyan-300' : 'text-slate-600 group-hover:text-slate-300'
                        ]"
                      />
                    </div>
                    <div
                      v-if="selectedOrganizationId === organization.id"
                      class="mt-3 inline-flex rounded-full border border-cyan-400/30 bg-cyan-400/10 px-2 py-1 text-xs font-semibold text-cyan-200"
                    >
                      {{ t('organizations.selected') }}
                    </div>
                  </button>
                </div>
              </div>

              <div
                v-if="selectedOrganization && childCount(selectedOrganization) === 0"
                class="w-[280px] flex-none rounded-xl border border-dashed border-white/10 bg-black/10 p-4"
              >
                <p class="text-xs uppercase tracking-[0.18em] text-slate-500">
                  {{ t('organizations.levelLabel', { level: selectedPath.length + 1 }) }}
                </p>
                <div class="mt-28 text-center text-sm text-slate-500">
                  {{ t('organizations.levelEmpty') }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      <aside class="glass-panel rounded-2xl border border-white/10 p-6 xl:sticky xl:top-6">
        <div class="space-y-6">
          <section>
            <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
              <Building2 class="w-4 h-4 text-cyan-300" />
              {{ t('organizations.editOrganization') }}
            </div>

            <div v-if="selectedOrganization && editForms[selectedOrganization.id]" class="mt-5 space-y-4">
              <div>
                <p class="text-sm font-semibold text-white">{{ selectedOrganization.name }}</p>
                <p class="mt-1 text-xs font-mono text-slate-600">{{ selectedOrganization.path || '--' }}</p>
              </div>

              <div class="space-y-2">
                <label class="text-sm font-medium text-slate-300">{{ t('organizations.name') }}</label>
                <input
                  v-model="editForms[selectedOrganization.id].name"
                  type="text"
                  autocomplete="off"
                  class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600"
                >
              </div>

              <div class="space-y-2">
                <label class="text-sm font-medium text-slate-300">{{ t('organizations.parent') }}</label>
                <select
                  v-model="editForms[selectedOrganization.id].parent_id"
                  class="w-full px-4 py-3 bg-slate-900/80 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white"
                >
                  <option value="">{{ t('organizations.root') }}</option>
                  <option
                    v-for="parent in availableParents(selectedOrganization)"
                    :key="parent.id"
                    :value="String(parent.id)"
                  >
                    {{ parent.displayName }}
                  </option>
                </select>
              </div>

              <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-1 2xl:grid-cols-2 gap-2">
                <button
                  type="button"
                  :disabled="pendingId === `update:${selectedOrganization.id}`"
                  class="inline-flex items-center justify-center gap-2 rounded-lg border border-cyan-500/30 bg-cyan-500/10 px-3 py-2.5 text-sm font-semibold text-cyan-200 transition-colors hover:bg-cyan-500/20 disabled:cursor-not-allowed disabled:opacity-50"
                  @click="updateOrganization(selectedOrganization)"
                >
                  <Save class="w-4 h-4" />
                  {{ t('organizations.save') }}
                </button>
                <button
                  type="button"
                  :disabled="pendingId === `delete:${selectedOrganization.id}`"
                  class="inline-flex items-center justify-center gap-2 rounded-lg border border-rose-500/30 bg-rose-500/10 px-3 py-2.5 text-sm font-semibold text-rose-200 transition-colors hover:bg-rose-500/20 disabled:cursor-not-allowed disabled:opacity-50"
                  @click="deleteOrganization(selectedOrganization)"
                >
                  <Trash2 class="w-4 h-4" />
                  {{ t('organizations.delete') }}
                </button>
              </div>
            </div>

            <div v-else class="mt-5 rounded-lg border border-dashed border-white/10 bg-black/20 px-4 py-8 text-center">
              <p class="text-sm font-semibold text-slate-300">{{ t('organizations.noSelection') }}</p>
              <p class="mt-2 text-sm text-slate-500">{{ t('organizations.selectToEdit') }}</p>
            </div>
          </section>

          <section class="border-t border-white/10 pt-6">
            <div class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
                  <Plus class="w-4 h-4 text-cyan-300" />
                  {{ t('organizations.new') }}
                </div>
                <h3 class="mt-2 truncate text-lg font-bold text-white">{{ createTitle }}</h3>
              </div>
            </div>

            <div class="mt-4 grid grid-cols-2 gap-2">
              <button
                type="button"
                :class="[
                  'inline-flex items-center justify-center gap-2 rounded-lg border px-3 py-2 text-sm font-semibold transition-colors',
                  createMode === 'root'
                    ? 'border-cyan-400/50 bg-cyan-500/15 text-cyan-100'
                    : 'border-white/10 bg-white/5 text-slate-300 hover:bg-white/10'
                ]"
                @click="prepareRoot"
              >
                <Plus class="w-4 h-4" />
                {{ t('organizations.newRoot') }}
              </button>
              <button
                type="button"
                :disabled="!selectedOrganization"
                :class="[
                  'inline-flex items-center justify-center gap-2 rounded-lg border px-3 py-2 text-sm font-semibold transition-colors disabled:cursor-not-allowed disabled:opacity-50',
                  createMode === 'child'
                    ? 'border-cyan-400/50 bg-cyan-500/15 text-cyan-100'
                    : 'border-white/10 bg-white/5 text-slate-300 hover:bg-white/10'
                ]"
                @click="prepareChild()"
              >
                <Plus class="w-4 h-4" />
                {{ t('organizations.newChild') }}
              </button>
            </div>

            <div class="mt-4 rounded-lg border border-white/10 bg-black/20 px-4 py-3 text-sm">
              <div class="text-xs uppercase tracking-[0.18em] text-slate-500">
                {{ createMode === 'child' && createParentOrganization ? t('organizations.parentPreview') : t('organizations.rootPreview') }}
              </div>
              <div class="mt-2 font-semibold text-slate-200">
                {{ createMode === 'child' && createParentOrganization ? createParentOrganization.name : t('organizations.root') }}
              </div>
            </div>

            <div class="mt-4 space-y-4">
              <div class="space-y-2">
                <label class="text-sm font-medium text-slate-300">{{ t('organizations.name') }}</label>
                <input
                  v-model="createForm.name"
                  type="text"
                  autocomplete="off"
                  class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600"
                >
              </div>

              <button
                type="button"
                :disabled="isCreating || (createMode === 'child' && !createParentOrganization)"
                class="w-full py-3 bg-cyber-primary text-black font-bold rounded-lg hover:bg-cyan-400 transition-colors disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center justify-center gap-2"
                @click="createOrganization"
              >
                <Plus class="w-4 h-4" />
                {{ isCreating ? t('organizations.creating') : t('organizations.create') }}
              </button>
            </div>
          </section>
        </div>
      </aside>
    </div>
  </div>
</template>
