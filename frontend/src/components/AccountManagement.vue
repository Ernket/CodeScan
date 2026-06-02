<script setup>
import { computed, onMounted, ref } from 'vue'
import axios from 'axios'
import { Building2, KeyRound, Plus, Power, RefreshCw, Save, ShieldCheck, Trash2, UserPlus } from 'lucide-vue-next'

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
  organizations: {
    type: Array,
    default: () => [],
  },
})

const emit = defineEmits(['auth-expired'])

const users = ref([])
const isLoading = ref(false)
const isCreating = ref(false)
const pendingUserId = ref('')
const error = ref('')
const createForm = ref({
  username: '',
  password: '',
  organization_assignments: [],
})
const resetPasswords = ref({})
const assignmentEditors = ref({})

const organizationRoleOptions = computed(() => [
  { value: 'member', label: props.t('accountManagement.organizationRoles.member') },
  { value: 'admin', label: props.t('accountManagement.organizationRoles.admin') },
])

const accountErrorKeys = {
  'Failed to list users': 'failedLoadAccounts',
  'Username and password are required': 'usernamePasswordRequired',
  'Failed to hash password': 'failedHashPassword',
  'Username already exists': 'usernameAlreadyExists',
  'enabled is required': 'enabledRequired',
  'User not found': 'userNotFound',
  'Failed to inspect super admin accounts': 'failedInspectSuperAdmins',
  'Cannot disable the last enabled super admin': 'cannotDisableLastSuperAdmin',
  'Failed to update user': 'failedUpdateAccount',
  'Failed to reload user': 'failedReloadUser',
  'Password is required': 'passwordRequired',
  'Failed to reset password': 'failedResetPassword',
  'Invalid user id': 'invalidUserId',
  'Organization role must be member or admin': 'organizationRoleMustBeMemberOrAdmin',
  'User or organization not found': 'userOrOrganizationNotFound',
  'Failed to update organization assignments': 'failedUpdateOrganizationAssignments',
}

const enabledUsers = computed(() => users.value.filter(user => user.enabled).length)

function authConfig() {
  return {
    headers: {
      Authorization: `Bearer ${props.authToken}`,
    },
  }
}

function defaultAssignment() {
  return {
    organization_id: props.organizations[0]?.id ? String(props.organizations[0].id) : '',
    role: 'member',
  }
}

function normalizeAssignments(assignments = []) {
  const byOrganization = new Map()
  for (const assignment of assignments) {
    const organizationId = Number(assignment.organization_id)
    if (!organizationId) continue
    const role = assignment.role === 'admin' ? 'admin' : 'member'
    const previous = byOrganization.get(organizationId)
    byOrganization.set(organizationId, previous === 'admin' || role === 'admin' ? 'admin' : 'member')
  }
  return Array.from(byOrganization, ([organization_id, role]) => ({ organization_id, role }))
    .sort((left, right) => left.organization_id - right.organization_id)
}

function cloneAssignments(assignments = []) {
  return normalizeAssignments(assignments).map((assignment) => ({
    organization_id: String(assignment.organization_id),
    role: assignment.role,
  }))
}

function syncAssignmentEditors(nextUsers) {
  assignmentEditors.value = Object.fromEntries(nextUsers.map((user) => [
    user.id,
    cloneAssignments(user.organization_assignments || []),
  ]))
}

function isSuperAdmin(user) {
  return user?.is_super_admin === true || user?.role === 'super_admin'
}

function statusLabel(enabled) {
  return props.t(enabled ? 'accountManagement.enabled' : 'accountManagement.disabled')
}

function statusClass(enabled) {
  return enabled
    ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
    : 'border-slate-500/30 bg-slate-500/10 text-slate-300'
}

function assignmentLabel(assignment) {
  const organization = props.organizations.find(item => Number(item.id) === Number(assignment.organization_id))
  return organization?.displayName?.trim() || organization?.name || assignment.organization?.name || `#${assignment.organization_id}`
}

