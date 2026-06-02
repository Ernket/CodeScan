<script setup>
import { ref, onMounted, onBeforeUnmount, computed, watch } from 'vue'
import axios from 'axios'
import {
  Lock, Upload, Trash2, Play, Pause, RefreshCw, Server, Shield, ShieldAlert,
  FileCode, XCircle, Terminal, Activity, Zap,
  LayoutDashboard, FolderOpen, LogOut, ChevronRight, Download, UserCog, User, KeyRound, Building2
} from 'lucide-vue-next'
import DashboardOverview from './components/DashboardOverview.vue'
import TaskStageStrip from './components/TaskStageStrip.vue'
import AuditStageView from './components/AuditStageView.vue'
import TaskOrchestrationWorkbench from './components/TaskOrchestrationWorkbench.vue'
import AccountManagement from './components/AccountManagement.vue'
import OrganizationManagement from './components/OrganizationManagement.vue'
import { DEFAULT_LOCALE, LOCALE_STORAGE_KEY, getIntlLocale, getMessage } from './i18n'
import { buildTaskOrchestrationPreview, pickDefaultRerunStages } from './utils/orchestration'
import {
  buildSeverityBreakdown,
  countRouteInventory,
  effectiveSeverity,
  formatTriggerLabel,
  formatTriggerSignature,
  isStageExportable,
  parseResultArray,
  splitFindings,
  normalizeSeverity,
} from './utils/findings'

const API_URL = '/api'

const createEmptyStats = () => ({
  projects: 0,
  interfaces: 0,
  vulns: 0,
  completed_audits: 0,
  status_breakdown: {
    pending: 0,
    running: 0,
    paused: 0,
    completed: 0,
    failed: 0,
  },
  severity_breakdown: [],
  stage_breakdown: [],
})

const getStoredLocale = () => {
  if (typeof window === 'undefined') return DEFAULT_LOCALE
  const stored = localStorage.getItem(LOCALE_STORAGE_KEY)
  return stored === 'en' ? 'en' : DEFAULT_LOCALE
}

const isAuthenticated = ref(false)
const authKey = ref('')
const loginForm = ref({ username: '', password: '' })
const currentUser = ref(null)
const locale = ref(getStoredLocale())
const currentView = ref('dashboard')
const selectedTask = ref(null)
const selectedTaskId = ref('')
const tasks = ref([])
const stats = ref(createEmptyStats())
const accessibleOrganizations = ref([])
const showUploadModal = ref(false)
const showPasswordModal = ref(false)
const rerunModalOpen = ref(false)
const uploadForm = ref({ name: '', remark: '', file: null, organization_id: '' })
const passwordForm = ref({ currentPassword: '', newPassword: '', confirmPassword: '' })
const passwordError = ref('')
const isUploading = ref(false)
const isChangingPassword = ref(false)
const isRepairing = ref(false)
const isLoading = ref(false)
const isTaskLoading = ref(false)
const isDownloadingReport = ref(false)
const isStartFlowPending = ref(false)
const stageActionPending = ref({})
const sidebarOpen = ref(true)
const consoleContainer = ref(null)
const activeTab = ref('results')
const rerunSelection = ref([])
const rerunModalError = ref('')

const displayStats = ref({ projects: 0, interfaces: 0, vulns: 0, completed_audits: 0 })

const t = (key, params = {}) => getMessage(locale.value, key, params)
const formatDateTime = (value) => new Date(value).toLocaleString(getIntlLocale(locale.value))
const displayStatus = (status) => t(`status.${String(status || '').trim().toLowerCase() || 'pending'}`)
const stageErrorPattern = /error|failed|\u5931\u8d25|\u5f02\u5e38/i
const stageErrorLogLookback = 20

const stageBaseDefinitions = [
  {
    key: 'rce',
    view: 'task-rce',
    icon: ShieldAlert,
    gradientClass: 'from-red-500/15 via-red-500/5 to-transparent',
    iconClass: 'bg-red-500/10 text-red-400 border-red-500/30',
    cardClass: 'border-red-500/20'
  },
  {
    key: 'injection',
    view: 'task-injection',
    icon: ShieldAlert,
    gradientClass: 'from-amber-500/15 via-amber-500/5 to-transparent',
    iconClass: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
    cardClass: 'border-amber-500/20'
  },
  {
    key: 'auth',
    view: 'task-auth',
    icon: Lock,
    gradientClass: 'from-sky-500/15 via-sky-500/5 to-transparent',
    iconClass: 'bg-sky-500/10 text-sky-400 border-sky-500/30',
    cardClass: 'border-sky-500/20'
  },
  {
    key: 'access',
    view: 'task-access',
    icon: Shield,
    gradientClass: 'from-indigo-500/15 via-indigo-500/5 to-transparent',
    iconClass: 'bg-indigo-500/10 text-indigo-400 border-indigo-500/30',
    cardClass: 'border-indigo-500/20'
  },
  {
    key: 'xss',
    view: 'task-xss',
    icon: ShieldAlert,
    gradientClass: 'from-emerald-500/15 via-emerald-500/5 to-transparent',
    iconClass: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30',
    cardClass: 'border-emerald-500/20'
  },
  {
    key: 'config',
    view: 'task-config',
    icon: FileCode,
    gradientClass: 'from-cyan-500/15 via-cyan-500/5 to-transparent',
    iconClass: 'bg-cyan-500/10 text-cyan-400 border-cyan-500/30',
    cardClass: 'border-cyan-500/20'
  },
  {
    key: 'fileop',
    view: 'task-fileop',
    icon: FolderOpen,
    gradientClass: 'from-orange-500/15 via-orange-500/5 to-transparent',
    iconClass: 'bg-orange-500/10 text-orange-400 border-orange-500/30',
    cardClass: 'border-orange-500/20'
  },
  {
    key: 'logic',
    view: 'task-logic',
    icon: Zap,
    gradientClass: 'from-rose-500/15 via-rose-500/5 to-transparent',
    iconClass: 'bg-rose-500/10 text-rose-400 border-rose-500/30',
    cardClass: 'border-rose-500/20'
  }
]

const stageDefinitions = computed(() => stageBaseDefinitions.map((stage) => ({
  ...stage,
  label: t(`stage.${stage.key}.label`),
  shortLabel: t(`stage.${stage.key}.shortLabel`),
  description: t(`stage.${stage.key}.description`),
})))

const auditViews = computed(() => Object.fromEntries(stageDefinitions.value.map(stage => [stage.view, stage.key])))
const stageLabelByKey = computed(() => Object.fromEntries(stageDefinitions.value.map(stage => [stage.key, stage.label])))
const stageShortLabelByKey = computed(() => Object.fromEntries(stageDefinitions.value.map(stage => [stage.key, stage.shortLabel])))

const toggleLocale = () => {
  locale.value = locale.value === 'zh' ? 'en' : 'zh'
  localStorage.setItem(LOCALE_STORAGE_KEY, locale.value)
}

const stageDisplayName = (stageName) => {
  if (stageName === 'init') return t('stage.init.label')
  return stageLabelByKey.value[stageName] || stageName
}

const currentTaskName = computed(() => {
  if (currentView.value === 'dashboard') return t('app.overview')
  if (currentView.value === 'accounts') return t('app.accounts')
  if (currentView.value === 'organizations') return t('organizations.title')
  return selectedTask.value?.name || tasks.value.find(task => task.id === selectedTaskId.value)?.name || t('app.loadingTask')
})
const taskPrimaryTabs = computed(() => [
  { key: 'task-detail', label: t('taskDetail.tabs.overview') },
  { key: 'task-orchestration', label: t('taskDetail.tabs.orchestration') },
  { key: 'task-report', label: t('taskDetail.tabs.report'), badge: reportStages.value.length },
])

const severityBadgeClass = (severity) => {
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

const statusBadgeClass = (status) => {
  switch (String(status || '').trim().toLowerCase()) {
    case 'running':
      return 'bg-amber-500/10 text-amber-300 border border-amber-500/30'
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-300 border border-emerald-500/30'
    case 'failed':
      return 'bg-rose-500/10 text-rose-300 border border-rose-500/30'
    case 'paused':
      return 'bg-sky-500/10 text-sky-300 border border-sky-500/30'
    default:
      return 'bg-slate-500/10 text-slate-300 border border-slate-500/30'
  }
}

const getStageRecord = (task, stageName) => task?.stages?.find(stage => stage.name === stageName) || null

const normalizeLogText = (log) => {
  if (typeof log === 'string') return log
  if (log === null || log === undefined) return ''
  try {
    return JSON.stringify(log)
  } catch {
    return String(log)
  }
}

const stripLogTimestamp = (text) => String(text || '').replace(/^\[[^\]]+\]\s*/, '').trim()
const compactStageMessage = (text, maxLength = 180) => {
  const compacted = stripLogTimestamp(text).replace(/\s+/g, ' ')
  if (compacted.length <= maxLength) return compacted
  return `${compacted.slice(0, maxLength - 3)}...`
}

const latestStageErrorLog = (logs = []) => {
  if (!Array.isArray(logs) || logs.length === 0) return ''
  const start = Math.max(0, logs.length - stageErrorLogLookback)

  for (let index = logs.length - 1; index >= start; index--) {
    const text = normalizeLogText(logs[index])
    if (stageErrorPattern.test(text)) {
      return compactStageMessage(text)
    }
  }

  return ''
}

const stageErrorSummaries = computed(() => {
  if (!selectedTask.value) return []

  return stageDefinitions.value
    .map(definition => {
      const stage = getStageRecord(selectedTask.value, definition.key)
      if (!stage) return null

      const status = String(stage.status || '').trim().toLowerCase()
      const errorLog = latestStageErrorLog(stage.logs)
      if (status !== 'failed' && !errorLog) return null

      const resultSummary = compactStageMessage(stage.result || '')

      return {
        key: definition.key,
        view: definition.view,
        label: definition.label,
        status,
        statusLabel: displayStatus(status),
        message: errorLog || resultSummary || t('taskStrip.lastRunFailed'),
        updatedAt: stage.updated_at ? formatDateTime(stage.updated_at) : '',
      }
    })
    .filter(Boolean)
})

const currentAuditStage = computed(() => {
  if (!selectedTask.value) return null
  const stageName = auditViews.value[currentView.value]
  if (!stageName) return null
  return getStageRecord(selectedTask.value, stageName)
})

const currentLogs = computed(() => {
  if (!selectedTask.value) return []
  if (auditViews.value[currentView.value]) {
    if (!currentAuditStage.value) return []
    return currentAuditStage.value.logs || []
  }
  return selectedTask.value.logs || []
})

const parsedResult = computed(() => {
  let raw = null
  if (auditViews.value[currentView.value]) {
    if (!currentAuditStage.value) return null
    if (currentAuditStage.value.status === 'failed') {
      const outputItems = parseResultArray(currentAuditStage.value.output_json)
      if (Array.isArray(outputItems) && outputItems.length > 0) return outputItems
      return parseResultArray(currentAuditStage.value.result)
    }
    raw = currentAuditStage.value.output_json || currentAuditStage.value.result
  } else {
    if (selectedTask.value?.status === 'failed') {
      const outputItems = parseResultArray(selectedTask.value?.output_json)
      if (Array.isArray(outputItems) && outputItems.length > 0) return outputItems
      return parseResultArray(selectedTask.value?.result)
    }
    raw = selectedTask.value?.output_json || selectedTask.value?.result
  }
  return parseResultArray(raw)
})

