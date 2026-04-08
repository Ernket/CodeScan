<script setup>
import { ref, onMounted, onBeforeUnmount, computed, watch } from 'vue'
import axios from 'axios'
import {
  Lock, Upload, Trash2, Play, Pause, RefreshCw, Server, Shield, ShieldAlert,
  FileCode, CheckCircle, XCircle, Terminal, Activity, Zap,
  LayoutDashboard, FolderOpen, LogOut, ChevronRight, Download
} from 'lucide-vue-next'
import DashboardOverview from './components/DashboardOverview.vue'
import TaskStageStrip from './components/TaskStageStrip.vue'
import AuditStageView from './components/AuditStageView.vue'
import { DEFAULT_LOCALE, LOCALE_STORAGE_KEY, getIntlLocale, getMessage } from './i18n'

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
const locale = ref(getStoredLocale())
const currentView = ref('dashboard')
const selectedTask = ref(null)
const selectedTaskId = ref('')
const tasks = ref([])
const stats = ref(createEmptyStats())
const showUploadModal = ref(false)
const uploadForm = ref({ name: '', remark: '', file: null })
const isUploading = ref(false)
const isRepairing = ref(false)
const isLoading = ref(false)
const isTaskLoading = ref(false)
const isDownloadingReport = ref(false)
const stageActionPending = ref({})
const sidebarOpen = ref(true)
const consoleContainer = ref(null)
const activeTab = ref('console')
const expandedVuln = ref(null)

const displayStats = ref({ projects: 0, interfaces: 0, vulns: 0, completed_audits: 0 })

const t = (key, params = {}) => getMessage(locale.value, key, params)
const formatDateTime = (value) => new Date(value).toLocaleString(getIntlLocale(locale.value))
const formatNumber = (value) => new Intl.NumberFormat(getIntlLocale(locale.value)).format(value || 0)
const displayStatus = (status) => t(`status.${String(status || '').trim().toLowerCase() || 'pending'}`)
const displayVerification = (status) => t(`verification.${String(status || '').trim().toLowerCase() || 'unreviewed'}`)
const displayOrigin = (origin) => t(`origin.${String(origin || '').trim().toLowerCase() || 'initial'}`)

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
  return selectedTask.value?.name || tasks.value.find(task => task.id === selectedTaskId.value)?.name || t('app.loadingTask')
})

const parseResultArray = (raw) => {
  if (!raw) return null

  try {
    if (Array.isArray(raw)) return raw
    if (raw && typeof raw === 'object') return null

    let text = String(raw).trim()
    if (!text) return null

    if (text.startsWith('```json')) {
      text = text.replace(/^```json\s*/, '').replace(/\s*```$/, '')
    } else if (text.startsWith('```')) {
      text = text.replace(/^```\s*/, '').replace(/\s*```$/, '')
    }

    const start = text.indexOf('[')
    const end = text.lastIndexOf(']')
    if (start !== -1 && end !== -1 && end > start) {
      text = text.slice(start, end + 1)
    }

    const parsed = JSON.parse(text)
    return Array.isArray(parsed) ? parsed : null
  } catch (e) {
    console.error('JSON Parse Error:', e)
    return null
  }
}

const normalizeSeverity = (value) => {
  switch (String(value || '').trim().toUpperCase()) {
    case 'CRITICAL':
      return 'CRITICAL'
    case 'MEDIUM':
      return 'MEDIUM'
    case 'LOW':
      return 'LOW'
    case 'INFO':
      return 'INFO'
    default:
      return 'HIGH'
  }
}

const verificationStatus = (item) => {
  const value = String(item?.verification_status || '').trim().toLowerCase()
  if (value === 'confirmed' || value === 'uncertain' || value === 'rejected') return value
  return 'unreviewed'
}

const buildSeverityBreakdown = (items = []) => {
  const order = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'INFO']
  const counts = items
    .filter(item => verificationStatus(item) !== 'rejected')
    .reduce((acc, item) => {
      const severity = normalizeSeverity(item?.reviewed_severity || item?.severity)
      acc[severity] = (acc[severity] || 0) + 1
      return acc
    }, {})

  return order
    .filter(label => counts[label])
    .map(label => ({ label, count: counts[label] }))
}

const severityBadgeClass = (severity) => {
  switch (normalizeSeverity(severity)) {
    case 'CRITICAL':
      return 'bg-red-500/15 text-red-300 border border-red-500/30'
    case 'MEDIUM':
      return 'bg-yellow-500/15 text-yellow-300 border border-yellow-500/30'
    case 'LOW':
      return 'bg-blue-500/15 text-blue-300 border border-blue-500/30'
    case 'INFO':
      return 'bg-slate-500/15 text-slate-300 border border-slate-500/30'
    default:
      return 'bg-orange-500/15 text-orange-300 border border-orange-500/30'
  }
}

const getStageRecord = (task, stageName) => task?.stages?.find(stage => stage.name === stageName) || null

const hasStagePayload = (stage) => {
  if (!stage) return false

  if (Array.isArray(stage.output_json)) return true

  if (typeof stage.output_json === 'string') {
    const trimmed = stage.output_json.trim()
    return trimmed !== '' && trimmed !== '{}' && trimmed !== 'null'
  }

  if (stage.output_json && typeof stage.output_json === 'object') {
    return Object.keys(stage.output_json).length > 0
  }

  return Boolean(stage.result?.trim())
}

const isStageExportable = (stage) => {
  if (!stage || stage.status !== 'completed') return false
  return parseResultArray(stage.output_json || stage.result) !== null || hasStagePayload(stage)
}

