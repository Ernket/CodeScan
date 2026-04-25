const severityOrder = ['CRITICAL', 'HIGH', 'MEDIUM', 'LOW', 'INFO', 'UNKNOWN']

export function parseResultArray(raw) {
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
  } catch (error) {
    console.error('JSON Parse Error:', error)
    return null
  }
}

export function normalizeSeverity(value) {
  switch (String(value || '').trim().toUpperCase()) {
    case 'CRITICAL':
      return 'CRITICAL'
    case 'HIGH':
      return 'HIGH'
    case 'MEDIUM':
      return 'MEDIUM'
    case 'LOW':
      return 'LOW'
    case 'INFO':
      return 'INFO'
    default:
      return 'UNKNOWN'
  }
}

export function effectiveSeverity(item) {
  return normalizeSeverity(item?.reviewed_severity || item?.severity)
}

export function verificationStatus(item) {
  const value = String(item?.verification_status || '').trim().toLowerCase()
  if (value === 'confirmed' || value === 'uncertain' || value === 'rejected') return value
  return 'unreviewed'
}

export function splitFindings(items = []) {
  const active = []
  const rejected = []

  for (const item of Array.isArray(items) ? items : []) {
    if (verificationStatus(item) === 'rejected') {
      rejected.push(item)
      continue
    }
    active.push(item)
  }

  return { active, rejected }
}

export function buildSeverityBreakdown(items = []) {
  const counts = activeFindings(items).reduce((acc, item) => {
    const severity = effectiveSeverity(item)
    acc[severity] = (acc[severity] || 0) + 1
    return acc
  }, {})

  return severityOrder
    .filter((label) => counts[label])
    .map((label) => ({ label, count: counts[label] }))
}

export function activeFindings(items = []) {
  return splitFindings(items).active
}

export function rejectedFindings(items = []) {
  return splitFindings(items).rejected
}

export function formatResultField(value) {
  if (value === null || value === undefined || value === '') return ''
  if (Array.isArray(value)) return value.map((item) => formatResultField(item)).filter(Boolean).join('\n')
  if (typeof value === 'object') return JSON.stringify(value, null, 2)
  return String(value)
}

export function formatTriggerSignature(trigger) {
  if (!trigger) return ''
  const method = formatResultField(trigger.method)
  const path = formatResultField(trigger.path)
  return `${method} ${path}`.trim()
}

export function formatTriggerLabel(trigger, fallback = '') {
  return formatTriggerSignature(trigger) || fallback
}

export function locationFile(location) {
  return String(location?.file || '').trim()
}

export function formatLocation(location, fallback = '') {
  const file = locationFile(location)
  const line = String(location?.line || '').trim()
  if (!file) return fallback
  return line ? `${file}:${line}` : file
}

export function formatComponentLabel(item, fallback = '') {
  const name = formatResultField(item?.component_name)
  const version = formatResultField(item?.component_version)
  if (!name) return fallback
  return version ? `${name} @ ${version}` : name
}

export function hasStagePayload(stage) {
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

export function isStageExportable(stage) {
  if (!stage || stage.status !== 'completed') return false
  return parseResultArray(stage.output_json || stage.result) !== null || hasStagePayload(stage)
}

export function countRouteInventory(task) {
  const routes = parseResultArray(task?.output_json || task?.result)
  if (!Array.isArray(routes)) return 0
  return routes.filter((item) => item && typeof item === 'object' && item.method && item.path).length
}