const currentRawResult = computed(() => {
  if (auditViews.value[currentView.value]) {
    if (!currentAuditStage.value) return ''
    return currentAuditStage.value.result || ''
  }
  return selectedTask.value?.result || ''
})

const flattenOrganizationTree = (nodes = [], depth = 0) => nodes.flatMap((node) => {
  const treeDepth = Number.isInteger(node.depth) ? node.depth : depth
  const current = {
    ...node,
    treeDepth,
    displayName: `${'  '.repeat(treeDepth)}${node.name}`,
  }
  return [current, ...flattenOrganizationTree(node.children || [], treeDepth + 1)]
})

const currentAuditDefinition = computed(() => stageDefinitions.value.find(stage => stage.view === currentView.value) || null)
const normalizeTaskStatus = (task) => String(task?.status || '').trim().toLowerCase()
const currentUserRole = computed(() => currentUser.value?.role || 'observer')
const canDelete = computed(() => currentUserRole.value === 'super_admin')
const canManageUsers = computed(() => currentUserRole.value === 'super_admin')
const flatOrganizations = computed(() => flattenOrganizationTree(accessibleOrganizations.value))
const writableOrganizations = computed(() => flatOrganizations.value.filter((organization) => organization.effective_role === 'admin'))
const canCreateProject = computed(() => writableOrganizations.value.length > 0)
const canWriteTask = (task) => canDelete.value || task?.permissions?.can_write === true
const selectedTaskCanWrite = computed(() => canWriteTask(selectedTask.value))
const taskOrganizationName = (task) => task?.organization?.name || t('common.unassigned')
const canRequestTaskStart = (task = selectedTask.value) => canWriteTask(task) && ['pending', 'failed', 'completed'].includes(normalizeTaskStatus(task))
const taskStartActionLabel = (task = selectedTask.value) => (
  normalizeTaskStatus(task) === 'pending'
    ? t('taskDetail.startScan')
    : t('taskDetail.selectRerunStages')
)
const taskOrchestrationPreview = computed(() => {
  const preview = buildTaskOrchestrationPreview(selectedTask.value?.orchestration)
  return {
    ...preview,
    focusStatusLabel: t(`orchestration.state.${preview.focusStatus}`),
    currentStageLabel: preview.currentStage ? stageDisplayName(preview.currentStage) : t('taskDetail.orchestrationNoFocusedStage'),
    lastProgressLabel: preview.lastProgressAt ? formatDateTime(preview.lastProgressAt) : t('taskDetail.orchestrationNoRecentProgress'),
    latestEventAtLabel: preview.latestEventAt ? formatDateTime(preview.latestEventAt) : '',
  }
})

const summarizeStageForReport = (task, definition) => {
  const stage = getStageRecord(task, definition.key)
  if (!isStageExportable(stage)) return null

  const parsedResults = parseResultArray(stage.output_json || stage.result)
  const results = Array.isArray(parsedResults) ? parsedResults : []
  const findingGroups = splitFindings(results)
  const activeResults = findingGroups.active
  const rejectedCount = findingGroups.rejected.length
  const rawOnly = parsedResults === null
  const allRejected = !rawOnly && activeResults.length === 0 && rejectedCount > 0
  const clean = !rawOnly && activeResults.length === 0 && rejectedCount === 0
  const files = new Set()
  const interfaces = new Set()

  activeResults.forEach(item => {
    if (item?.location?.file) files.add(item.location.file)
    const trigger = formatTriggerSignature(item?.trigger)
    if (trigger) interfaces.add(trigger)
  })

  return {
    ...definition,
    stage,
    rawOnly,
    allRejected,
    clean,
    results: activeResults,
    rejectedCount,
    findingCount: activeResults.length,
    uniqueFiles: files.size,
    uniqueInterfaces: interfaces.size,
    completedAt: stage?.updated_at ? formatDateTime(stage.updated_at) : '',
    severityBreakdown: buildSeverityBreakdown(activeResults),
    summaryText: rawOnly
      ? t('reportView.rawStageNote')
      : allRejected
        ? t('auditView.allRejected')
        : clean
          ? t('reportView.cleanStageNote')
          : locale.value === 'zh'
            ? `\u5df2\u51c6\u5907\u5bfc\u51fa ${activeResults.length} \u6761\u53d1\u73b0\u3002`
            : `${activeResults.length} finding${activeResults.length === 1 ? '' : 's'} ready for export.`
  }
}

const reportStages = computed(() => {
  if (!selectedTask.value) return []
  return stageDefinitions.value
    .map(definition => summarizeStageForReport(selectedTask.value, definition))
    .filter(Boolean)
})

const reportOverview = computed(() => {
  const files = new Set()
  const interfaces = new Set()
  const allResults = []
  let totalFindings = 0
  let cleanStageCount = 0
  let rawOnlyStageCount = 0

  reportStages.value.forEach(stage => {
    totalFindings += stage.findingCount
    if (stage.rawOnly) rawOnlyStageCount += 1
    if (stage.clean) cleanStageCount += 1

    stage.results.forEach(item => {
      if (item?.location?.file) files.add(item.location.file)
      const trigger = formatTriggerSignature(item?.trigger)
      if (trigger) interfaces.add(trigger)
      allResults.push(item)
    })
  })

  const routeCount = countRouteInventory(selectedTask.value)

  return {
    stageCount: reportStages.value.length,
    totalFindings,
    cleanStageCount,
    rawOnlyStageCount,
    uniqueFiles: files.size,
    uniqueInterfaces: Math.max(routeCount, interfaces.size),
    routeCount,
    severityBreakdown: buildSeverityBreakdown(allResults)
  }
})

const stageActionKey = (stageName, action) => `${stageName}:${action}`
const isStageActionPending = (stageName, action) => Boolean(stageActionPending.value[stageActionKey(stageName, action)])
const rerunStageOptions = computed(() => {
  const task = selectedTask.value
  if (!task) return []

  const hasRoutes = countRouteInventory(task) > 0
  const initStatus = hasRoutes ? 'completed' : (String(task.status || '').trim().toLowerCase() === 'failed' ? 'failed' : 'pending')
  const initUpdatedAt = task.created_at ? formatDateTime(task.created_at) : ''
  const options = [
    {
      key: 'init',
      label: t('stage.init.label'),
      detail: hasRoutes ? t('taskDetail.routesCount', { count: countRouteInventory(task) }) : t('taskDetail.waitingForExecution'),
      status: initStatus,
      updatedAt: initUpdatedAt,
    },
  ]

  for (const definition of stageDefinitions.value) {
    const stage = getStageRecord(task, definition.key)
    options.push({
      key: definition.key,
      label: definition.label,
      detail: stage?.updated_at ? t('common.completedAt') : t('taskDetail.waitingForExecution'),
      status: stage?.status || 'pending',
      updatedAt: stage?.updated_at ? formatDateTime(stage.updated_at) : '',
    })
  }

  return options
})

const authConfig = () => ({ headers: { Authorization: `Bearer ${authKey.value}` } })

const passwordErrorKeys = {
  'Current password and new password are required': 'required',
  'Current password is incorrect': 'currentIncorrect',
  'Failed to hash password': 'failed',
  'Failed to change password': 'failed',
  'User not found': 'userNotFound',
}

const organizationErrorKeys = {
  'organization_id is required': 'organizationIdRequired',
  'Failed to inspect organization permissions': 'failedInspectPermissions',
  'Permission denied': 'permissionDenied',
}

const translateOrganizationError = (message, fallback = '') => {
  const trimmed = String(message || '').trim()
  const key = organizationErrorKeys[trimmed]
  if (key) {
    const translated = t(`organizations.${key}`)
    if (translated !== `organizations.${key}`) return translated
  }
  return trimmed || fallback
}

const translatePasswordError = (message, fallback = '') => {
  const trimmed = String(message || '').trim()
  const key = passwordErrorKeys[trimmed]
  if (key) {
    const translated = t(`password.${key}`)
    if (translated !== `password.${key}`) return translated
  }
  return trimmed || fallback || t('password.failed')
}

const resetPasswordForm = () => {
  passwordForm.value = { currentPassword: '', newPassword: '', confirmPassword: '' }
  passwordError.value = ''
}

const openPasswordModal = () => {
  resetPasswordForm()
  showPasswordModal.value = true
}

const closePasswordModal = () => {
  if (isChangingPassword.value) return
  showPasswordModal.value = false
  resetPasswordForm()
}

const submitPasswordChange = async () => {
  passwordError.value = ''
  const currentPassword = passwordForm.value.currentPassword
  const newPassword = passwordForm.value.newPassword
  const confirmPassword = passwordForm.value.confirmPassword
  if (!currentPassword || !newPassword || !confirmPassword) {
    passwordError.value = t('password.required')
    return
  }
  if (newPassword !== confirmPassword) {
    passwordError.value = t('password.mismatch')
    return
  }

  isChangingPassword.value = true
  try {
    await axios.post(`${API_URL}/me/password`, {
      current_password: currentPassword,
      new_password: newPassword,
    }, authConfig())
    alert(t('password.changed'))
    logout()
  } catch (e) {
    const message = e.response?.data?.error
    if (e.response?.status === 401 && String(message || '').trim() !== 'Current password is incorrect') {
      logout()
      return
    }
    passwordError.value = translatePasswordError(message, e.message)
  } finally {
    isChangingPassword.value = false
  }
}

const normalizeStatsResponse = (payload = {}) => ({
  ...createEmptyStats(),
  ...payload,
  status_breakdown: {
    ...createEmptyStats().status_breakdown,
    ...(payload.status_breakdown || {}),
  },
  severity_breakdown: Array.isArray(payload.severity_breakdown) ? payload.severity_breakdown : [],
  stage_breakdown: Array.isArray(payload.stage_breakdown) ? payload.stage_breakdown : [],
})

const snapshotTaskSummary = (task) => task ? {
  ...task,
  logs: task.logs || [],
  stages: task.stages || [],
  result: task.result || '',
  output_json: task.output_json || null,
} : null

let detailAbortController = null
let detailRequestSeq = 0
let detailLoadingSeq = 0

const cancelDetailRequest = () => {
  if (detailAbortController) {
    detailAbortController.abort()
    detailAbortController = null
  }
}

const isDetailCanceled = (error) => (
  axios.isCancel?.(error) ||
  error?.code === 'ERR_CANCELED' ||
  error?.name === 'CanceledError' ||
  error?.name === 'AbortError'
)

const goDashboard = () => {
  closeRerunModal()
  cancelDetailRequest()
  currentView.value = 'dashboard'
  selectedTask.value = null
  selectedTaskId.value = ''
}

const openAccounts = () => {
  if (!canManageUsers.value) return
  closeRerunModal()
  cancelDetailRequest()
  currentView.value = 'accounts'
  selectedTask.value = null
  selectedTaskId.value = ''
}

const openOrganizations = () => {
  if (!canManageUsers.value) return
  closeRerunModal()
  cancelDetailRequest()
  currentView.value = 'organizations'
  selectedTask.value = null
  selectedTaskId.value = ''
}

const handleOrganizationsUpdated = async () => {
  await fetchData()
}

const findTaskById = (id) => {
  if (selectedTask.value?.id === id) return selectedTask.value
  return tasks.value.find((task) => task.id === id) || null
}

const canWriteTaskById = (id) => canWriteTask(findTaskById(id))