const countRouteInventory = (task) => {
  const routes = parseResultArray(task?.output_json || task?.result)
  if (!Array.isArray(routes)) return 0
  return routes.filter(item => item && typeof item === 'object' && item.method && item.path).length
}

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
    raw = currentAuditStage.value.output_json || currentAuditStage.value.result
  } else {
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

const currentAuditDefinition = computed(() => stageDefinitions.value.find(stage => stage.view === currentView.value) || null)
const currentAuditResults = computed(() => Array.isArray(parsedResult.value) ? parsedResult.value : [])
const activeAuditResults = computed(() => currentAuditResults.value.filter(item => verificationStatus(item) !== 'rejected'))
const rejectedAuditResults = computed(() => currentAuditResults.value.filter(item => verificationStatus(item) === 'rejected'))

const formatResultField = (value) => {
  if (value === null || value === undefined || value === '') return ''
  if (Array.isArray(value)) return value.join('\n')
  if (typeof value === 'object') return JSON.stringify(value, null, 2)
  return String(value)
}

const getTriggerSignature = (trigger) => {
  if (!trigger) return ''
  const method = formatResultField(trigger.method)
  const path = formatResultField(trigger.path)
  return `${method} ${path}`.trim()
}

const formatTriggerLabel = (trigger) => {
  const label = getTriggerSignature(trigger)
  return label || t('auditView.staticFinding')
}

const formatComponentLabel = (item) => {
  const name = formatResultField(item?.component_name)
  const version = formatResultField(item?.component_version)
  if (!name) return t('auditView.staticFinding')
  return version ? `${name} @ ${version}` : name
}

const summarizeStageForReport = (task, definition) => {
  const stage = getStageRecord(task, definition.key)
  if (!isStageExportable(stage)) return null

  const parsedResults = parseResultArray(stage.output_json || stage.result)
  const results = parsedResults || []
  const activeResults = results.filter(item => verificationStatus(item) !== 'rejected')
  const rejectedCount = results.length - activeResults.length
  const rawOnly = parsedResults === null
  const files = new Set()
  const interfaces = new Set()

  activeResults.forEach(item => {
    if (item?.location?.file) files.add(item.location.file)
    const trigger = getTriggerSignature(item?.trigger)
    if (trigger) interfaces.add(trigger)
  })

  return {
    ...definition,
    stage,
    rawOnly,
    results: activeResults,
    rejectedCount,
    findingCount: activeResults.length,
    uniqueFiles: files.size,
    uniqueInterfaces: interfaces.size,
    completedAt: stage?.updated_at ? formatDateTime(stage.updated_at) : '',
    severityBreakdown: buildSeverityBreakdown(activeResults),
    summaryText: rawOnly
      ? t('reportView.rawStageNote')
      : activeResults.length === 0
        ? rejectedCount > 0
          ? t('auditView.allRejected')
          : t('reportView.cleanStageNote')
        : locale.value === 'zh'
          ? `已准备导出 ${activeResults.length} 条发现。`
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
    if (!stage.rawOnly && stage.findingCount === 0) cleanStageCount += 1

    stage.results.forEach(item => {
      if (item?.location?.file) files.add(item.location.file)
      const trigger = getTriggerSignature(item?.trigger)
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

const isTypedResult = (result, type) => Array.isArray(result) && result.length > 0 && result[0].type === type
const isRCEResult = (result) => isTypedResult(result, 'RCE')
const isInjectionResult = (result) => isTypedResult(result, 'Injection')
const isAuthenticationResult = (result) => isTypedResult(result, 'Authentication')
const isAuthorizationResult = (result) => isTypedResult(result, 'Authorization')
const isXSSResult = (result) => isTypedResult(result, 'XSS')
const isConfigurationResult = (result) => isTypedResult(result, 'Configuration')
const isFileOperationResult = (result) => isTypedResult(result, 'FileOperation')
const isBusinessLogicResult = (result) => isTypedResult(result, 'BusinessLogic')

const toggleDetails = (idx) => {
  expandedVuln.value = expandedVuln.value === idx ? null : idx
}

const stageActionKey = (stageName, action) => `${stageName}:${action}`
const isStageActionPending = (stageName, action) => Boolean(stageActionPending.value[stageActionKey(stageName, action)])

const authConfig = () => ({ headers: { Authorization: authKey.value } })

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

const goDashboard = () => {
  currentView.value = 'dashboard'
  selectedTask.value = null
  selectedTaskId.value = ''
  expandedVuln.value = null
}

const fetchTaskDetail = async (taskId = selectedTaskId.value, options = {}) => {
  if (!taskId || !isAuthenticated.value) return null

  const { silent = false, fallback = null } = options

  if (fallback) {
    selectedTask.value = snapshotTaskSummary(fallback)
  }
  if (!silent) {
    isTaskLoading.value = true
  }

  try {
    const res = await axios.get(`${API_URL}/tasks/${taskId}`, authConfig())
    selectedTask.value = res.data
    selectedTaskId.value = res.data.id
    return res.data
  } catch (e) {
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
    if (!silent) {
      isTaskLoading.value = false
    }
  }
}

const runStage = async (taskId, stageName, options = {}) => {
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

const runStagePostAction = async (taskId, stageName, actionPath, actionLabel, options = {}) => {
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
  if (!task || task.status === 'running') return false
  if (stageName === 'init') return Array.isArray(parseResultArray(task?.output_json || task?.result))
  const stage = getStageRecord(task, stageName)
  return Boolean(stage && stage.status === 'completed' && Array.isArray(parseResultArray(stage.output_json || stage.result)))
}

const canRevalidateStage = (task, stageName) => {
  if (!task || task.status === 'running' || stageName === 'init') return false
  const stage = getStageRecord(task, stageName)
  const parsed = parseResultArray(stage?.output_json || stage?.result)
  return Boolean(stage && stage.status === 'completed' && Array.isArray(parsed) && parsed.length > 0)
}

const repairJSON = async (taskId, stageName) => {
  if (isRepairing.value) return
  isRepairing.value = true
  try {
    await axios.post(`${API_URL}/tasks/${taskId}/repair?stage=${stageName}`, {}, authConfig())
    await fetchData()
    alert(t('alerts.jsonRepaired'))
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
    const res = await axios.post(`${API_URL}/login`, { key: authKey.value })
    if (res.data.token) {
      localStorage.setItem('auth_token', res.data.token)
      isAuthenticated.value = true
      await fetchData()
    }
  } catch {
    alert(t('alerts.authenticationFailed'))
  }
}

const logout = () => {
  isAuthenticated.value = false
  localStorage.removeItem('auth_token')
  authKey.value = ''
  tasks.value = []
  stats.value = createEmptyStats()
  displayStats.value = { projects: 0, interfaces: 0, vulns: 0, completed_audits: 0 }
  goDashboard()
  stopPolling()
}

const checkAuth = () => {
  const token = localStorage.getItem('auth_token')
  if (token) {
    authKey.value = token
    isAuthenticated.value = true
    fetchData()
  }
}

const fetchData = async () => {
  if (!isAuthenticated.value) return

  isLoading.value = true
  try {
    const [statsRes, tasksRes] = await Promise.all([
      axios.get(`${API_URL}/stats`, authConfig()),
      axios.get(`${API_URL}/tasks`, authConfig())
    ])

    stats.value = normalizeStatsResponse(statsRes.data)
    tasks.value = Array.isArray(tasksRes.data) ? tasksRes.data : []

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

const createTask = async () => {
  if (!uploadForm.value.file) return alert(t('alerts.pleaseSelectFile'))

  const formData = new FormData()
  formData.append('name', uploadForm.value.name)
  formData.append('remark', uploadForm.value.remark)
  formData.append('file', uploadForm.value.file)

  isUploading.value = true
  try {
    await axios.post(`${API_URL}/tasks`, formData, {
      headers: {
        Authorization: authKey.value,
        'Content-Type': 'multipart/form-data'
      }
    })
    showUploadModal.value = false
    uploadForm.value = { name: '', remark: '', file: null }
    await fetchData()
  } catch (e) {
    alert(t('alerts.uploadFailed', { message: e.response?.data?.error || e.message }))
  } finally {
    isUploading.value = false
  }
}

const getTaskForDelete = (id) => {
  if (selectedTask.value?.id === id) return selectedTask.value
  return tasks.value.find(task => task.id === id) || null
}

const deleteTask = async (id) => {
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
  if (action === 'start') {
    await runStage(id, 'init', { skipConfirm: true, successMessage: t('alerts.scanStarted') })
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
  selectedTaskId.value = task.id
  selectedTask.value = snapshotTaskSummary(task)
  currentView.value = 'task-detail'
  activeTab.value = 'console'
  expandedVuln.value = null
  await fetchTaskDetail(task.id, { fallback: task })
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
                  <label class="text-xs uppercase tracking-wider text-slate-500 font-semibold ml-1">{{ t('login.securityKey') }}</label>
                  <div class="relative group">
                    <Lock class="absolute left-4 top-3.5 w-5 h-5 text-slate-600 group-focus-within:text-cyber-primary transition-colors duration-300" />
                    <input 
                      v-model="authKey" 
                      type="password" 
                      :placeholder="t('login.placeholder')"
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
                @click="showUploadModal = true"
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
              @refresh="fetchData"
              @open-task="openTask"
              @delete-task="deleteTask"
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
                </div>

                <div class="flex flex-wrap gap-3">
                  <button 
                    @click="currentView = 'task-rce'"
                    class="px-5 py-2.5 bg-red-500/10 text-red-400 border border-red-500/30 hover:bg-red-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <ShieldAlert class="w-4 h-4" /> {{ stageLabelByKey.rce }}
                  </button>
                  <button 
                    @click="currentView = 'task-injection'"
                    class="px-5 py-2.5 bg-amber-500/10 text-amber-400 border border-amber-500/30 hover:bg-amber-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <ShieldAlert class="w-4 h-4" /> {{ stageLabelByKey.injection }}
                  </button>
                  <button 
                    @click="currentView = 'task-auth'"
                    class="px-5 py-2.5 bg-sky-500/10 text-sky-400 border border-sky-500/30 hover:bg-sky-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Lock class="w-4 h-4" /> {{ stageLabelByKey.auth }}
                  </button>
                  <button 
                    @click="currentView = 'task-access'"
                    class="px-5 py-2.5 bg-indigo-500/10 text-indigo-400 border border-indigo-500/30 hover:bg-indigo-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Shield class="w-4 h-4" /> {{ stageLabelByKey.access }}
                  </button>
                  <button 
                    @click="currentView = 'task-xss'"
                    class="px-5 py-2.5 bg-emerald-500/10 text-emerald-400 border border-emerald-500/30 hover:bg-emerald-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <ShieldAlert class="w-4 h-4" /> {{ stageLabelByKey.xss }}
                  </button>
                  <button 
                    @click="currentView = 'task-config'"
                    class="px-5 py-2.5 bg-cyan-500/10 text-cyan-400 border border-cyan-500/30 hover:bg-cyan-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <FileCode class="w-4 h-4" /> {{ stageLabelByKey.config }}
                  </button>
                  <button 
                    @click="currentView = 'task-fileop'"
                    class="px-5 py-2.5 bg-orange-500/10 text-orange-400 border border-orange-500/30 hover:bg-orange-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <FolderOpen class="w-4 h-4" /> {{ stageLabelByKey.fileop }}
                  </button>
                  <button 
                    @click="currentView = 'task-logic'"
                    class="px-5 py-2.5 bg-rose-500/10 text-rose-400 border border-rose-500/30 hover:bg-rose-500/20 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Zap class="w-4 h-4" /> {{ stageLabelByKey.logic }}
                  </button>
                  <button 
                    @click="currentView = 'task-report'"
                    class="px-5 py-2.5 bg-white/5 text-slate-100 border border-white/10 hover:bg-white/10 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Download class="w-4 h-4" /> {{ t('taskDetail.reportExport') }}
                    <span class="px-2 py-0.5 rounded-full bg-white/10 text-xs text-slate-300">{{ reportStages.length }}</span>
                  </button>
                  <button 
                    v-if="selectedTask.status === 'pending' || selectedTask.status === 'failed'"
                    @click="taskAction(selectedTask.id, 'start')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" /> {{ t('taskDetail.startScan') }}
                  </button>
                  <button 
                    v-if="selectedTask.status === 'running'"
                    @click="taskAction(selectedTask.id, 'pause')"
                    class="px-5 py-2.5 bg-yellow-500/20 text-yellow-400 border border-yellow-500/50 hover:bg-yellow-500/30 rounded-lg font-bold flex items-center gap-2 transition-all"
                  >
                    <Pause class="w-4 h-4" /> {{ t('common.pause') }}
                  </button>
                  <button 
                    v-if="selectedTask.status === 'paused'"
                    @click="taskAction(selectedTask.id, 'resume')"
                    class="glass-button px-5 py-2.5 rounded-lg flex items-center gap-2"
                  >
                    <Play class="w-4 h-4" /> {{ t('common.resume') }}
                  </button>
                  <button 
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
                :locale="locale"
                :t="t"
                @select-stage="currentView = $event"
              />

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
                        @click="runGapCheck(selectedTask.id, 'init')"
                        :disabled="!canGapCheckStage(selectedTask, 'init') || isStageActionPending('init', 'gap-check')"
                        class="px-4 py-2 bg-white/5 hover:bg-white/10 text-slate-100 border border-white/10 rounded-lg font-bold text-sm flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                       >
                        <RefreshCw :class="['w-4 h-4', isStageActionPending('init', 'gap-check') ? 'animate-spin' : '']" />
                        {{ isStageActionPending('init', 'gap-check') ? t('auditView.gapChecking') : t('taskDetail.gapCheckRoutes') }}
                       </button>
                     </div>
                  </div>

                  <!-- Routes Table (Default) -->
                  <div v-if="parsedResult && !isRCEResult(parsedResult) && !isInjectionResult(parsedResult) && !isAuthenticationResult(parsedResult) && !isAuthorizationResult(parsedResult) && !isXSSResult(parsedResult) && !isConfigurationResult(parsedResult) && !isFileOperationResult(parsedResult) && !isBusinessLogicResult(parsedResult)" class="overflow-x-auto">
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

                  <!-- RCE Vulnerability Table: REMOVED (Moved to Task RCE View) -->
                  <div v-else-if="parsedResult && isRCEResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.rce }) }}</p>
                     <button 
                        @click="currentView = 'task-rce'"
                        class="px-4 py-2 bg-red-500/20 hover:bg-red-500/30 text-red-400 border border-red-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <ShieldAlert class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.rce }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isInjectionResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.injection }) }}</p>
                     <button 
                        @click="currentView = 'task-injection'"
                        class="px-4 py-2 bg-amber-500/20 hover:bg-amber-500/30 text-amber-400 border border-amber-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <ShieldAlert class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.injection }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isAuthenticationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.auth }) }}</p>
                     <button 
                        @click="currentView = 'task-auth'"
                        class="px-4 py-2 bg-sky-500/20 hover:bg-sky-500/30 text-sky-400 border border-sky-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <Lock class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.auth }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isAuthorizationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.access }) }}</p>
                     <button 
                        @click="currentView = 'task-access'"
                        class="px-4 py-2 bg-indigo-500/20 hover:bg-indigo-500/30 text-indigo-400 border border-indigo-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <Shield class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.access }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isXSSResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.xss }) }}</p>
                     <button 
                        @click="currentView = 'task-xss'"
                        class="px-4 py-2 bg-emerald-500/20 hover:bg-emerald-500/30 text-emerald-400 border border-emerald-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <ShieldAlert class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.xss }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isConfigurationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.config }) }}</p>
                     <button 
                        @click="currentView = 'task-config'"
                        class="px-4 py-2 bg-cyan-500/20 hover:bg-cyan-500/30 text-cyan-400 border border-cyan-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <FileCode class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.config }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isFileOperationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.fileop }) }}</p>
                     <button 
                        @click="currentView = 'task-fileop'"
                        class="px-4 py-2 bg-orange-500/20 hover:bg-orange-500/30 text-orange-400 border border-orange-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <FolderOpen class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.fileop }) }}
                     </button>
                  </div>
                  <div v-else-if="parsedResult && isBusinessLogicResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <p class="mb-4">{{ t('taskDetail.auditCompleted', { stage: stageLabelByKey.logic }) }}</p>
                     <button 
                        @click="currentView = 'task-logic'"
                        class="px-4 py-2 bg-rose-500/20 hover:bg-rose-500/30 text-rose-400 border border-rose-500/30 rounded flex items-center gap-2 transition-all"
                     >
                        <Zap class="w-4 h-4" />
                        {{ t('taskDetail.viewStageResults', { stage: stageShortLabelByKey.logic }) }}
                     </button>
                  </div>

                  <!-- Fallback Text View if not JSON -->
                  <div v-else-if="selectedTask.result" class="font-mono text-sm text-slate-300 whitespace-pre-wrap">
                    {{ selectedTask.result }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
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

            <!-- Task Report View -->
            <div v-if="currentView === 'task-report' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col xl:flex-row justify-between items-start xl:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> {{ t('common.backToTask') }}
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-slate-300 text-sm font-mono">{{ t('taskDetail.reportExport') }}</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">{{ t('reportView.title') }}</h1>
                  <p class="text-slate-400 mt-1">{{ t('reportView.subtitle') }}</p>
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
                        <div v-else-if="stage.findingCount === 0" class="rounded-xl border border-emerald-500/20 bg-emerald-500/10 px-4 py-4 text-sm text-emerald-200">{{ t('reportView.cleanStageNote') }}</div>
                        <div v-else class="space-y-3">
                          <div 
                            v-for="(finding, idx) in stage.results.slice(0, 3)"
                            :key="`${stage.key}-${idx}`"
                            class="rounded-xl border border-white/10 bg-black/20 px-4 py-4"
                          >
                            <div class="flex flex-col lg:flex-row lg:items-start lg:justify-between gap-3">
                              <div>
                                <div class="flex items-center gap-2 flex-wrap">
                                  <span :class="['px-2.5 py-1 rounded-full text-xs font-bold', severityBadgeClass(finding.severity)]">{{ normalizeSeverity(finding.severity) }}</span>
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
              @back="currentView = 'task-detail'"
              @update:activeTab="activeTab = $event"
              @run="runStage(selectedTask.id, currentAuditDefinition.key)"
              @gap-check="runGapCheck(selectedTask.id, currentAuditDefinition.key)"
              @revalidate="revalidateStage(selectedTask.id, currentAuditDefinition.key)"
              @repair="repairJSON(selectedTask.id, currentAuditDefinition.key)"
            />

            <!-- Legacy Task RCE View -->
            <div v-if="false && currentView === 'task-rce' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <!-- Header -->
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-red-500 text-sm font-mono">RCE Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Vulnerability Audit</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for Remote Code Execution risks</p>
                </div>

                <div class="flex gap-3">
                     <button 
                        @click="runStage(selectedTask.id, 'rce')"
                        :disabled="selectedTask.status === 'running'"
                        class="px-5 py-2.5 bg-red-500 hover:bg-red-600 text-white font-bold rounded-lg shadow-[0_0_20px_rgba(239,68,68,0.4)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                     >
                        <ShieldAlert class="w-4 h-4" />
                        {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run RCE Audit' }}
                     </button>
                </div>
              </div>

              <!-- Content -->
              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-red-500/20 shadow-[0_0_30px_rgba(239,68,68,0.1)]">
                 <!-- Tabs (Console vs Results) -->
                 <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

                <!-- Console -->
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
                     <p>Ready to start audit.</p>
                  </div>
                </div>

                <!-- Results -->
                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isRCEResult(parsedResult))" class="space-y-4">
                     <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-red-500/5 border border-red-500/20 rounded-lg p-4">
                        <div class="flex justify-between items-start mb-2">
                           <div>
                              <div class="flex items-center gap-2">
                                 <span class="px-2 py-0.5 bg-red-500 text-black text-xs font-bold rounded uppercase">
                                    {{ vuln.severity || 'CRITICAL' }}
                                 </span>
                                 <span class="text-red-400 font-bold text-lg">{{ vuln.subtype }}</span>
                              </div>
                              <div class="text-slate-400 text-sm mt-1 font-mono">
                                 {{ vuln.location?.file }}:{{ vuln.location?.line }}
                              </div>
                           </div>
                           <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                              {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                           </button>
                        </div>
                        <p class="text-slate-300 mb-3">{{ vuln.description }}</p>
                        <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-red-500/10 space-y-4 animate-fade-in">
                           <div class="bg-black/30 p-3 rounded border border-white/5">
                              <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                              <div class="font-mono text-sm text-cyber-primary">
                                 {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                              </div>
                           </div>
                           
                           <div v-if="vuln.execution_logic">
                              <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                              <p class="text-sm text-slate-300">{{ vuln.execution_logic }}</p>
                           </div>

                           <div v-if="vuln.vulnerable_code">
                              <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                              <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5">{{ vuln.vulnerable_code }}</pre>
                           </div>

                           <div>
                              <div class="text-xs text-slate-500 uppercase mb-1">HTTP POC Payload</div>
                              <div class="relative group">
                                 <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-green-400 overflow-x-auto border border-white/5">{{ vuln.poc }}</pre>
                                 <button class="absolute top-2 right-2 opacity-0 group-hover:opacity-100 px-2 py-1 bg-white/10 text-white text-xs rounded">Copy</button>
                              </div>
                           </div>
                        </div>
                     </div>
                     <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                        <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                        <p>No RCE Vulnerabilities Found</p>
                        <!-- Optional Repair button if user suspects error -->
                        <button 
                          v-if="currentRawResult && currentRawResult.length > 50"
                          @click="repairJSON(selectedTask.id, 'rce')"
                          :disabled="isRepairing"
                          class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                        >
                          <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                          Suspect Parsing Error? Repair JSON
                        </button>
                     </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isRCEResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                        <ShieldAlert class="w-12 h-12 opacity-50 mb-2" />
                        <p class="text-lg text-slate-400">Result Format Mismatch</p>
                        <p class="text-sm">The AI output was parsed as JSON but doesn't match the RCE Vulnerability schema.</p>
                        
                        <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                           <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                           <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                        </div>

                        <button 
                           @click="repairJSON(selectedTask.id, 'rce')"
                           :disabled="isRepairing"
                           class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                        >
                           <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                           {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                        </button>
                     </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                     {{ currentRawResult }}
                     <div class="mt-4 pt-4 border-t border-white/10">
                       <button 
                         @click="repairJSON(selectedTask.id, 'rce')"
                         :disabled="isRepairing"
                         class="px-4 py-2 bg-red-500/10 hover:bg-red-500/20 text-red-400 border border-red-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                       >
                         <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                         {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                       </button>
                     </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No RCE results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task Injection View -->
            <div v-if="false && currentView === 'task-injection' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <!-- Header -->
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-amber-500 text-sm font-mono">Injection Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Injection Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for SQLi, Command Injection, and other injection risks</p>
                </div>

                <div class="flex gap-3">
                     <button 
                        @click="runStage(selectedTask.id, 'injection')"
                        :disabled="selectedTask.status === 'running'"
                        class="px-5 py-2.5 bg-amber-500 hover:bg-amber-600 text-black font-bold rounded-lg shadow-[0_0_20px_rgba(245,158,11,0.4)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                     >
                        <ShieldAlert class="w-4 h-4" />
                        {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run Injection Audit' }}
                     </button>
                </div>
              </div>

              <!-- Content -->
              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-amber-500/20 shadow-[0_0_30px_rgba(245,158,11,0.1)]">
                 <!-- Tabs -->
                 <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

                <!-- Console -->
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
                     <p>Ready to start injection audit.</p>
                  </div>
                </div>

                <!-- Results -->
                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isInjectionResult(parsedResult))" class="space-y-4">
                     <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-amber-500/5 border border-amber-500/20 rounded-lg p-4">
                        <div class="flex justify-between items-start mb-2">
                           <div>
                              <div class="flex items-center gap-2">
                                 <span class="px-2 py-0.5 bg-amber-500 text-black text-xs font-bold rounded uppercase">
                                    {{ vuln.severity || 'CRITICAL' }}
                                 </span>
                                 <span class="text-amber-400 font-bold text-lg">{{ vuln.subtype }}</span>
                              </div>
                              <div class="text-slate-400 text-sm mt-1 font-mono">
                                 {{ vuln.location?.file }}:{{ vuln.location?.line }}
                              </div>
                           </div>
                           <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                              {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                           </button>
                        </div>
                        <p class="text-slate-300 mb-3">{{ vuln.description }}</p>
                        <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-amber-500/10 space-y-4 animate-fade-in">
                           <div class="bg-black/30 p-3 rounded border border-white/5">
                              <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                              <div class="font-mono text-sm text-cyber-primary">
                                 {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                              </div>
                           </div>

                           <div v-if="vuln.execution_logic">
                              <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                              <p class="text-sm text-slate-300">{{ vuln.execution_logic }}</p>
                           </div>

                           <div v-if="vuln.vulnerable_code">
                              <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                              <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5">{{ vuln.vulnerable_code }}</pre>
                           </div>

                           <div>
                              <div class="text-xs text-slate-500 uppercase mb-1">HTTP POC Payload</div>
                              <div class="relative group">
                                 <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-amber-400 overflow-x-auto border border-white/5">{{ vuln.poc }}</pre>
                                 <button class="absolute top-2 right-2 opacity-0 group-hover:opacity-100 px-2 py-1 bg-white/10 text-white text-xs rounded">Copy</button>
                              </div>
                           </div>
                        </div>
                     </div>
                     <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                        <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                        <p>No Injection Vulnerabilities Found</p>
                        <button 
                          v-if="currentRawResult && currentRawResult.length > 50"
                          @click="repairJSON(selectedTask.id, 'injection')"
                          :disabled="isRepairing"
                          class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                        >
                          <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                          Suspect Parsing Error? Repair JSON
                        </button>
                     </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isInjectionResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                     <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                        <ShieldAlert class="w-12 h-12 opacity-50 mb-2" />
                        <p class="text-lg text-slate-400">Result Format Mismatch</p>
                        <p class="text-sm">The AI output was parsed as JSON but doesn't match the Injection Vulnerability schema.</p>
                        
                        <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                           <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                           <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                        </div>

                        <button 
                           @click="repairJSON(selectedTask.id, 'injection')"
                           :disabled="isRepairing"
                           class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                        >
                           <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                           {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                        </button>
                     </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                     {{ currentRawResult }}
                     <div class="mt-4 pt-4 border-t border-white/10">
                       <button 
                         @click="repairJSON(selectedTask.id, 'injection')"
                         :disabled="isRepairing"
                         class="px-4 py-2 bg-amber-500/10 hover:bg-amber-500/20 text-amber-500 border border-amber-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                       >
                         <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                         {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                       </button>
                     </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No Injection results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task Auth View -->
            <div v-if="false && currentView === 'task-auth' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-sky-400 text-sm font-mono">Auth & Session Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Authentication Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for authentication flow and session security risks</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'auth')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-sky-500 hover:bg-sky-600 text-black font-bold rounded-lg shadow-[0_0_20px_rgba(14,165,233,0.4)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <Lock class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run Auth & Session Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-sky-500/20 shadow-[0_0_30px_rgba(14,165,233,0.1)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start auth audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isAuthenticationResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-sky-500/5 border border-sky-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-sky-500 text-black text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-sky-400 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-sky-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-2">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Auth Mechanism</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.auth_mechanism) || 'Unknown' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Affected Endpoints</div>
                            <pre class="text-xs font-mono text-sky-300 whitespace-pre-wrap">{{ formatResultField(vuln.affected_endpoints) || 'Not provided' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Session Artifact</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.session_artifact) || 'Not applicable' }}</pre>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-sky-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.trigger_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Trigger Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.trigger_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.impact)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Impact</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.impact) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No Authentication or Session Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'auth')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isAuthenticationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <Lock class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the Authentication Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'auth')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'auth')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-sky-500/10 hover:bg-sky-500/20 text-sky-400 border border-sky-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No authentication audit results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task Access Control View -->
            <div v-if="false && currentView === 'task-access' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-indigo-400 text-sm font-mono">Access Control Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Authorization Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for horizontal, vertical, and function-level access control risks</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'access')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-indigo-500 hover:bg-indigo-600 text-white font-bold rounded-lg shadow-[0_0_20px_rgba(99,102,241,0.4)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <Shield class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run Access Control Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-indigo-500/20 shadow-[0_0_30px_rgba(99,102,241,0.12)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start access control audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isAuthorizationResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-indigo-500/5 border border-indigo-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-indigo-500 text-white text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-indigo-300 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-indigo-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-2">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Authentication State</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.authentication_state) || 'Unknown' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Required Privilege</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.required_privilege) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Access Boundary</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.access_boundary) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Attacker Profile</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.attacker_profile) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Target Profile</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.target_profile) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Target Resource</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.target_resource) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Affected Endpoints</div>
                            <pre class="text-xs font-mono text-indigo-300 whitespace-pre-wrap">{{ formatResultField(vuln.affected_endpoints) || 'Not provided' }}</pre>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.authorization_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Authorization Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.authorization_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.bypass_vector)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Bypass Vector</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-indigo-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.bypass_vector) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-indigo-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.trigger_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Trigger Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.trigger_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.impact)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Impact</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.impact) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No Authorization or Access Control Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'access')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isAuthorizationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <Shield class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the Authorization Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'access')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'access')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-indigo-500/10 hover:bg-indigo-500/20 text-indigo-300 border border-indigo-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No access control audit results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task XSS View -->
            <div v-if="false && currentView === 'task-xss' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-emerald-500 text-sm font-mono">XSS Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">XSS Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for reflected, stored, and DOM-based Cross-Site Scripting risks</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'xss')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-emerald-500 hover:bg-emerald-600 text-black font-bold rounded-lg shadow-[0_0_20px_rgba(16,185,129,0.4)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <ShieldAlert class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run XSS Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-emerald-500/20 shadow-[0_0_30px_rgba(16,185,129,0.1)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start XSS audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isXSSResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-emerald-500/5 border border-emerald-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-emerald-500 text-black text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-emerald-400 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-emerald-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-2">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Sink Type</div>
                            <div class="font-mono text-sm text-emerald-300">{{ formatResultField(vuln.sink_type) }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Render Context</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.render_context) }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Storage Point</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.storage_point) || 'Not applicable' }}</div>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.payload_hint)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Payload Hint</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-emerald-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.payload_hint) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-emerald-400 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.trigger_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Trigger Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.trigger_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.expected_execution)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Expected Execution</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.expected_execution) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No XSS Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'xss')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isXSSResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <ShieldAlert class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the XSS Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'xss')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'xss')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-emerald-500/10 hover:bg-emerald-500/20 text-emerald-400 border border-emerald-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No XSS results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task Config View -->
            <div v-if="false && currentView === 'task-config' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-cyan-400 text-sm font-mono">Config & Component Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Configuration Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for exposed secrets, unsafe defaults, risky parser settings, and dependency security issues</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'config')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-cyan-500 hover:bg-cyan-600 text-black font-bold rounded-lg shadow-[0_0_20px_rgba(6,182,212,0.35)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <FileCode class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run Config & Component Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-cyan-500/20 shadow-[0_0_30px_rgba(6,182,212,0.1)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start configuration and component audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isConfigurationResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-cyan-500/5 border border-cyan-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-cyan-500 text-black text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-cyan-300 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-cyan-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger / Proof Type</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ formatTriggerLabel(vuln.trigger) }}
                            </div>
                            <div class="text-xs text-slate-400 mt-2">
                              Proof Type: <span class="font-mono">{{ formatResultField(vuln.proof_type) || 'Unknown' }}</span>
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-1">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Configuration Item</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.configuration_item) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Component</div>
                            <div class="text-sm text-slate-300">{{ formatComponentLabel(vuln) }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Reference ID</div>
                            <div class="font-mono text-sm text-cyan-300">{{ formatResultField(vuln.reference_id) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5 md:col-span-2">
                            <div class="text-xs text-slate-500 uppercase mb-1">Affected Endpoints</div>
                            <pre class="text-xs font-mono text-cyan-300 whitespace-pre-wrap">{{ formatResultField(vuln.affected_endpoints) || 'Not provided' }}</pre>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.exposure_mechanism)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Exposure Mechanism</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-cyan-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.exposure_mechanism) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.upgrade_recommendation)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Upgrade Recommendation</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-cyan-200 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.upgrade_recommendation) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code / Config</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-cyan-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.reproduction_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Reproduction Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.reproduction_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.impact)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Impact</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.impact) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No Configuration or Component Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'config')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isConfigurationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <FileCode class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the Configuration Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'config')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'config')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-cyan-500/10 hover:bg-cyan-500/20 text-cyan-400 border border-cyan-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No configuration audit results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task File Operation View -->
            <div v-if="false && currentView === 'task-fileop' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-orange-400 text-sm font-mono">File Operation Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">File Operation Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for unsafe upload, download, path traversal, and file inclusion risks</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'fileop')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-orange-500 hover:bg-orange-600 text-black font-bold rounded-lg shadow-[0_0_20px_rgba(249,115,22,0.35)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <FolderOpen class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run File Operation Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-orange-500/20 shadow-[0_0_30px_rgba(249,115,22,0.1)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start file operation audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isFileOperationResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-orange-500/5 border border-orange-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-orange-500 text-black text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-orange-300 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-orange-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-2">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">File Operation</div>
                            <div class="font-mono text-sm text-orange-300">{{ formatResultField(vuln.file_operation) || 'Unknown' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Input Vector</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.input_vector) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Target Path</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.target_path) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5 md:col-span-2">
                            <div class="text-xs text-slate-500 uppercase mb-1">Validation Logic</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.validation_logic) || 'Not provided' }}</pre>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.payload_hint)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Payload Hint</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-orange-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.payload_hint) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-orange-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.trigger_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Trigger Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.trigger_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.impact)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Impact</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.impact) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No File Operation Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'fileop')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isFileOperationResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <FolderOpen class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the File Operation Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'fileop')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'fileop')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-orange-500/10 hover:bg-orange-500/20 text-orange-400 border border-orange-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No file operation audit results available.</p>
                  </div>
                </div>
              </div>
            </div>

            <!-- Task Business Logic View -->
            <div v-if="false && currentView === 'task-logic' && selectedTask" class="space-y-6 max-w-7xl mx-auto animate-slide-up">
              <div class="glass-panel p-6 rounded-2xl flex flex-col md:flex-row justify-between items-start md:items-center gap-4">
                <div>
                  <div class="flex items-center gap-2 mb-1">
                    <button @click="currentView = 'task-detail'" class="text-slate-400 hover:text-white transition-colors text-sm flex items-center gap-1">
                      <LayoutDashboard class="w-3 h-3" /> Back to Task
                    </button>
                    <span class="text-slate-600">/</span>
                    <span class="text-rose-400 text-sm font-mono">Business Logic Audit</span>
                  </div>
                  <h1 class="text-3xl font-bold text-white">Business Logic Analysis</h1>
                  <p class="text-slate-400 mt-1">Deep analysis for workflow bypass, race conditions, amount tampering, and business rule abuse</p>
                </div>

                <div class="flex gap-3">
                  <button 
                    @click="runStage(selectedTask.id, 'logic')"
                    :disabled="selectedTask.status === 'running'"
                    class="px-5 py-2.5 bg-rose-500 hover:bg-rose-600 text-white font-bold rounded-lg shadow-[0_0_20px_rgba(244,63,94,0.35)] transition-all flex items-center gap-2 disabled:opacity-50 disabled:cursor-not-allowed"
                  >
                    <Zap class="w-4 h-4" />
                    {{ selectedTask.status === 'running' ? 'Audit in Progress...' : 'Run Business Logic Audit' }}
                  </button>
                </div>
              </div>

              <div class="glass-panel rounded-2xl overflow-hidden flex flex-col h-[600px] border border-rose-500/20 shadow-[0_0_30px_rgba(244,63,94,0.1)]">
                <div class="bg-black/40 px-6 py-3 border-b border-white/5 flex items-center justify-between">
                  <div class="flex items-center gap-4">
                    <button 
                      @click="activeTab = 'console'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'console' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Terminal class="w-4 h-4" />
                      Console
                    </button>
                    <button 
                      @click="activeTab = 'results'"
                      :class="['flex items-center gap-2 px-3 py-1.5 rounded-lg text-sm font-medium transition-colors', activeTab === 'results' ? 'bg-white/10 text-white' : 'text-slate-400 hover:text-white']"
                    >
                      <Activity class="w-4 h-4" />
                      Results
                    </button>
                  </div>
                </div>

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
                    <p>Ready to start business logic audit.</p>
                  </div>
                </div>

                <div v-if="activeTab === 'results'" class="flex-1 bg-slate-900/50 p-6 overflow-auto">
                  <div v-if="parsedResult && (parsedResult.length === 0 || isBusinessLogicResult(parsedResult))" class="space-y-4">
                    <div v-for="(vuln, idx) in parsedResult" :key="idx" class="bg-rose-500/5 border border-rose-500/20 rounded-lg p-4">
                      <div class="flex justify-between items-start mb-2">
                        <div>
                          <div class="flex items-center gap-2">
                            <span class="px-2 py-0.5 bg-rose-500 text-white text-xs font-bold rounded uppercase">
                              {{ vuln.severity || 'HIGH' }}
                            </span>
                            <span class="text-rose-300 font-bold text-lg">{{ vuln.subtype }}</span>
                          </div>
                          <div class="text-slate-400 text-sm mt-1 font-mono">
                            {{ vuln.location?.file }}:{{ vuln.location?.line }}
                          </div>
                        </div>
                        <button class="text-slate-400 hover:text-white" @click="toggleDetails(idx)">
                          {{ expandedVuln === idx ? 'Collapse' : 'Details' }}
                        </button>
                      </div>
                      <p class="text-slate-300 mb-3">{{ vuln.description }}</p>

                      <div v-if="expandedVuln === idx" class="mt-4 pt-4 border-t border-rose-500/10 space-y-4 animate-fade-in">
                        <div class="grid md:grid-cols-2 gap-3">
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Trigger Endpoint</div>
                            <div class="font-mono text-sm text-cyber-primary">
                              {{ vuln.trigger?.method }} {{ vuln.trigger?.path }}
                            </div>
                            <div v-if="vuln.trigger?.parameter" class="text-xs text-slate-400 mt-2">
                              Parameter: <span class="font-mono">{{ vuln.trigger?.parameter }}</span>
                            </div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Proof Type</div>
                            <div class="font-mono text-sm text-rose-300">{{ formatResultField(vuln.proof_type) || 'Unknown' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Workflow Name</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.workflow_name) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Business Action</div>
                            <div class="text-sm text-slate-300">{{ formatResultField(vuln.business_action) || 'Not provided' }}</div>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Affected Endpoints</div>
                            <pre class="text-xs font-mono text-rose-300 whitespace-pre-wrap">{{ formatResultField(vuln.affected_endpoints) || 'Not provided' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Manipulated Fields</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.manipulated_fields) || 'Not provided' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5 md:col-span-2">
                            <div class="text-xs text-slate-500 uppercase mb-1">Preconditions</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.preconditions) || 'Not provided' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5 md:col-span-2">
                            <div class="text-xs text-slate-500 uppercase mb-1">State Transition</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.state_transition) || 'Not provided' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Race Window</div>
                            <pre class="text-xs font-mono text-slate-300 whitespace-pre-wrap">{{ formatResultField(vuln.race_window) || 'Not applicable' }}</pre>
                          </div>
                          <div class="bg-black/30 p-3 rounded border border-white/5">
                            <div class="text-xs text-slate-500 uppercase mb-1">Bypass Vector</div>
                            <pre class="text-xs font-mono text-rose-300 whitespace-pre-wrap">{{ formatResultField(vuln.bypass_vector) || 'Not provided' }}</pre>
                          </div>
                        </div>

                        <div v-if="formatResultField(vuln.execution_logic)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Execution Logic</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.execution_logic) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.vulnerable_code)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Vulnerable Code</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-blue-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.vulnerable_code) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.poc_http)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Raw HTTP POC</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-rose-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.poc_http) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.trigger_steps)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Trigger Steps</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.trigger_steps) }}</pre>
                        </div>

                        <div v-if="formatResultField(vuln.impact)">
                          <div class="text-xs text-slate-500 uppercase mb-1">Impact</div>
                          <pre class="bg-slate-950 p-4 rounded text-xs font-mono text-slate-300 overflow-x-auto border border-white/5 whitespace-pre-wrap">{{ formatResultField(vuln.impact) }}</pre>
                        </div>
                      </div>
                    </div>
                    <div v-if="parsedResult.length === 0" class="text-center py-10 text-green-400">
                      <CheckCircle class="w-12 h-12 mx-auto mb-2 opacity-50" />
                      <p>No Business Logic Vulnerabilities Found</p>
                      <button 
                        v-if="currentRawResult && currentRawResult.length > 50"
                        @click="repairJSON(selectedTask.id, 'logic')"
                        :disabled="isRepairing"
                        class="mt-4 px-3 py-1.5 bg-slate-800 hover:bg-slate-700 text-slate-400 text-xs rounded border border-white/5 transition-colors disabled:opacity-50 inline-flex items-center gap-2"
                      >
                        <RefreshCw :class="['w-3 h-3', isRepairing ? 'animate-spin' : '']" />
                        Suspect Parsing Error? Repair JSON
                      </button>
                    </div>
                  </div>
                  <div v-else-if="parsedResult && parsedResult.length > 0 && !isBusinessLogicResult(parsedResult)" class="h-full flex flex-col items-center justify-center text-slate-600">
                    <div class="flex flex-col items-center gap-4 max-w-lg text-center">
                      <Zap class="w-12 h-12 opacity-50 mb-2" />
                      <p class="text-lg text-slate-400">Result Format Mismatch</p>
                      <p class="text-sm">The AI output was parsed as JSON but doesn't match the Business Logic Vulnerability schema.</p>

                      <div class="w-full bg-slate-950 p-4 rounded border border-white/5 text-left">
                        <p class="text-xs uppercase text-slate-500 mb-2 font-bold">Preview of Parsed Data:</p>
                        <pre class="font-mono text-xs text-slate-400 overflow-auto max-h-32">{{ JSON.stringify(parsedResult, null, 2) }}</pre>
                      </div>

                      <button 
                        @click="repairJSON(selectedTask.id, 'logic')"
                        :disabled="isRepairing"
                        class="px-5 py-2.5 bg-cyber-primary/10 hover:bg-cyber-primary/20 text-cyber-primary border border-cyber-primary/30 rounded-lg flex items-center gap-2 transition-all shadow-[0_0_15px_rgba(0,243,255,0.1)]"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'AI Repairing...' : 'Fix Format with AI' }}
                      </button>
                    </div>
                  </div>
                  <div v-else-if="currentRawResult" class="font-mono text-sm text-slate-300 whitespace-pre-wrap p-4 bg-slate-950 rounded">
                    {{ currentRawResult }}
                    <div class="mt-4 pt-4 border-t border-white/10">
                      <button 
                        @click="repairJSON(selectedTask.id, 'logic')"
                        :disabled="isRepairing"
                        class="px-4 py-2 bg-rose-500/10 hover:bg-rose-500/20 text-rose-400 border border-rose-500/30 rounded flex items-center gap-2 transition-all disabled:opacity-50 disabled:cursor-not-allowed"
                      >
                        <RefreshCw :class="['w-4 h-4', isRepairing ? 'animate-spin' : '']" />
                        {{ isRepairing ? 'Repairing JSON...' : 'Repair JSON Format' }}
                      </button>
                    </div>
                  </div>
                  <div v-else class="h-full flex flex-col items-center justify-center text-slate-600">
                    <p>No business logic audit results available.</p>
                  </div>
                </div>
              </div>
            </div>

          </div>
        </main>

        <!-- Upload Modal -->
        <transition name="fade">
          <div v-if="showUploadModal" class="fixed inset-0 z-50 flex items-center justify-center p-4">
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
                <p class="text-slate-400">{{ t('upload.subtitle') }}</p>
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