function assignmentRoleClass(role) {
  return role === 'admin'
    ? 'border-cyan-500/30 bg-cyan-500/10 text-cyan-200'
    : 'border-slate-500/30 bg-slate-500/10 text-slate-300'
}

function assignmentRoleLabel(role) {
  const normalized = role === 'admin' ? 'admin' : 'member'
  return props.t(`accountManagement.organizationRoles.${normalized}`)
}

function handleRequestError(requestError, fallback) {
  if (requestError.response?.status === 401) {
    emit('auth-expired')
    return
  }
  error.value = translateAccountError(requestError.response?.data?.error, fallback)
}

function translateAccountError(message, fallback) {
  const trimmed = String(message || '').trim()
  const key = accountErrorKeys[trimmed]
  if (!key) return fallback
  const translated = props.t(`accountManagement.${key}`)
  return translated === `accountManagement.${key}` ? trimmed : translated
}

async function loadUsers() {
  isLoading.value = true
  error.value = ''
  try {
    const { data } = await axios.get(`${props.apiUrl}/users`, authConfig())
    users.value = Array.isArray(data) ? data : []
    syncAssignmentEditors(users.value)
  } catch (requestError) {
    handleRequestError(requestError, props.t('accountManagement.failedLoadAccounts'))
  } finally {
    isLoading.value = false
  }
}

async function createUser() {
  if (!createForm.value.username || !createForm.value.password) {
    error.value = props.t('accountManagement.usernamePasswordRequired')
    return
  }

  isCreating.value = true
  error.value = ''
  try {
    await axios.post(`${props.apiUrl}/users`, {
      username: createForm.value.username,
      password: createForm.value.password,
      organization_assignments: normalizeAssignments(createForm.value.organization_assignments),
    }, authConfig())
    createForm.value = { username: '', password: '', organization_assignments: [] }
    await loadUsers()
  } catch (requestError) {
    handleRequestError(requestError, props.t('accountManagement.failedCreateAccount'))
  } finally {
    isCreating.value = false
  }
}

function addCreateAssignment() {
  createForm.value.organization_assignments.push(defaultAssignment())
}

function removeCreateAssignment(index) {
  createForm.value.organization_assignments.splice(index, 1)
}

function addUserAssignment(user) {
  const current = assignmentEditors.value[user.id] || []
  assignmentEditors.value = {
    ...assignmentEditors.value,
    [user.id]: [...current, defaultAssignment()],
  }
}

function removeUserAssignment(user, index) {
  const current = [...(assignmentEditors.value[user.id] || [])]
  current.splice(index, 1)
  assignmentEditors.value = {
    ...assignmentEditors.value,
    [user.id]: current,
  }
}

async function saveUserAssignments(user) {
  pendingUserId.value = `organizations:${user.id}`
  error.value = ''
  try {
    await axios.put(`${props.apiUrl}/users/${user.id}/organizations`, {
      assignments: normalizeAssignments(assignmentEditors.value[user.id] || []),
    }, authConfig())
    await loadUsers()
  } catch (requestError) {
    handleRequestError(requestError, props.t('accountManagement.failedUpdateOrganizationAssignments'))
  } finally {
    pendingUserId.value = ''
  }
}

async function setUserEnabled(user, enabled) {
  pendingUserId.value = `status:${user.id}`
  error.value = ''
  try {
    await axios.patch(`${props.apiUrl}/users/${user.id}/status`, { enabled }, authConfig())
    await loadUsers()
  } catch (requestError) {
    handleRequestError(requestError, props.t('accountManagement.failedUpdateAccount'))
  } finally {
    pendingUserId.value = ''
  }
}

async function resetPassword(user) {
  const password = resetPasswords.value[user.id] || ''
  if (!password) {
    error.value = props.t('accountManagement.enterNewPassword')
    return
  }

  pendingUserId.value = `password:${user.id}`
  error.value = ''
  try {
    await axios.post(`${props.apiUrl}/users/${user.id}/password`, { password }, authConfig())
    resetPasswords.value = { ...resetPasswords.value, [user.id]: '' }
  } catch (requestError) {
    handleRequestError(requestError, props.t('accountManagement.failedResetPassword'))
  } finally {
    pendingUserId.value = ''
  }
}