const fetchTaskDetail = async (taskId = selectedTaskId.value, options = {}) => {
  if (!taskId || !isAuthenticated.value) return null

  const { silent = false, fallback = null } = options
  const requestedTaskId = String(taskId)
  const requestSeq = ++detailRequestSeq
  const showLoading = !silent

  cancelDetailRequest()

  if (fallback && String(selectedTaskId.value) === requestedTaskId) {
    selectedTask.value = snapshotTaskSummary(fallback)
  }
  if (showLoading) {
    detailLoadingSeq = requestSeq
    isTaskLoading.value = true
  }

  const controller = new AbortController()
  detailAbortController = controller

  const canCommit = () => (
    requestSeq === detailRequestSeq &&
    String(selectedTaskId.value) === requestedTaskId
  )

  try {
    const res = await axios.get(`${API_URL}/tasks/${requestedTaskId}`, {
      ...authConfig(),
      signal: controller.signal,
    })

    if (!canCommit()) return null
    if (String(res.data?.id) !== requestedTaskId) return null

    selectedTask.value = res.data
    return res.data
  } catch (e) {
    if (isDetailCanceled(e)) return null
    if (!canCommit()) return null

    console.error(e)
    if (e.response?.status === 404) {
      goDashboard()
    } else if (e.response?.status === 401) {
      logout()
    } else if (!silent) {
      alert(t('alerts.failedLoadTaskDetails'))
    }
    return null
  } finally {
    if (detailAbortController === controller) {
      detailAbortController = null
    }
    if (showLoading && detailLoadingSeq === requestSeq) {
      isTaskLoading.value = false
    }
  }
}

const runStage = async (taskId, stageName, options = {}) => {
  if (!canWriteTaskById(taskId)) return false
  const { skipConfirm = false, successMessage = t('alerts.stageStarted') } = options
  if (!skipConfirm && !confirm(t('confirm.startStage', { stage: stageDisplayName(stageName) }))) return false

  try {
    await axios.post(`${API_URL}/tasks/${taskId}/stage/${stageName}`, {}, authConfig())
    activeTab.value = 'console'
    await fetchData()
    if (successMessage) {
      alert(successMessage)
    }
    return true
  } catch (e) {
    alert(t('alerts.failedToStartStage', { message: e.response?.data?.error || e.message }))
    return false
  }
}

const startTaskPipeline = async (taskId, options = {}) => {
  if (!canWriteTaskById(taskId)) return false
  const { successMessage = t('alerts.scanStarted') } = options

  try {
    await axios.post(`${API_URL}/tasks/${taskId}/orchestration/start`, {}, authConfig())
    activeTab.value = 'console'
    await fetchData()
    if (successMessage) {
      alert(successMessage)
    }
    return true
  } catch (e) {
    alert(t('alerts.failedToStartPipeline', { message: e.response?.data?.error || e.message }))
    return false
  }
}

const closeRerunModal = () => {
  rerunModalOpen.value = false
  rerunModalError.value = ''
  rerunSelection.value = []
}

const openRerunModal = (task = selectedTask.value) => {
  if (!task) return
  rerunSelection.value = pickDefaultRerunStages(task)
  rerunModalError.value = ''
  rerunModalOpen.value = true
}

const requestTaskStart = async (task = selectedTask.value) => {
  if (!canWriteTask(task)) return false
  if (!task || isStartFlowPending.value) return false

  const status = String(task.status || '').trim().toLowerCase()
  if (status === 'pending') {
    isStartFlowPending.value = true
    try {
      return await startTaskPipeline(task.id, { successMessage: t('alerts.scanStarted') })
    } finally {
      isStartFlowPending.value = false
    }
  }

  if (status === 'failed' || status === 'completed') {
    openRerunModal(task)
    return false
  }

  return false
}

const submitRerunSelection = async () => {
  const task = selectedTask.value
  if (!canWriteTask(task)) return
  if (!task || isStartFlowPending.value) return

  if (!rerunSelection.value.length) {
    rerunModalError.value = t('taskDetail.rerunSelectAtLeastOne')
    return
  }

  isStartFlowPending.value = true
  rerunModalError.value = ''

  try {
    await axios.post(`${API_URL}/tasks/${task.id}/orchestration/rerun`, {
      selected_stages: rerunSelection.value,
    }, authConfig())
    activeTab.value = 'console'
    closeRerunModal()
    await fetchData()
    alert(t('taskDetail.rerunQueued'))
  } catch (e) {
    const message = e.response?.data?.error || e.message
    rerunModalError.value = message
  } finally {
    isStartFlowPending.value = false
  }
}

const selectFailedRerunStages = () => {
  rerunSelection.value = pickDefaultRerunStages(selectedTask.value)
}

const selectAllAuditRerunStages = () => {
  rerunSelection.value = stageDefinitions.value.map((stage) => stage.key)
}

const clearRerunStages = () => {
  rerunSelection.value = []
}

const runStagePostAction = async (taskId, stageName, actionPath, actionLabel, options = {}) => {
  if (!canWriteTaskById(taskId)) return false
  const { confirmMessage = '', successMessage = '' } = options
  if (confirmMessage && !confirm(confirmMessage)) return false
  const key = stageActionKey(stageName, actionPath)
  if (stageActionPending.value[key]) return false

  stageActionPending.value = { ...stageActionPending.value, [key]: true }
  try {
    await axios.post(`${API_URL}/tasks/${taskId}/stage/${stageName}/${actionPath}`, {}, authConfig())
    activeTab.value = 'console'
    await fetchData()
    if (successMessage) {
      alert(successMessage)
    }
    return true
  } catch (e) {
    alert(t('alerts.failedToAction', {
      action: actionLabel,
      message: e.response?.data?.error || e.message,
    }))
    return false
  } finally {
    const next = { ...stageActionPending.value }
    delete next[key]
    stageActionPending.value = next
  }
}

const runGapCheck = async (taskId, stageName) => runStagePostAction(
  taskId,
  stageName,
  'gap-check',
  t('actionNames.runGapCheck'),
  {
    confirmMessage: t('confirm.runGapCheck', { stage: stageDisplayName(stageName) }),
    successMessage: t('alerts.gapCheckStarted'),
  }
)

const revalidateStage = async (taskId, stageName) => runStagePostAction(
  taskId,
  stageName,
  'revalidate',
  t('actionNames.revalidateFindings'),
  {
    confirmMessage: t('confirm.revalidate', { stage: stageDisplayName(stageName) }),
    successMessage: t('alerts.findingRevalidationStarted'),
  }
)

const canGapCheckStage = (task, stageName) => {
  if (!canWriteTask(task)) return false
  if (!task || task.status === 'running') return false
  if (stageName === 'init') return Array.isArray(parseResultArray(task?.output_json || task?.result))
  const stage = getStageRecord(task, stageName)
  return Boolean(stage && stage.status === 'completed' && Array.isArray(parseResultArray(stage.output_json || stage.result)))
}

const canRevalidateStage = (task, stageName) => {
  if (!canWriteTask(task)) return false
  if (!task || task.status === 'running' || stageName === 'init') return false
  const stage = getStageRecord(task, stageName)
  const parsed = parseResultArray(stage?.output_json || stage?.result)
  return Boolean(stage && stage.status === 'completed' && Array.isArray(parsed) && parsed.length > 0)
}

const repairJSON = async (taskId, stageName) => {
  if (!canWriteTaskById(taskId)) return
  if (isRepairing.value) return
  isRepairing.value = true
  try {
    const response = await axios.post(`${API_URL}/tasks/${taskId}/repair?stage=${stageName}`, {}, authConfig())
    await fetchData()
    const count = Number(response.data?.count ?? (Array.isArray(response.data?.output_json) ? response.data.output_json.length : 0))
    alert(count > 0 ? t('alerts.jsonRepairedWithCount', { count }) : t('alerts.jsonRepairedEmpty'))
  } catch (e) {
    alert(t('alerts.repairFailed', { message: e.response?.data?.error || e.message }))
  } finally {
    isRepairing.value = false
  }
}

const extractFileNameFromDisposition = (headerValue) => {
  if (!headerValue) return ''

  const utfMatch = headerValue.match(/filename\*=UTF-8''([^;]+)/i)
  if (utfMatch?.[1]) {
    try {
      return decodeURIComponent(utfMatch[1])
    } catch {
      return utfMatch[1]
    }
  }

  const asciiMatch = headerValue.match(/filename="?([^";]+)"?/i)
  return asciiMatch?.[1] || ''
}

const downloadTaskReport = async (taskId) => {
  if (isDownloadingReport.value) return
  if (!reportStages.value.length) {
    alert(t('alerts.noCompletedAuditsForExport'))
    return
  }

  isDownloadingReport.value = true
  try {
    const res = await axios.get(`${API_URL}/tasks/${taskId}/report`, {
      ...authConfig(),
      responseType: 'blob'
    })

    const fallbackName = `${(selectedTask.value?.name || 'codescan-report').replace(/\s+/g, '-').toLowerCase()}-report.html`
    const fileName = extractFileNameFromDisposition(res.headers['content-disposition']) || fallbackName
    const blob = new Blob([res.data], { type: 'text/html;charset=utf-8' })
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = fileName
    document.body.appendChild(link)
    link.click()
    link.remove()
    window.URL.revokeObjectURL(url)
  } catch (e) {
    alert(t('alerts.failedToExportReport', { message: e.response?.data?.error || e.message }))
  } finally {
    isDownloadingReport.value = false
  }
}

watch(stats, (newVal) => {
  animateValue('projects', newVal.projects)
  animateValue('interfaces', newVal.interfaces)
  animateValue('vulns', newVal.vulns)
  animateValue('completed_audits', newVal.completed_audits)
})

watch(() => currentLogs.value?.length, () => {
  if (consoleContainer.value) {
    setTimeout(() => {
      consoleContainer.value.scrollTop = consoleContainer.value.scrollHeight
    }, 100)
  }
})

const animateValue = (key, target) => {
  const start = displayStats.value[key] || 0
  const duration = 1500
  const startTime = performance.now()

  const step = (currentTime) => {
    const elapsed = currentTime - startTime
    const progress = Math.min(elapsed / duration, 1)
    const ease = progress === 1 ? 1 : 1 - Math.pow(2, -10 * progress)

    displayStats.value[key] = Math.floor(start + ((target || 0) - start) * ease)

    if (progress < 1) {
      requestAnimationFrame(step)
    }
  }
  requestAnimationFrame(step)
}

const login = async () => {
  try {
    const res = await axios.post(`${API_URL}/login`, {
      username: loginForm.value.username,
      password: loginForm.value.password,
    })
    if (res.data.token) {
      localStorage.setItem('auth_token', res.data.token)
      localStorage.setItem('current_user', JSON.stringify(res.data.user || null))
      authKey.value = res.data.token
      currentUser.value = res.data.user || null
      loginForm.value.password = ''
      isAuthenticated.value = true
      await fetchData()
    }
  } catch (e) {
    alert(e.response?.data?.error || t('alerts.authenticationFailed'))
  }
}

const logout = () => {
  cancelDetailRequest()
  isAuthenticated.value = false
  showPasswordModal.value = false
  isChangingPassword.value = false
  localStorage.removeItem('auth_token')
  localStorage.removeItem('current_user')
  authKey.value = ''
  currentUser.value = null
  loginForm.value.password = ''
  resetPasswordForm()
  tasks.value = []
  accessibleOrganizations.value = []
  uploadForm.value = { name: '', remark: '', file: null, organization_id: '' }
  stats.value = createEmptyStats()
  displayStats.value = { projects: 0, interfaces: 0, vulns: 0, completed_audits: 0 }
  goDashboard()
  stopPolling()
}