onMounted(loadUsers)
</script>

<template>
  <div class="space-y-6 max-w-7xl mx-auto animate-slide-up">
    <div class="glass-panel rounded-2xl p-6 border border-white/10 flex flex-col xl:flex-row xl:items-start xl:justify-between gap-5">
      <div>
        <div class="flex items-center gap-2 text-cyan-300 text-xs uppercase tracking-[0.22em]">
          <ShieldCheck class="w-4 h-4" />
          {{ t('accountManagement.eyebrow') }}
        </div>
        <h1 class="mt-3 text-3xl font-bold text-white">{{ t('accountManagement.title') }}</h1>
        <p class="mt-2 text-sm text-slate-400">{{ t('accountManagement.subtitle') }}</p>
      </div>

      <button
        type="button"
        class="px-4 py-2.5 rounded-xl border border-white/10 bg-white/5 hover:bg-white/10 text-slate-100 text-sm font-semibold transition-colors inline-flex items-center gap-2"
        @click="loadUsers"
      >
        <RefreshCw :class="['w-4 h-4', isLoading ? 'animate-spin' : '']" />
        {{ t('accountManagement.refresh') }}
      </button>
    </div>

    <div v-if="error" class="rounded-2xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
      {{ error }}
    </div>

    <div class="grid grid-cols-1 xl:grid-cols-[0.9fr_1.4fr] gap-6">
      <section class="glass-panel rounded-2xl p-6 border border-white/10">
        <div class="flex items-center gap-2 text-xs uppercase tracking-[0.22em] text-slate-500">
          <UserPlus class="w-4 h-4 text-cyan-300" />
          {{ t('accountManagement.newAccount') }}
        </div>

        <div class="mt-5 space-y-4">
          <div class="space-y-2">
            <label class="text-sm font-medium text-slate-300">{{ t('accountManagement.username') }}</label>
            <input
              v-model="createForm.username"
              type="text"
              autocomplete="off"
              class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-xl focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600"
            >
          </div>

          <div class="space-y-2">
            <label class="text-sm font-medium text-slate-300">{{ t('accountManagement.password') }}</label>
            <input
              v-model="createForm.password"
              type="password"
              autocomplete="new-password"
              class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-xl focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600"
            >
          </div>

          <div class="space-y-3">
            <div class="flex items-center justify-between gap-3">
              <label class="text-sm font-medium text-slate-300">{{ t('accountManagement.organizationAccessTitle') }}</label>
              <button
                type="button"
                :disabled="organizations.length === 0"
                class="inline-flex items-center gap-1.5 rounded-lg border border-white/10 bg-white/5 px-2.5 py-1.5 text-xs font-semibold text-slate-200 transition-colors hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
                @click="addCreateAssignment"
              >
                <Plus class="w-3.5 h-3.5" />
                {{ t('organizations.add') }}
              </button>
            </div>

            <div v-if="createForm.organization_assignments.length > 0" class="space-y-2">
              <div
                v-for="(assignment, index) in createForm.organization_assignments"
                :key="index"
                class="grid grid-cols-[1fr_auto_auto] gap-2"
              >
                <select
                  v-model="assignment.organization_id"
                  class="min-w-0 px-3 py-2 bg-slate-900/80 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white text-sm"
                >
                  <option value="" disabled>{{ t('accountManagement.selectOrganization') }}</option>
                  <option v-for="organization in organizations" :key="organization.id" :value="String(organization.id)">
                    {{ organization.displayName }}
                  </option>
                </select>
                <select
                  v-model="assignment.role"
                  class="px-3 py-2 bg-slate-900/80 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white text-sm"
                >
                  <option v-for="option in organizationRoleOptions" :key="option.value" :value="option.value">
                    {{ option.label }}
                  </option>
                </select>
                <button
                  type="button"
                  class="rounded-lg border border-rose-500/30 bg-rose-500/10 p-2 text-rose-200 transition-colors hover:bg-rose-500/20"
                  @click="removeCreateAssignment(index)"
                >
                  <Trash2 class="w-4 h-4" />
                </button>
              </div>
            </div>
            <p v-else class="rounded-xl border border-white/10 bg-black/20 px-3 py-2 text-xs text-slate-500">
              {{ t('accountManagement.noOrganizationAccessAssigned') }}
            </p>
          </div>

          <button
            type="button"
            :disabled="isCreating"
            class="w-full py-3 bg-cyber-primary text-black font-bold rounded-xl hover:bg-cyan-400 transition-colors disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center justify-center gap-2"
            @click="createUser"
          >
            <UserPlus class="w-4 h-4" />
            {{ isCreating ? t('accountManagement.creating') : t('accountManagement.createAccount') }}
          </button>
        </div>
      </section>

      <section class="glass-panel rounded-2xl overflow-hidden border border-white/10">
        <div class="p-6 border-b border-white/5 flex items-center justify-between gap-4">
          <div>
            <p class="text-xs uppercase tracking-[0.22em] text-slate-500">{{ t('accountManagement.directory') }}</p>
            <h2 class="mt-2 text-xl font-bold text-white">{{ t('accountManagement.accountCount', { count: users.length }) }}</h2>
          </div>
          <div class="rounded-xl border border-white/10 bg-white/5 px-4 py-3 text-sm text-slate-300">
            {{ t('accountManagement.enabledCount', { count: enabledUsers }) }}
          </div>
        </div>

        <div v-if="isLoading" class="px-6 py-10 text-sm text-slate-400">
          {{ t('accountManagement.loadingAccounts') }}
        </div>

        <div v-else class="divide-y divide-white/5">
          <div
            v-for="user in users"
            :key="user.id"
            class="px-6 py-5"
          >
            <div class="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-4">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="text-lg font-semibold text-white">{{ user.username }}</h3>
                  <span
                    v-if="isSuperAdmin(user)"
                    class="rounded-full border border-cyan-500/30 bg-cyan-500/10 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide text-cyan-200"
                  >
                    {{ t('accountManagement.superAdmin') }}
                  </span>
                  <span :class="['rounded-full border px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide', statusClass(user.enabled)]">
                    {{ statusLabel(user.enabled) }}
                  </span>
                </div>
                <div class="mt-2 text-xs text-slate-500">
                  {{ t('accountManagement.created') }} {{ user.created_at || '--' }}
                </div>
                <div class="mt-3 flex flex-wrap gap-2">
                  <span
                    v-for="assignment in user.organization_assignments || []"
                    :key="`${user.id}:${assignment.organization_id}`"
                    :class="['inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-[11px] font-semibold', assignmentRoleClass(assignment.role)]"
                  >
                    <Building2 class="w-3 h-3" />
                    {{ assignmentLabel(assignment) }} - {{ assignmentRoleLabel(assignment.role) }}
                  </span>
                  <span v-if="!user.organization_assignments?.length" class="text-xs text-slate-600">
                    {{ t('accountManagement.noOrganizationAccess') }}
                  </span>
                </div>
              </div>

              <div class="flex flex-col sm:flex-row gap-3 sm:items-center">
                <div class="relative">
                  <KeyRound class="absolute left-3 top-2.5 h-4 w-4 text-slate-500" />
                  <input
                    v-model="resetPasswords[user.id]"
                    type="password"
                    autocomplete="new-password"
                    :placeholder="t('accountManagement.newPassword')"
                    class="w-full sm:w-48 pl-9 pr-3 py-2 bg-slate-900/50 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600 text-sm"
                  >
                </div>

                <button
                  type="button"
                  :disabled="pendingUserId === `password:${user.id}`"
                  class="px-3 py-2 rounded-lg border border-white/10 bg-white/5 hover:bg-white/10 text-slate-100 text-sm font-semibold transition-colors disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center justify-center gap-2"
                  @click="resetPassword(user)"
                >
                  <KeyRound class="w-4 h-4" />
                  {{ t('accountManagement.resetPassword') }}
                </button>

                <button
                  type="button"
                  :disabled="pendingUserId === `status:${user.id}`"
                  :class="[
                    'px-3 py-2 rounded-lg border text-sm font-semibold transition-colors disabled:opacity-50 disabled:cursor-not-allowed inline-flex items-center justify-center gap-2',
                    user.enabled
                      ? 'border-rose-500/30 bg-rose-500/10 text-rose-200 hover:bg-rose-500/20'
                      : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200 hover:bg-emerald-500/20'
                  ]"
                  @click="setUserEnabled(user, !user.enabled)"
                >
                  <Power class="w-4 h-4" />
                  {{ user.enabled ? t('accountManagement.disable') : t('accountManagement.enable') }}
                </button>
              </div>
            </div>

            <div class="mt-5 rounded-2xl border border-white/10 bg-black/20 p-4">
              <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
                <div>
                  <div class="flex items-center gap-2 text-sm font-semibold text-slate-200">
                    <Building2 class="w-4 h-4 text-cyan-300" />
                    {{ t('accountManagement.organizationAccessTitle') }}
                  </div>
                  <p class="mt-1 text-xs text-slate-500">{{ t('accountManagement.organizationAccessDescription') }}</p>
                </div>
                <div class="flex flex-wrap gap-2">
                  <button
                    type="button"
                    :disabled="organizations.length === 0"
                    class="inline-flex items-center justify-center gap-2 rounded-lg border border-white/10 bg-white/5 px-3 py-2 text-sm font-semibold text-slate-100 transition-colors hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
                    @click="addUserAssignment(user)"
                  >
                    <Plus class="w-4 h-4" />
                    {{ t('organizations.add') }}
                  </button>
                  <button
                    type="button"
                    :disabled="pendingUserId === `organizations:${user.id}`"
                    class="inline-flex items-center justify-center gap-2 rounded-lg border border-cyan-500/30 bg-cyan-500/10 px-3 py-2 text-sm font-semibold text-cyan-200 transition-colors hover:bg-cyan-500/20 disabled:cursor-not-allowed disabled:opacity-50"
                    @click="saveUserAssignments(user)"
                  >
                    <Save class="w-4 h-4" />
                    {{ t('organizations.save') }}
                  </button>
                </div>
              </div>

              <div v-if="(assignmentEditors[user.id] || []).length > 0" class="mt-4 space-y-2">
                <div
                  v-for="(assignment, index) in assignmentEditors[user.id]"
                  :key="`${user.id}:editor:${index}`"
                  class="grid grid-cols-[minmax(0,1fr)_auto_auto] gap-2"
                >
                  <select
                    v-model="assignment.organization_id"
                    class="min-w-0 px-3 py-2 bg-slate-900/80 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white text-sm"
                  >
                    <option value="" disabled>{{ t('accountManagement.selectOrganization') }}</option>
                    <option v-for="organization in organizations" :key="organization.id" :value="String(organization.id)">
                      {{ organization.displayName }}
                    </option>
                  </select>
                  <select
                    v-model="assignment.role"
                    class="px-3 py-2 bg-slate-900/80 border border-slate-600 rounded-lg focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white text-sm"
                  >
                    <option v-for="option in organizationRoleOptions" :key="option.value" :value="option.value">
                      {{ option.label }}
                    </option>
                  </select>
                  <button
                    type="button"
                    class="rounded-lg border border-rose-500/30 bg-rose-500/10 p-2 text-rose-200 transition-colors hover:bg-rose-500/20"
                    @click="removeUserAssignment(user, index)"
                  >
                    <Trash2 class="w-4 h-4" />
                  </button>
                </div>
              </div>
              <p v-else class="mt-4 rounded-xl border border-white/10 bg-black/20 px-3 py-2 text-xs text-slate-500">
                {{ t('accountManagement.noOrganizationAccessAssigned') }}
              </p>
            </div>
          </div>

          <div v-if="users.length === 0" class="px-6 py-10 text-sm text-slate-400">
            {{ t('accountManagement.noAccounts') }}
          </div>
        </div>
      </section>
    </div>
  </div>
</template>