const checkAuth = () => {
  const token = localStorage.getItem('auth_token')
  if (token) {
    authKey.value = token
    try {
      currentUser.value = JSON.parse(localStorage.getItem('current_user') || 'null')
    } catch {
      currentUser.value = null
    }
    isAuthenticated.value = true
    fetchData()
  }
}

const fetchData = async () => {
  if (!isAuthenticated.value) return

  isLoading.value = true
  try {
    const [statsRes, tasksRes, organizationsRes] = await Promise.all([
      axios.get(`${API_URL}/stats`, authConfig()),
      axios.get(`${API_URL}/tasks`, authConfig()),
      axios.get(`${API_URL}/organizations/accessible`, authConfig())
    ])

    stats.value = normalizeStatsResponse(statsRes.data)
    tasks.value = Array.isArray(tasksRes.data) ? tasksRes.data : []
    accessibleOrganizations.value = Array.isArray(organizationsRes.data) ? organizationsRes.data : []

    if (selectedTaskId.value) {
      const summary = tasks.value.find(task => task.id === selectedTaskId.value)
      if (!summary) {
        goDashboard()
      } else if (currentView.value !== 'dashboard') {
        const fallback = !selectedTask.value || selectedTask.value.id !== summary.id ? summary : null
        await fetchTaskDetail(selectedTaskId.value, { silent: true, fallback })
      }
    }

    startPolling()
  } catch (e) {
    console.error(e)
    if (e.response?.status === 401) logout()
  } finally {
    isLoading.value = false
  }
}

const handleFileUpload = (event) => {
  uploadForm.value.file = event.target.files[0]
}

const openUploadModal = () => {
  if (!canCreateProject.value) return
  if (!uploadForm.value.organization_id && writableOrganizations.value.length > 0) {
    uploadForm.value.organization_id = String(writableOrganizations.value[0].id)
  }
  showUploadModal.value = true
}

const createTask = async () => {
  if (!canCreateProject.value) return
  if (!uploadForm.value.organization_id) return alert(t('organizations.selectRequired'))
  if (!uploadForm.value.file) return alert(t('alerts.pleaseSelectFile'))

  const formData = new FormData()
  formData.append('name', uploadForm.value.name)
  formData.append('remark', uploadForm.value.remark)
  formData.append('organization_id', uploadForm.value.organization_id)
  formData.append('file', uploadForm.value.file)

  isUploading.value = true
  try {
    await axios.post(`${API_URL}/tasks`, formData, {
      headers: {
        Authorization: `Bearer ${authKey.value}`,
        'Content-Type': 'multipart/form-data'
      }
    })
    showUploadModal.value = false
    uploadForm.value = { name: '', remark: '', file: null, organization_id: '' }
    await fetchData()
    alert(t('alerts.projectCreated'))
  } catch (e) {
    alert(t('alerts.uploadFailed', { message: translateOrganizationError(e.response?.data?.error, e.message) }))
  } finally {
    isUploading.value = false
  }
}

const getTaskForDelete = (id) => {
  if (selectedTask.value?.id === id) return selectedTask.value
  return tasks.value.find(task => task.id === id) || null
}

const deleteTask = async (id) => {
  if (!canDelete.value) return
  const task = getTaskForDelete(id)
  if (task?.status === 'running') {
    alert(t('alerts.pauseTaskBeforeDelete'))
    return
  }
  if (!confirm(t('confirm.deleteTask'))) return
  try {
    await axios.delete(`${API_URL}/tasks/${id}`, authConfig())
    if (selectedTaskId.value === id) {
      goDashboard()
    }
    await fetchData()
  } catch (e) {
    if (e.response?.status === 409) {
      alert(t('alerts.pauseTaskBeforeDelete'))
      return
    }
    alert(t('alerts.failedToDeleteTask', { message: e.response?.data?.error || e.message }))
  }
}

const taskAction = async (id, action) => {
  const task = findTaskById(id)
  if (!canWriteTask(task)) return
  if (action === 'start') {
    await requestTaskStart(task)
    return
  }

  try {
    await axios.post(`${API_URL}/tasks/${id}/${action}`, {}, authConfig())
    await fetchData()
  } catch {
    alert(t('alerts.failedToTaskAction', { action: t(`actionNames.${action}`) }))
  }
}

const openTask = async (task) => {
  closeRerunModal()
  selectedTaskId.value = task.id
  selectedTask.value = snapshotTaskSummary(task)
  currentView.value = 'task-detail'
  activeTab.value = 'results'
  await fetchTaskDetail(task.id, { fallback: task })
}

const openStageConsole = (view) => {
  currentView.value = view
  activeTab.value = 'console'
}

let pollTimer = null
const startPolling = () => {
  stopPolling()
  const hasRunning = tasks.value.some(task => task.status === 'running')
  const interval = hasRunning ? 2000 : 5000
  pollTimer = setInterval(fetchData, interval)
}

const stopPolling = () => {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

onMounted(() => {
  checkAuth()
  startPolling()
})

onBeforeUnmount(() => {
  cancelDetailRequest()
  stopPolling()
})
</script>
<template>
  <div class="min-h-screen text-slate-200 font-sans selection:bg-cyber-primary selection:text-black">

    <!-- Login Screen -->
    <transition name="fade">
      <div v-if="!isAuthenticated" class="fixed inset-0 z-50 flex items-center justify-center overflow-hidden bg-cyber-dark">
        <!-- Static Background with subtle grid -->
        <div class="absolute inset-0 bg-grid opacity-10"></div>

        <!-- Decorative elements -->
        <div class="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-transparent via-cyber-primary to-transparent opacity-50"></div>
        <div class="absolute bottom-0 left-0 w-full h-1 bg-gradient-to-r from-transparent via-cyber-secondary to-transparent opacity-50"></div>

        <div class="relative z-10 w-full max-w-md p-6">
          <!-- Main Card -->
          <div class="relative bg-slate-900/80 backdrop-blur-xl rounded-2xl border border-white/10 shadow-2xl overflow-hidden">
            <button
              type="button"
              @click="toggleLocale"
              class="absolute top-4 right-4 z-10 px-3 py-1.5 rounded-lg border border-white/10 bg-white/5 text-xs font-semibold tracking-wide text-slate-200 hover:bg-white/10 transition-colors"
            >
              {{ t('app.languageToggle') }}
            </button>

            <!-- Top Gradient Line -->
            <div class="absolute top-0 left-0 w-full h-1 bg-gradient-to-r from-cyber-primary via-purple-500 to-cyber-primary"></div>

            <div class="p-8 pt-10">
              <div class="flex flex-col items-center mb-8">
                <div class="relative mb-6 group">
                  <div class="absolute inset-0 bg-cyber-primary rounded-full blur-xl opacity-10 group-hover:opacity-30 transition-opacity duration-500"></div>
                  <div class="relative p-4 bg-slate-950 rounded-full border border-white/10 group-hover:border-cyber-primary/50 transition-colors duration-300">
                    <Shield class="w-10 h-10 text-cyber-primary" />
                  </div>
                </div>
                <h1 class="text-3xl font-bold text-white mb-2 tracking-tight">{{ t('login.title') }}</h1>
                <p class="text-slate-400 font-mono text-xs tracking-widest uppercase">{{ t('login.subtitle') }}</p>
              </div>

              <form @submit.prevent="login" class="space-y-6">
                <div class="space-y-2">
                  <label class="text-xs uppercase tracking-wider text-slate-500 font-semibold ml-1">{{ t('login.username') }}</label>
                  <div class="relative group">
                    <User class="absolute left-4 top-3.5 w-5 h-5 text-slate-600 group-focus-within:text-cyber-primary transition-colors duration-300" />
                    <input
                      v-model="loginForm.username"
                      type="text"
                      autocomplete="username"
                      :placeholder="t('login.usernamePlaceholder')"
                      class="w-full pl-12 pr-4 py-3 bg-black/40 border border-white/10 rounded-xl focus:border-cyber-primary/50 focus:ring-1 focus:ring-cyber-primary/50 outline-none transition-all duration-300 text-white placeholder-slate-600 font-mono text-sm"
                    >
                  </div>
                </div>

                <div class="space-y-2">
                  <label class="text-xs uppercase tracking-wider text-slate-500 font-semibold ml-1">{{ t('login.password') }}</label>
                  <div class="relative group">
                    <KeyRound class="absolute left-4 top-3.5 w-5 h-5 text-slate-600 group-focus-within:text-cyber-primary transition-colors duration-300" />
                    <input
                      v-model="loginForm.password"
                      type="password"
                      autocomplete="current-password"
                      :placeholder="t('login.passwordPlaceholder')"
                      class="w-full pl-12 pr-4 py-3 bg-black/40 border border-white/10 rounded-xl focus:border-cyber-primary/50 focus:ring-1 focus:ring-cyber-primary/50 outline-none transition-all duration-300 text-white placeholder-slate-600 font-mono text-sm"
                    >
                  </div>
                </div>
                <button
                  type="submit"
                  class="w-full py-3.5 bg-cyber-primary text-black font-bold rounded-xl hover:bg-cyan-400 hover:shadow-lg hover:shadow-cyber-primary/20 transition-all duration-300 transform hover:-translate-y-0.5 active:translate-y-0 text-sm tracking-wide"
                >
                  {{ t('login.authenticate') }}
                </button>
              </form>
            </div>

            <!-- Bottom Status Bar -->
            <div class="bg-black/40 px-6 py-3 border-t border-white/5 flex justify-between items-center text-[10px] text-slate-600 font-mono uppercase tracking-wider">
              <span class="flex items-center gap-1.5">
                <span class="w-1.5 h-1.5 rounded-full bg-green-500"></span>
                {{ t('login.systemReady') }}
              </span>
              <span>v2.4.0-secure</span>
            </div>

          </div>
        </div>
      </div>
    </transition>

    <!-- Main App -->
    <transition name="fade">
      <div v-if="isAuthenticated" class="flex h-screen overflow-hidden bg-grid">

        <!-- Sidebar -->
        <aside :class="['glass-panel border-r border-white/5 transition-all duration-500 z-40 flex flex-col', sidebarOpen ? 'w-72' : 'w-20']">
          <div class="p-6 flex items-center justify-between border-b border-white/5">
            <div class="flex items-center gap-3 overflow-hidden whitespace-nowrap">
              <div class="p-2 bg-gradient-to-br from-cyber-primary to-blue-600 rounded-lg shrink-0">
                <Shield class="w-6 h-6 text-black" />
              </div>
              <span v-if="sidebarOpen" class="font-bold text-xl tracking-tight animate-fade-in">CodeScan</span>
            </div>
            <button @click="sidebarOpen = !sidebarOpen" class="p-1 hover:bg-white/10 rounded-lg transition-colors">
              <ChevronRight :class="['w-5 h-5 transition-transform duration-500', sidebarOpen ? 'rotate-180' : '']" />
            </button>
          </div>

          <nav class="flex-1 p-4 space-y-2 overflow-y-auto">
            <button
              @click="goDashboard()"
              :class="['w-full flex items-center gap-4 px-4 py-3 rounded-xl transition-all duration-300 group', currentView === 'dashboard' ? 'bg-cyber-primary/10 text-cyber-primary border border-cyber-primary/20 shadow-[0_0_15px_rgba(0,243,255,0.1)]' : 'hover:bg-white/5 text-slate-400 hover:text-white']"
            >
              <LayoutDashboard class="w-5 h-5 shrink-0" />
              <span v-if="sidebarOpen" class="font-medium animate-fade-in">{{ t('app.dashboard') }}</span>
              <div v-if="currentView === 'dashboard' && sidebarOpen" class="ml-auto w-1.5 h-1.5 bg-cyber-primary rounded-full animate-pulse"></div>
            </button>

            <button
              v-if="canManageUsers"
              @click="openAccounts()"
              :class="['w-full flex items-center gap-4 px-4 py-3 rounded-xl transition-all duration-300 group', currentView === 'accounts' ? 'bg-cyber-primary/10 text-cyber-primary border border-cyber-primary/20 shadow-[0_0_15px_rgba(0,243,255,0.1)]' : 'hover:bg-white/5 text-slate-400 hover:text-white']"
            >
              <UserCog class="w-5 h-5 shrink-0" />
              <span v-if="sidebarOpen" class="font-medium animate-fade-in">{{ t('app.accounts') }}</span>
              <div v-if="currentView === 'accounts' && sidebarOpen" class="ml-auto w-1.5 h-1.5 bg-cyber-primary rounded-full animate-pulse"></div>
            </button>

            <button
              v-if="canManageUsers"
              @click="openOrganizations()"
              :class="['w-full flex items-center gap-4 px-4 py-3 rounded-xl transition-all duration-300 group', currentView === 'organizations' ? 'bg-cyber-primary/10 text-cyber-primary border border-cyber-primary/20 shadow-[0_0_15px_rgba(0,243,255,0.1)]' : 'hover:bg-white/5 text-slate-400 hover:text-white']"
            >
              <Building2 class="w-5 h-5 shrink-0" />
              <span v-if="sidebarOpen" class="font-medium animate-fade-in">{{ t('organizations.title') }}</span>
              <div v-if="currentView === 'organizations' && sidebarOpen" class="ml-auto w-1.5 h-1.5 bg-cyber-primary rounded-full animate-pulse"></div>
            </button>

            <div class="pt-6 pb-2" v-if="sidebarOpen">
              <p class="px-4 text-xs font-bold text-slate-500 uppercase tracking-widest animate-fade-in">{{ t('app.projects') }}</p>
            </div>

            <div v-if="tasks.length > 0" class="space-y-1">
              <button
                v-for="task in tasks.slice(0, 5)"
                :key="task.id"
                @click="openTask(task)"
                :class="['w-full flex items-center gap-4 px-4 py-2.5 rounded-xl transition-all duration-300 group', selectedTaskId === task.id ? 'bg-white/10 text-white' : 'text-slate-400 hover:bg-white/5 hover:text-white']"
              >
                <FolderOpen class="w-5 h-5 shrink-0 group-hover:text-cyber-secondary transition-colors" />
                <span v-if="sidebarOpen" class="truncate text-sm animate-fade-in">{{ task.name }}</span>
              </button>
            </div>
          </nav>

          <div class="p-4 border-t border-white/5">
            <button @click="logout" class="w-full flex items-center gap-4 px-4 py-3 text-red-400 hover:bg-red-500/10 hover:text-red-300 rounded-xl transition-all duration-300">
              <LogOut class="w-5 h-5 shrink-0" />
              <span v-if="sidebarOpen" class="font-medium animate-fade-in">{{ t('app.logout') }}</span>
            </button>
          </div>
        </aside>

        <!-- Main Content -->
        <main class="flex-1 overflow-hidden relative flex flex-col">
          <!-- Top Bar -->
          <header class="h-20 glass-panel border-b border-white/5 flex items-center justify-between px-8 z-30">
            <div>
              <h2 class="text-2xl font-bold bg-gradient-to-r from-white to-slate-400 bg-clip-text text-transparent">
                {{ currentTaskName }}
              </h2>
              <p class="text-slate-500 text-sm flex items-center gap-2">
                <span class="w-2 h-2 rounded-full bg-green-500 animate-pulse"></span>
                {{ t('app.systemOnline') }}
              </p>
            </div>

            <div class="flex items-center gap-3">
              <button
                type="button"
                @click="toggleLocale"
                class="px-3 py-2 rounded-lg border border-white/10 bg-white/5 text-sm font-semibold text-slate-200 hover:bg-white/10 transition-colors"
              >
                {{ t('app.languageToggle') }}
              </button>
              <button
                type="button"
                :title="t('password.changePassword')"
                :aria-label="t('password.changePassword')"
                class="inline-flex h-10 w-10 items-center justify-center rounded-lg border border-white/10 bg-white/5 text-slate-200 transition-colors hover:bg-white/10 hover:text-cyan-200"
                @click="openPasswordModal"
              >
                <KeyRound class="w-5 h-5" />
              </button>
              <button
                v-if="canCreateProject"
                @click="openUploadModal"
                class="flex items-center gap-2 px-6 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/50 rounded-lg font-semibold shadow-[0_0_15px_rgba(0,243,255,0.1)] hover:shadow-[0_0_25px_rgba(0,243,255,0.2)] transition-all transform hover:-translate-y-0.5"
              >
                <Upload class="w-5 h-5" />
                <span class="hidden sm:inline">{{ t('app.newProject') }}</span>
              </button>
            </div>
          </header>

          <!-- Scrollable Area -->
          <div class="flex-1 overflow-y-auto p-8 relative scroll-smooth">

            <!-- Dashboard View -->
            <DashboardOverview
              v-if="currentView === 'dashboard'"
              :stats="stats"
              :display-stats="displayStats"
              :tasks="tasks"
              :stage-definitions="stageDefinitions"
              :loading="isLoading"
              :selected-task-id="selectedTaskId"
              :locale="locale"
              :t="t"
              :can-delete="canDelete"
              @refresh="fetchData"
              @open-task="openTask"
              @delete-task="deleteTask"
            />

            <AccountManagement
              v-if="currentView === 'accounts' && canManageUsers"
              :api-url="API_URL"
              :auth-token="authKey"
              :t="t"
              :organizations="flatOrganizations"
              @auth-expired="logout"
            />

            <OrganizationManagement
              v-if="currentView === 'organizations' && canManageUsers"
              :api-url="API_URL"
              :auth-token="authKey"
              :t="t"
              @auth-expired="logout"
              @updated="handleOrganizationsUpdated"
            />

            <!-- Task Detail View -->
            <div v-if="currentView === 'task-detail' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <!-- Header -->
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="goDashboard()" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> {{ t('app.dashboard') }}
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-cyber-primary text-sm font-mono">{{ selectedTask.id.substring(0,8) }}...</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">{{ selectedTask.name }}</h1>
                  <p class="text-slate-400 mt-1">{{ selectedTask.remark }}</p>
                  <div class="mt-3 inline-flex items-center gap-2 rounded-full border border-white/10 bg-white/5 px-3 py-1 text-xs font-semibold text-slate-300">
                    <Building2 class="w-3.5 h-3.5 text-cyan-300" />
                    {{ taskOrganizationName(selectedTask) }}
                  </div>
                  <div class="mt-5 flex flex-wrap gap-2">
                    <button
                      v-for="tab in taskPrimaryTabs"
                      :key="tab.key"
                      @click="currentView = tab.key"
                      :class="[
                        'px-4 py-2 rounded-xl border text-sm font-semibold transition-colors flex items-center gap-2',
                        currentView === tab.key
                          ? 'bg-cyber-primary/12 text-cyber-primary border-cyber-primary/30'
                          : 'bg-white/5 text-slate-300 border-white/10 hover:bg-white/10 hover:text-white'
                      ]"
                    >
                      <span>{{ tab.label }}</span>
                      <span v-if="tab.badge !== undefined" class="px-2 py-0.5 rounded-full bg-black/20 text-xs text-slate-300 border border-white/10">
                        {{ tab.badge }}
                      </span>
                    </button>
                  </div>
                </div>

                <div class="flex flex-wrap gap-3">
                  <button
                    v-if="canRequestTaskStart(selectedTask)"
                    @click="taskAction(selectedTask.id, 'start')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" />
                    {{ taskStartActionLabel(selectedTask) }}
                  </button>
                  <button
                    v-if="selectedTaskCanWrite && selectedTask.status === 'running'"
                    @click="taskAction(selectedTask.id, 'pause')"
                    class="px-5 py-2.5 bg-yellow-500/20 text-yellow-400 border border-yellow-500/50 hover:bg-yellow-500/30 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Pause class="w-4 h-4" /> {{ t('common.pause') }}
                  </button>
                  <button
                    v-if="selectedTaskCanWrite && selectedTask.status === 'paused'"
                    @click="taskAction(selectedTask.id, 'resume')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" /> {{ t('common.resume') }}
                  </button>
                  <button
                    v-if="canDelete"
                    @click="deleteTask(selectedTask.id)"
                    class="px-5 py-2.5 bg-red-500/10 text-red-400 border border-red-500/30 hover:bg-red-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Trash2 class="w-4 h-4" /> {{ t('common.delete') }}
                  </button>
                </div>
              </div>

              <TaskStageStrip
                :task="selectedTask"
                :stage-definitions="stageDefinitions"
                :current-view="currentView"
                :t="t"
                @select-stage="currentView = $event"
              />

              <div class="glass-panel rounded-2xl p-6 border border-white/10">
                <div class="flex flex-col xl:flex-row xl:items-start xl:justify-between gap-5">
                  <div>
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('taskDetail.orchestrationOverview') }}</div>
                    <h2 class="text-2xl font-bold text-white mt-2">{{ t('taskDetail.tabs.orchestration') }}</h2>
                    <p class="text-slate-400 mt-2 max-w-3xl">
                      {{ taskOrchestrationPreview.hasRun ? t('taskDetail.orchestrationOverviewDesc') : t('taskDetail.orchestrationNoRunDesc') }}
                    </p>
                  </div>
                  <div class="flex flex-wrap gap-3">
                    <button
                      @click="currentView = 'task-orchestration'"
                      class="px-5 py-2.5 rounded-xl bg-white/5 hover:bg-white/10 border border-white/10 text-slate-100 font-semibold transition-colors"
                    >
                      {{ t('taskDetail.openWorkbench') }}
                    </button>
                    <button
                      v-if="!taskOrchestrationPreview.hasRun && canRequestTaskStart(selectedTask)"
                      @click="taskAction(selectedTask.id, 'start')"
                      class="glass-button px-5 py-2.5 rounded-xl flex items-center gap-2"
                    >
                      <Play class="w-4 h-4" />
                      {{ taskStartActionLabel(selectedTask) }}
                    </button>
                  </div>
                </div>

                <div v-if="taskOrchestrationPreview.hasRun" class="grid md:grid-cols-2 xl:grid-cols-4 gap-4 mt-6">
                  <div class="rounded-2xl bg-white/5 border border-white/10 px-4 py-4">
                    <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('taskDetail.orchestrationStatus') }}</div>
                    <div class="mt-2 text-lg font-semibold text-white">{{ taskOrchestrationPreview.focusStatusLabel }}</div>
                  </div>
                  <div class="rounded-2xl bg-white/5 border border-white/10 px-4 py-4">
                    <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('taskDetail.orchestrationCurrentStage') }}</div>
                    <div class="mt-2 text-lg font-semibold text-white">{{ taskOrchestrationPreview.currentStageLabel }}</div>
                  </div>
                  <div class="rounded-2xl bg-white/5 border border-white/10 px-4 py-4">
                    <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('taskDetail.orchestrationActiveSubtasks') }}</div>
                    <div class="mt-2 text-lg font-semibold text-white">{{ taskOrchestrationPreview.activeSubtaskCount }}</div>
                  </div>
                  <div class="rounded-2xl bg-white/5 border border-white/10 px-4 py-4">
                    <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('taskDetail.orchestrationLastProgress') }}</div>
                    <div class="mt-2 text-lg font-semibold text-white">{{ taskOrchestrationPreview.lastProgressLabel }}</div>
                  </div>
                </div>

                <div v-if="taskOrchestrationPreview.hasRun && taskOrchestrationPreview.latestEventMessage" class="mt-4 rounded-2xl border border-white/10 bg-black/20 px-4 py-4">
                  <div class="text-xs uppercase tracking-[0.18em] text-slate-500">{{ t('taskDetail.orchestrationLatestEvent') }}</div>
                  <div class="mt-2 text-white font-semibold">{{ taskOrchestrationPreview.latestEventMessage }}</div>
                  <div v-if="taskOrchestrationPreview.latestEventAtLabel" class="mt-1 text-sm text-slate-400">
                    {{ taskOrchestrationPreview.latestEventAtLabel }}
                  </div>
                </div>
              </div>

              <!-- Terminal/Output Area -->
              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-cyber-primary/20 shadow-[0_0_30px_rgba(0,0,0,0.3)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      {{ t('common.console') }}
                    </button>
                    <button
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      {{ t('common.results') }}
                    </button>
                  </div>
                  <div class="flex gap-1.5">
                    <div class="w-3 h-3 rounded-full bg-red-500/50"></div>
                    <div class="w-3 h-3 rounded-full bg-yellow-500/50"></div>
                    <div class="w-3 h-3 rounded-full bg-green-500/50"></div>
                  </div>
                </div>

                <!-- Console View -->
                <div v-if="activeTab === 'console'" class="flex-1 bg-slate-950 p-6 overflow-auto font-mono text-sm relative group" ref="consoleContainer">
                  <div class="absolute inset-0 pointer-events-none bg-scan-lines opacity-5"></div>

                  <div v-if="currentLogs && currentLogs.length > 0" class="space-y-1">
                    <div v-for="(log, i) in currentLogs" :key="i" class="text-slate-400 break-all hover:bg-white/5 px-1 rounded flex gap-3 animate-fade-in">
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
                    <div class="relative mb-6">
                      <div class="absolute inset-0 bg-cyber-primary/20 blur-xl rounded-full animate-pulse"></div>
                      <Server class="w-16 h-16 relative z-10 animate-float" />
                    </div>
                    <p class="text-lg">{{ t('taskDetail.waitingForExecution') }}</p>
                  </div>
                </div>

                <!-- Results View -->
                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div class="mb-4 flex flex-wrap items-center gap-2 text-xs">
                    <span class="text-slate-500 uppercase tracking-[0.18em] font-semibold">{{ t('taskDetail.routeInventory') }}</span>
                    <span class="px-2.5 py-1 rounded-full bg-cyber-primary/10 text-cyber-primary border border-cyber-primary/25 font-mono">{{ t('taskDetail.routesCount', { count: countRouteInventory(selectedTask) }) }}</span>
                    <span v-if="isTaskLoading" class="px-2.5 py-1 rounded-full bg-white/5 text-slate-300 border border-white/10">{{ t('taskDetail.refreshingTaskDetail') }}</span>
                  </div>

                  <!-- Action Bar -->
                  <div class="mb-4 flex justify-between items-center">
                     <h3 class="text-lg font-bold text-white">{{ t('taskDetail.analysisResultsRoutes') }}</h3>
                     <div class="flex items-center gap-3">
                       <button
                        v-if="selectedTaskCanWrite"
                        @click="runGapCheck(selectedTask.id, 'init')"
                        :disabled="!canGapCheckStage(selectedTask, 'init') || isStageActionPending('init', 'gap-check')"
                        class="px-4 py-2 bg-white/5 hover:bg-white/10 text-slate-100 border border-white/10 rounded-lg font-bold text-sm flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                       >
                        <RefreshCw :class="['w-4 h-4', isStageActionPending('init', 'gap-check') ? 'animate-spin' : '']" />
                        {{ isStageActionPending('init', 'gap-check') ? t('auditView.gapChecking') : t('taskDetail.gapCheckRoutes') }}
                       </button>
                     </div>
                  </div>

                  <!-- Routes Table -->
                  <div v-if="parsedResult" class="overflow-x-auto">
                    <table class="w-full text-left border-collapse">
                      <thead>
                        <tr class="border-b border-white/10 text-slate-400 text-xs uppercase tracking-wider">
                          <th class="p-3 font-semibold">{{ t('common.method') }}</th>
                          <th class="p-3 font-semibold">{{ t('common.path') }}</th>
                          <th class="p-3 font-semibold">{{ t('common.sourceFile') }}</th>
                          <th class="p-3 font-semibold">{{ t('common.description') }}</th>
                        </tr>
                      </thead>
                      <tbody class="divide-y divide-white/5 text-sm">
                        <tr v-for="(item, idx) in parsedResult" :key="idx" class="hover:bg-white/5 transition-colors">
                          <td class="p-3">
                            <span :class="['px-2 py-1 rounded text-xs font-bold',
                              item.method === 'GET' ? 'bg-blue-500/20 text-blue-400' :
                              item.method === 'POST' ? 'bg-green-500/20 text-green-400' :
                              item.method === 'DELETE' ? 'bg-red-500/20 text-red-400' :
                              item.method === 'PUT' ? 'bg-yellow-500/20 text-yellow-400' :
                              'bg-slate-500/20 text-slate-400']">
                              {{ item.method }}
                            </span>
                          </td>
                          <td class="p-3 font-mono text-white">{{ item.path }}</td>
                          <td class="p-3 text-slate-400 font-mono text-xs">{{ item.source }}</td>
                          <td class="p-3 text-slate-300">{{ item.description }}</td>
                        </tr>
                      </tbody>
                    </table>
                  </div>

                  <!-- Fallback Text View if not JSON -->
                  <div v-else-if="selectedTask.result" class="font-mono text-sm text-slate-300 whitespace-pre-wrap">
                    {{ selectedTask.result }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button
                        v-if="selectedTaskCanWrite"
                        @click="repairJSON(selectedTask.id, 'init')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? t('common.repairingJson') : t('common.repairJsonFormat') }}
                      </button>
                    </div>
                  </div>

                  <!-- Empty State -->
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>{{ t('taskDetail.noResultsYet') }}</p>
                  </div>
                </div>
              </div>
            </div>

            <div v-if="currentView === 'task-orchestration' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col xl:flex-row justify-between items-start xl:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="goDashboard()" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> {{ t('app.dashboard') }}
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-cyber-primary text-sm font-mono">{{ selectedTask.id.substring(0, 8) }}...</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">{{ selectedTask.name }}</h1>
                  <p class="text-slate-400 mt-1">{{ selectedTask.remark }}</p>
                  <div class="mt-3 inline-flex items-center gap-2 rounded-full border border-white/10 bg-white/5 px-3 py-1 text-xs font-semibold text-slate-300">
                    <Building2 class="w-3.5 h-3.5 text-cyan-300" />
                    {{ taskOrganizationName(selectedTask) }}
                  </div>
                  <div class="mt-5 flex flex-wrap gap-2">
                    <button
                      v-for="tab in taskPrimaryTabs"
                      :key="tab.key"
                      @click="currentView = tab.key"
                      :class="[
                        'px-4 py-2 rounded-xl border text-sm font-semibold transition-colors flex items-center gap-2',
                        currentView === tab.key
                          ? 'bg-cyber-primary/12 text-cyber-primary border-cyber-primary/30'
                          : 'bg-white/5 text-slate-300 border-white/10 hover:bg-white/10 hover:text-white'
                      ]"
                    >
                      <span>{{ tab.label }}</span>
                      <span v-if="tab.badge !== undefined" class="px-2 py-0.5 rounded-full bg-black/20 text-xs text-slate-300 border border-white/10">
                        {{ tab.badge }}
                      </span>
                    </button>
                  </div>
                </div>

                <div class="flex flex-wrap gap-3">
                  <button
                    v-if="canRequestTaskStart(selectedTask)"
                    @click="taskAction(selectedTask.id, 'start')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" />
                    {{ taskStartActionLabel(selectedTask) }}
                  </button>
                  <button
                    v-if="selectedTaskCanWrite && selectedTask.status === 'running'"
                    @click="taskAction(selectedTask.id, 'pause')"
                    class="px-5 py-2.5 bg-yellow-500/20 text-yellow-400 border border-yellow-500/50 hover:bg-yellow-500/30 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Pause class="w-4 h-4" /> {{ t('common.pause') }}
                  </button>
                  <button
                    v-if="selectedTaskCanWrite && selectedTask.status === 'paused'"
                    @click="taskAction(selectedTask.id, 'resume')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" /> {{ t('common.resume') }}
                  </button>
                  <button
                    v-if="canDelete"
                    @click="deleteTask(selectedTask.id)"
                    class="px-5 py-2.5 bg-red-500/10 text-red-400 border border-red-500/30 hover:bg-red-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Trash2 class="w-4 h-4" /> {{ t('common.delete') }}
                  </button>
                </div>
              </div>

              <TaskOrchestrationWorkbench
                :task-id="selectedTask.id"
                :task="selectedTask"
                :api-url="API_URL"
                :auth-token="authKey"
                :locale="locale"
                :t="t"
                :start-pending="isStartFlowPending"
                :stage-issue-summaries="stageErrorSummaries"
                :can-write="selectedTaskCanWrite"
                @refresh-task="fetchTaskDetail(selectedTask.id, { silent: true })"
                @request-start="requestTaskStart(selectedTask)"
                @open-stage-console="openStageConsole"
              />
            </div>

            <!-- Task Report View -->
            <div v-if="currentView === 'task-report' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col xl:flex-row justify-between items-start xl:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="goDashboard()" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> {{ t('app.dashboard') }}
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-cyber-primary text-sm font-mono">{{ selectedTask.id.substring(0, 8) }}...</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">{{ t('reportView.title') }}</h1>
                  <p class="text-slate-400 mt-1">{{ t('reportView.subtitle') }}</p>
                  <div class="mt-3 inline-flex items-center gap-2 rounded-full border border-white/10 bg-white/5 px-3 py-1 text-xs font-semibold text-slate-300">
                    <Building2 class="w-3.5 h-3.5 text-cyan-300" />
                    {{ selectedTask.name }} / {{ taskOrganizationName(selectedTask) }}
                  </div>
                  <div class="mt-5 flex flex-wrap gap-2">
                    <button
                      v-for="tab in taskPrimaryTabs"
                      :key="tab.key"
                      @click="currentView = tab.key"
                      :class="[
                        'px-4 py-2 rounded-xl border text-sm font-semibold transition-colors flex items-center gap-2',
                        currentView === tab.key
                          ? 'bg-cyber-primary/12 text-cyber-primary border-cyber-primary/30'
                          : 'bg-white/5 text-slate-300 border-white/10 hover:bg-white/10 hover:text-white'
                      ]"
                    >
                      <span>{{ tab.label }}</span>
                      <span v-if="tab.badge !== undefined" class="px-2 py-0.5 rounded-full bg-black/20 text-xs text-slate-300 border border-white/10">
                        {{ tab.badge }}
                      </span>
                    </button>
                  </div>
                </div>

                <div class="flex flex-wrap gap-3 w-full xl:w-auto">
                  <div class="px-4 py-3 rounded-xl bg-white/5 border border-white/10 min-w-[140px]">
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.detectedAudits') }}</div>
                    <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.stageCount }}</div>
                  </div>
                  <div class="px-4 py-3 rounded-xl bg-white/5 border border-white/10 min-w-[140px]">
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.confirmedFindings') }}</div>
                    <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.totalFindings }}</div>
                  </div>
                  <button
                    @click="downloadTaskReport(selectedTask.id)"
                    :disabled="isDownloadingReport || reportStages.length === 0"
                    class="px-5 py-3 bg-gradient-to-r from-cyan-300 to-blue-500 hover:from-cyan-200 hover:to-blue-400 text-black font-bold rounded-xl shadow-[0_0_25px_rgba(56,189,248,0.25)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <Download :class="['w-4 h-4', isDownloadingReport ? 'animate-bounce' : '']" />
                    {{ isDownloadingReport ? t('reportView.generatingHtml') : t('reportView.downloadHtmlReport') }}
                  </button>
                </div>
              </div>

              <div class="grid xl:grid-cols-[1.5fr_0.8fr] gap-6">
                <div class="space-y-5">
                  <div v-if="reportStages.length > 0" class="space-y-5">
                    <div
                      v-for="stage in reportStages"
                      :key="stage.key"
                      :class="['glass-panel rounded-2xl overflow-hidden border', stage.cardClass]"
                    >
                      <div :class="['p-5 border-b border-white/5 bg-gradient-to-br', stage.gradientClass]">
                        <div class="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-4">
                          <div class="flex items-start gap-4">
                            <div :class="['w-11 h-11 rounded-xl border flex items-center justify-center', stage.iconClass]">
                              <component :is="stage.icon" class="w-5 h-5" />
                            </div>
                            <div>
                              <div class="flex items-center gap-2 flex-wrap">
                                <h2 class="text-xl font-bold text-white">{{ stage.label }}</h2>
                                <span class="px-2.5 py-1 rounded-full bg-white/10 border border-white/10 text-xs font-bold text-slate-200">{{ t('reportView.includedInExport') }}</span>
                                <span v-if="stage.rawOnly" class="px-2.5 py-1 rounded-full bg-amber-500/15 border border-amber-500/30 text-xs font-bold text-amber-300">{{ t('reportView.rawOutputFallback') }}</span>
                              </div>
                              <p class="text-slate-400 mt-2">{{ stage.description }}</p>
                            </div>
                          </div>
                          <div class="text-sm text-slate-400 lg:text-right">
                            <div class="uppercase tracking-[0.2em] text-[11px] text-slate-500">{{ t('common.completedAt') }}</div>
                            <div class="mt-1">{{ stage.completedAt || displayStatus('completed') }}</div>
                          </div>
                        </div>

                        <div class="grid md:grid-cols-3 gap-3 mt-5">
                          <div class="rounded-xl bg-black/20 border border-white/10 px-4 py-3">
                            <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('common.findings') }}</div>
                            <div class="text-2xl font-bold text-white mt-2">{{ stage.findingCount }}</div>
                          </div>
                          <div class="rounded-xl bg-black/20 border border-white/10 px-4 py-3">
                            <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('common.files') }}</div>
                            <div class="text-2xl font-bold text-white mt-2">{{ stage.uniqueFiles }}</div>
                          </div>
                          <div class="rounded-xl bg-black/20 border border-white/10 px-4 py-3">
                            <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('common.interfaces') }}</div>
                            <div class="text-2xl font-bold text-white mt-2">{{ stage.uniqueInterfaces }}</div>
                          </div>
                        </div>
                      </div>

                      <div class="p-5 space-y-4">
                        <p class="text-sm text-slate-300">{{ stage.summaryText }}</p>

                        <div v-if="stage.severityBreakdown.length > 0" class="flex flex-wrap gap-2">
                          <span
                            v-for="severity in stage.severityBreakdown"
                            :key="severity.label"
                            :class="['px-2.5 py-1 rounded-full text-xs font-bold', severityBadgeClass(severity.label)]"
                          >
                            {{ severity.label }} / {{ severity.count }}
                          </span>
                        </div>

                        <div v-if="stage.rawOnly" class="rounded-xl border border-amber-500/20 bg-amber-500/10 px-4 py-4 text-sm text-amber-100">{{ t('reportView.rawStageNote') }}</div>
                        <div v-else-if="stage.allRejected" class="rounded-xl border border-rose-500/20 bg-rose-500/10 px-4 py-4 text-sm text-rose-200">{{ t('auditView.allRejected') }}</div>
                        <div v-else-if="stage.clean" class="rounded-xl border border-emerald-500/20 bg-emerald-500/10 px-4 py-4 text-sm text-emerald-200">{{ t('reportView.cleanStageNote') }}</div>
                        <div v-else class="space-y-3">
                          <div
                            v-for="(finding, idx) in stage.results.slice(0, 3)"
                            :key="`${stage.key}-${idx}`"
                            class="rounded-xl border border-white/10 bg-black/20 px-4 py-4"
                          >
                            <div class="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-3">
                              <div>
                                <div class="flex items-center gap-2 flex-wrap">
                                  <span :class="['px-2.5 py-1 rounded-full text-xs font-bold', severityBadgeClass(effectiveSeverity(finding))]">{{ effectiveSeverity(finding) }}</span>
                                  <span class="text-white font-semibold">{{ finding.subtype || stage.shortLabel }}</span>
                                </div>
                                <p class="text-slate-300 mt-3">{{ finding.description || t('common.noDescription') }}</p>
                              </div>
                              <div class="text-sm text-slate-400 lg:text-right">{{ finding.location?.file ? (finding.location?.line ? `${finding.location.file}:${finding.location.line}` : finding.location.file) : t('auditView.locationNotProvided') }}</div>
                            </div>
                            <div class="mt-3 text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.trigger') }}</div>
                            <div class="mt-1 text-sm text-cyan-300 font-mono break-all">{{ formatTriggerLabel(finding.trigger) }}</div>
                          </div>

                          <div v-if="stage.findingCount > 3" class="text-xs text-slate-500">
                            {{ t('reportView.moreFindings', { count: stage.findingCount - 3 }) }}
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>

                  <div v-else class="glass-panel rounded-2xl p-10 border border-white/10 text-center">
                    <Download class="w-12 h-12 mx-auto text-slate-500 mb-4" />
                    <h2 class="text-xl font-bold text-white">{{ t('reportView.noExportable') }}</h2>
                    <p class="text-slate-400 mt-2">{{ t('reportView.noExportableDesc') }}</p>
                  </div>
                </div>

                <div class="space-y-5">
                  <div class="glass-panel rounded-2xl p-5 border border-white/10">
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.reportScope') }}</div>
                    <h2 class="text-xl font-bold text-white mt-2">{{ t('reportView.whatWillBeExported') }}</h2>
                    <p class="text-slate-400 mt-2">{{ t('reportView.reportScopeDesc') }}</p>

                    <div class="grid grid-cols-2 gap-3 mt-5">
                      <div class="rounded-xl bg-white/5 border border-white/10 px-4 py-3">
                        <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('common.routes') }}</div>
                        <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.routeCount }}</div>
                      </div>
                      <div class="rounded-xl bg-white/5 border border-white/10 px-4 py-3">
                        <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('common.files') }}</div>
                        <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.uniqueFiles }}</div>
                      </div>
                      <div class="rounded-xl bg-white/5 border border-white/10 px-4 py-3">
                        <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.endpoints') }}</div>
                        <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.uniqueInterfaces }}</div>
                      </div>
                      <div class="rounded-xl bg-white/5 border border-white/10 px-4 py-3">
                        <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.cleanAudits') }}</div>
                        <div class="text-2xl font-bold text-white mt-2">{{ reportOverview.cleanStageCount }}</div>
                      </div>
                    </div>
                  </div>

                  <div class="glass-panel rounded-2xl p-5 border border-white/10">
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.detectedModules') }}</div>
                    <div class="mt-4 space-y-3">
                      <div
                        v-for="stage in reportStages"
                        :key="`summary-${stage.key}`"
                        class="flex items-center justify-between gap-3 rounded-xl bg-white/5 border border-white/10 px-4 py-3"
                      >
                        <div class="flex items-center gap-3 min-w-0">
                          <div :class="['w-9 h-9 rounded-lg border flex items-center justify-center shrink-0', stage.iconClass]">
                            <component :is="stage.icon" class="w-4 h-4" />
                          </div>
                          <div class="min-w-0">
                            <div class="text-sm font-semibold text-white truncate">{{ stage.label }}</div>
                            <div class="text-xs text-slate-400 truncate">{{ stage.rawOnly ? t('reportView.rawOutputFallback') : stage.summaryText }}</div>
                          </div>
                        </div>
                        <div class="text-right">
                          <div class="text-lg font-bold text-white">{{ stage.findingCount }}</div>
                          <div class="text-[11px] uppercase tracking-[0.2em] text-slate-500">{{ t('common.findings') }}</div>
                        </div>
                      </div>
                    </div>
                  </div>

                  <div v-if="reportOverview.severityBreakdown.length > 0" class="glass-panel rounded-2xl p-5 border border-white/10">
                    <div class="text-xs uppercase tracking-[0.2em] text-slate-500">{{ t('reportView.severityMix') }}</div>
                    <div class="flex flex-wrap gap-2 mt-4">
                      <span
                        v-for="severity in reportOverview.severityBreakdown"
                        :key="`overall-${severity.label}`"
                        :class="['px-2.5 py-1 rounded-full text-xs font-bold', severityBadgeClass(severity.label)]"
                      >
                        {{ severity.label }} / {{ severity.count }}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <AuditStageView
              v-if="selectedTask && auditViews[currentView] && currentAuditDefinition"
              :task="selectedTask"
              :stage-definition="currentAuditDefinition"
              :logs="currentLogs"
              :results="parsedResult"
              :raw-result="currentRawResult"
              :stage-meta="currentAuditStage?.meta || {}"
              :is-repairing="isRepairing"
              :active-tab="activeTab"
              :locale="locale"
              :t="t"
              :task-running="selectedTask.status === 'running'"
              :gap-check-pending="isStageActionPending(currentAuditDefinition.key, 'gap-check')"
              :revalidate-pending="isStageActionPending(currentAuditDefinition.key, 'revalidate')"
              :can-gap-check="canGapCheckStage(selectedTask, currentAuditDefinition.key)"
              :can-revalidate="canRevalidateStage(selectedTask, currentAuditDefinition.key)"
              :can-write="selectedTaskCanWrite"
              @back="currentView = 'task-detail'"
              @update:activeTab="activeTab = $event"
              @run="runStage(selectedTask.id, currentAuditDefinition.key)"
              @gap-check="runGapCheck(selectedTask.id, currentAuditDefinition.key)"
              @revalidate="revalidateStage(selectedTask.id, currentAuditDefinition.key)"
              @repair="repairJSON(selectedTask.id, currentAuditDefinition.key)"
            />

          </div>
        </main>

        <transition name="fade">
          <div v-if="rerunModalOpen && selectedTask && selectedTaskCanWrite" class="fixed inset-0 z-50 flex items-center justify-center p-4">
            <div class="absolute inset-0 bg-black/80 backdrop-blur-sm" @click="closeRerunModal"></div>

            <div class="relative z-10 w-full max-w-2xl glass-panel rounded-2xl p-8 border-t border-cyber-primary/30 shadow-[0_0_50px_rgba(0,0,0,0.5)] animate-slide-up">
              <button @click="closeRerunModal" class="absolute top-4 right-4 text-slate-400 hover:text-white transition-colors">
                <XCircle class="w-6 h-6" />
              </button>

              <div class="mb-6">
                <h2 class="text-2xl font-bold text-white">
                  {{ t('taskDetail.rerunTitle') }}
                </h2>
                <p class="text-slate-400 mt-2">
                  {{ t('taskDetail.rerunDescription') }}
                </p>
              </div>

              <div class="flex flex-wrap gap-2 mb-5">
                <button
                  type="button"
                  class="px-3 py-1.5 rounded-full border border-white/10 bg-white/5 text-sm text-slate-200 hover:bg-white/10 transition-colors"
                  @click="selectFailedRerunStages"
                >
                  {{ t('taskDetail.rerunFailedStages') }}
                </button>
                <button
                  type="button"
                  class="px-3 py-1.5 rounded-full border border-white/10 bg-white/5 text-sm text-slate-200 hover:bg-white/10 transition-colors"
                  @click="selectAllAuditRerunStages"
                >
                  {{ t('taskDetail.rerunAllStages') }}
                </button>
                <button
                  type="button"
                  class="px-3 py-1.5 rounded-full border border-white/10 bg-white/5 text-sm text-slate-200 hover:bg-white/10 transition-colors"
                  @click="clearRerunStages"
                >
                  {{ t('taskDetail.rerunClear') }}
                </button>
              </div>

              <div class="space-y-3 max-h-[420px] overflow-auto pr-1">
                <label
                  v-for="option in rerunStageOptions"
                  :key="option.key"
                  class="flex items-start gap-4 rounded-2xl border border-white/10 bg-black/20 px-4 py-4 hover:bg-white/5 transition-colors cursor-pointer"
                >
                  <input
                    v-model="rerunSelection"
                    type="checkbox"
                    :value="option.key"
                    class="mt-1 h-4 w-4 rounded border-white/20 bg-slate-950 text-cyan-400 focus:ring-cyan-400"
                  >
                  <div class="min-w-0 flex-1">
                    <div class="flex items-center justify-between gap-3 flex-wrap">
                      <div class="text-white font-semibold">{{ option.label }}</div>
                      <span :class="['inline-flex items-center gap-2 px-3 py-1 rounded-full text-xs font-bold uppercase', statusBadgeClass(option.status)]">
                        {{ displayStatus(option.status) }}
                      </span>
                    </div>
                    <div class="mt-2 text-sm text-slate-400">{{ option.detail }}</div>
                    <div v-if="option.updatedAt" class="mt-1 text-xs text-slate-500">{{ option.updatedAt }}</div>
                  </div>
                </label>
              </div>

              <div v-if="rerunModalError" class="mt-5 rounded-xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
                {{ rerunModalError }}
              </div>

              <div class="mt-6 flex flex-wrap justify-end gap-3">
                <button
                  type="button"
                  class="px-4 py-2.5 rounded-xl border border-white/10 bg-white/5 text-slate-200 hover:bg-white/10 transition-colors"
                  @click="closeRerunModal"
                >
                  {{ t('taskDetail.rerunCancel') }}
                </button>
                <button
                  type="button"
                  class="px-5 py-2.5 rounded-xl bg-cyber-primary text-black font-bold disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="isStartFlowPending || rerunSelection.length === 0"
                  @click="submitRerunSelection"
                >
                  {{ isStartFlowPending ? t('taskDetail.rerunStarting') : t('taskDetail.rerunStart') }}
                </button>
              </div>
            </div>
          </div>
        </transition>

        <!-- Password Modal -->
        <transition name="fade">
          <div v-if="showPasswordModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
            <div class="absolute inset-0 bg-black/80 backdrop-blur-sm" @click="closePasswordModal"></div>

            <form
              class="relative z-10 w-full max-w-md glass-panel rounded-2xl p-7 border-t border-cyan-500/30 shadow-[0_0_50px_rgba(0,0,0,0.5)] animate-slide-up"
              @submit.prevent="submitPasswordChange"
            >
              <button
                type="button"
                class="absolute top-4 right-4 text-slate-400 transition-colors hover:text-white disabled:cursor-not-allowed disabled:opacity-50"
                :disabled="isChangingPassword"
                @click="closePasswordModal"
              >
                <XCircle class="w-6 h-6" />
              </button>

              <div class="mb-6">
                <div class="mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-cyan-500/10 text-cyan-300">
                  <KeyRound class="w-6 h-6" />
                </div>
                <h2 class="text-2xl font-bold text-white">{{ t('password.title') }}</h2>
                <p class="mt-2 text-sm text-slate-400">{{ t('password.subtitle') }}</p>
              </div>

              <div class="space-y-4">
                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('password.currentPassword') }}</label>
                  <input
                    v-model="passwordForm.currentPassword"
                    type="password"
                    autocomplete="current-password"
                    class="w-full rounded-xl border border-slate-600 bg-slate-900/50 px-4 py-3 text-white outline-none transition-all placeholder-slate-600 focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary"
                  >
                </div>

                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('password.newPassword') }}</label>
                  <input
                    v-model="passwordForm.newPassword"
                    type="password"
                    autocomplete="new-password"
                    class="w-full rounded-xl border border-slate-600 bg-slate-900/50 px-4 py-3 text-white outline-none transition-all placeholder-slate-600 focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary"
                  >
                </div>

                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('password.confirmPassword') }}</label>
                  <input
                    v-model="passwordForm.confirmPassword"
                    type="password"
                    autocomplete="new-password"
                    class="w-full rounded-xl border border-slate-600 bg-slate-900/50 px-4 py-3 text-white outline-none transition-all placeholder-slate-600 focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary"
                  >
                </div>

                <div v-if="passwordError" class="rounded-xl border border-rose-500/20 bg-rose-500/10 px-4 py-3 text-sm text-rose-200">
                  {{ passwordError }}
                </div>

                <div class="flex justify-end gap-3 pt-2">
                  <button
                    type="button"
                    class="rounded-xl border border-white/10 bg-white/5 px-4 py-2.5 text-sm font-semibold text-slate-200 transition-colors hover:bg-white/10 disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="isChangingPassword"
                    @click="closePasswordModal"
                  >
                    {{ t('password.cancel') }}
                  </button>
                  <button
                    type="submit"
                    class="inline-flex items-center justify-center gap-2 rounded-xl bg-cyber-primary px-5 py-2.5 text-sm font-bold text-black transition-colors hover:bg-cyan-400 disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="isChangingPassword"
                  >
                    <span v-if="isChangingPassword" class="h-4 w-4 rounded-full border-2 border-black/30 border-t-black animate-spin"></span>
                    {{ isChangingPassword ? t('password.saving') : t('password.submit') }}
                  </button>
                </div>
              </div>
            </form>
          </div>
        </transition>

        <!-- Upload Modal -->
        <transition name="fade">
          <div v-if="showUploadModal && canCreateProject" class="fixed inset-0 z-50 flex items-center justify-center p-4">
            <div class="absolute inset-0 bg-black/80 backdrop-blur-sm" @click="showUploadModal = false"></div>

            <div class="relative z-10 w-full max-w-lg glass-panel rounded-2xl p-8 border-t border-cyber-primary/30 shadow-[0_0_50px_rgba(0,0,0,0.5)] animate-slide-up">
              <button @click="showUploadModal = false" class="absolute top-4 right-4 text-slate-400 hover:text-white transition-colors">
                <XCircle class="w-6 h-6" />
              </button>

              <div class="mb-8">
                <div class="w-12 h-12 bg-cyber-primary/10 rounded-full flex items-center justify-center mb-4 text-cyber-primary">
                  <Upload class="w-6 h-6" />
                </div>
                <h2 class="text-2xl font-bold text-white">{{ t('upload.title') }}</h2>
                <p class="text-slate-400 mt-2">{{ t('upload.subtitle') }}</p>
              </div>

              <div class="space-y-6">
                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('upload.projectName') }}</label>
                  <input v-model="uploadForm.name" type="text" class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-xl focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600">
                </div>

                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('upload.remarksOptional') }}</label>
                  <textarea v-model="uploadForm.remark" rows="3" class="w-full px-4 py-3 bg-slate-900/50 border border-slate-600 rounded-xl focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white placeholder-slate-600"></textarea>
                </div>

                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('upload.organization') }}</label>
                  <select
                    v-model="uploadForm.organization_id"
                    class="w-full px-4 py-3 bg-slate-900/80 border border-slate-600 rounded-xl focus:border-cyber-primary focus:ring-1 focus:ring-cyber-primary outline-none transition-all text-white"
                  >
                    <option value="" disabled>{{ t('upload.selectOrganization') }}</option>
                    <option
                      v-for="organization in writableOrganizations"
                      :key="organization.id"
                      :value="String(organization.id)"
                    >
                      {{ organization.displayName }}
                    </option>
                  </select>
                </div>

                <div class="space-y-2">
                  <label class="text-sm font-medium text-slate-300">{{ t('upload.sourceArchive') }}</label>
                  <div
                    class="relative border-2 border-dashed border-slate-600 rounded-xl p-8 text-center hover:border-cyber-primary hover:bg-cyber-primary/5 transition-all cursor-pointer group"
                  >
                    <input type="file" accept=".zip" @change="handleFileUpload" class="absolute inset-0 w-full h-full opacity-0 cursor-pointer z-10">
                    <Upload class="w-8 h-8 text-slate-500 mx-auto mb-3 group-hover:text-cyber-primary transition-colors group-hover:scale-110 duration-300" />
                    <p class="text-slate-400 font-medium group-hover:text-white transition-colors" v-if="!uploadForm.file">{{ t('upload.clickToUpload') }}</p>
                    <p class="text-cyber-primary font-bold" v-else>{{ uploadForm.file.name }}</p>
                    <p class="text-xs text-slate-500 mt-2">{{ t('upload.maxFileSize') }}</p>
                  </div>
                </div>

                <button
                  @click="createTask"
                  :disabled="isUploading"
                  class="w-full py-3.5 bg-gradient-to-r from-cyber-primary to-blue-600 hover:from-cyan-400 hover:to-blue-500 text-black font-bold rounded-xl shadow-lg transition-all transform hover:-translate-y-1 active:scale-95 disabled:opacity-50 disabled:cursor-not-allowed flex items-center justify-center gap-2"
                >
                  <span v-if="isUploading" class="w-5 h-5 border-2 border-black/30 border-t-black rounded-full animate-spin"></span>
                  {{ isUploading ? t('upload.uploadingAndExtracting') : t('upload.createProjectScan') }}
                </button>
              </div>
            </div>
          </div>
        </transition>

      </div>
    </transition>
  </div>
</template>

<style scoped>
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.4s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

.bg-scan-lines {
  background: linear-gradient(
    to bottom,
    rgba(255, 255, 255, 0),
    rgba(255, 255, 255, 0) 50%,
    rgba(0, 0, 0, 0.2) 50%,
    rgba(0, 0, 0, 0.2)
  );
  background-size: 100% 4px;
}
</style>
