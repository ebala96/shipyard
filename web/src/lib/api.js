import axios from 'axios'

const BASE = import.meta.env.VITE_API_URL || 'http://localhost:8888'
const client = axios.create({ baseURL: BASE })

// ── Services ──────────────────────────────────────────────────────────────────
export const listServices    = () => client.get('/api/v1/services').then(r => r.data)
export const getService      = (name) => client.get(`/api/v1/services/${name}`).then(r => r.data)
export const onboardGithub   = (url, branch = '', subdir = '') =>
  client.post('/api/v1/services/github', { url, branch, subdir }).then(r => r.data)

export const cancelOnboard   = (sessionID) =>
  client.delete(`/api/v1/services/github/progress/${sessionID}`).then(r => r.data)

// Returns an EventSource for onboard progress streaming.
export const streamOnboardProgress = (sessionID, onStep, onDone, onError, onCancelled) => {
  const es = new EventSource(`${BASE}/api/v1/services/github/progress/${sessionID}`)
  es.addEventListener('step',      (e) => onStep(JSON.parse(e.data)))
  es.addEventListener('done',      (e) => { onDone(JSON.parse(e.data)); es.close() })
  es.addEventListener('error',     (e) => { onError(JSON.parse(e.data)); es.close() })
  es.addEventListener('cancelled', (e) => { onCancelled?.(); es.close() })
  return es
}
export const onboardZip      = (formData) =>
  client.post('/api/v1/services/zip', formData, { headers: { 'Content-Type': 'multipart/form-data' } }).then(r => r.data)
export const deleteService   = (name) => client.delete(`/api/v1/services/${name}`).then(r => r.data)
export const scanServiceFiles = (name) => client.get(`/api/v1/services/${name}/files`).then(r => r.data)

// ── Deploy ────────────────────────────────────────────────────────────────────
// platform: 'docker' | 'compose' | 'kubernetes' | 'nomad' | 'podman' | ''
// file: specific file to use, '' = auto-detect
export const deployService   = (name, platform = '', file = '', stackName = '') =>
  client.post(`/api/v1/services/${name}/deploy`, { platform, file, stackName }).then(r => r.data)
export const redeployService = (name, platform = '', file = '', stackName = '') =>
  client.post(`/api/v1/services/${name}/redeploy`, { platform, file, stackName }).then(r => r.data)

// ── IDE ───────────────────────────────────────────────────────────────────────
export const spawnIDE        = (name) => client.post(`/api/v1/ide/${name}`).then(r => r.data)
export const stopIDE         = (name) => client.delete(`/api/v1/ide/${name}`).then(r => r.data)
export const listIDEs        = () => client.get('/api/v1/ide').then(r => r.data)

// ── Containers ────────────────────────────────────────────────────────────────
export const listContainers     = () => client.get('/api/v1/containers').then(r => r.data)
export const getContainerStats  = () => client.get('/api/v1/containers/stats').then(r => r.data)
export const inspectContainer   = (id) => client.get(`/api/v1/containers/${id}/inspect`).then(r => r.data)

export const startContainer   = (id) => client.post(`/api/v1/containers/${id}/start`).then(r => r.data)
export const stopContainer    = (id, service = '', mode = '') =>
  client.post(`/api/v1/containers/${id}/stop?service=${service}&mode=${mode}`).then(r => r.data)
export const restartContainer = (id) => client.post(`/api/v1/containers/${id}/restart`).then(r => r.data)
export const removeContainer  = (id, force = false, service = '', mode = '') =>
  client.delete(`/api/v1/containers/${id}?force=${force}&service=${service}&mode=${mode}`).then(r => r.data)
export const getContainerStatus = (id) => client.get(`/api/v1/containers/${id}/status`).then(r => r.data)
export const execInContainer  = (id, cmd) => client.post(`/api/v1/containers/${id}/exec`, { cmd }).then(r => r.data)

// ── Logs ──────────────────────────────────────────────────────────────────────
export const fetchLogs = (id, tail = 100) =>
  client.get(`/api/v1/containers/${id}/logs/fetch?tail=${tail}`).then(r => r.data)
export const streamLogs = (id, tail = 50, onLine, onError) => {
  const es = new EventSource(`${BASE}/api/v1/containers/${id}/logs?tail=${tail}`)
  es.addEventListener('log',   (e) => onLine(JSON.parse(e.data)))
  es.addEventListener('error', (e) => onError?.(e))
  es.addEventListener('close', () => es.close())
  return es
}

// ── Proxy ─────────────────────────────────────────────────────────────────────
export const getProxyRoutes = () => client.get('/api/v1/proxy/routes').then(r => r.data)

// ── Manifest ──────────────────────────────────────────────────────────────────
export const getManifest    = (name) => client.get(`/api/v1/services/${name}/manifest`).then(r => r.data)
export const saveManifest   = (name, content) => client.put(`/api/v1/services/${name}/manifest`, { content }).then(r => r.data)

// ── Catalog ───────────────────────────────────────────────────────────────────
export const listBlueprints      = () => client.get('/api/v1/catalog').then(r => r.data)
export const getBlueprint        = (name) => client.get(`/api/v1/catalog/${name}`).then(r => r.data)
export const deleteBlueprint     = (name) => client.delete(`/api/v1/catalog/${name}`).then(r => r.data)
export const getSizeProfiles     = () => client.get('/api/v1/catalog/sizes').then(r => r.data)
export const instantiateBlueprint = (name, params) =>
  client.post(`/api/v1/catalog/${name}/instantiate`, params).then(r => r.data)
export const listStacks     = () => client.get('/api/v1/stacks').then(r => r.data)
export const getStack       = (name) => client.get(`/api/v1/stacks/${name}`).then(r => r.data)
export const getStackLedger = (name) => client.get(`/api/v1/stacks/${name}/ledger`).then(r => r.data)
export const stackStop      = (name) => client.post(`/api/v1/stacks/${name}/stop`).then(r => r.data)
export const stackStart     = (name) => client.post(`/api/v1/stacks/${name}/start`).then(r => r.data)
export const stackRestart   = (name) => client.post(`/api/v1/stacks/${name}/restart`).then(r => r.data)
export const stackDown      = (name) => client.post(`/api/v1/stacks/${name}/down`).then(r => r.data)
export const stackDestroy   = (name) => client.delete(`/api/v1/stacks/${name}`).then(r => r.data)
export const stackRollback  = (name, version = '') =>
  client.post(`/api/v1/stacks/${name}/rollback${version ? `?version=${version}` : ''}`).then(r => r.data)

// ── VNC ───────────────────────────────────────────────────────────────────────
export const getVNC    = (name) => client.get(`/api/v1/services/${name}/vnc`).then(r => r.data)
export const startVNC  = (name) => client.post(`/api/v1/services/${name}/vnc/start`).then(r => r.data)
export const stopVNC   = (name) => client.post(`/api/v1/services/${name}/vnc/stop`).then(r => r.data)
export const listVNC   = () => client.get('/api/v1/vnc').then(r => r.data)
export const shareVNC  = (name) => client.post(`/api/v1/services/${name}/vnc/share`).then(r => r.data)
export const listRelay = () => client.get('/api/v1/relay').then(r => r.data)
export const revokeRelay = (token) => client.delete(`/api/v1/relay/${token}`).then(r => r.data)

// ── Shiplink ──────────────────────────────────────────────────────────────────
export const getShiplinkServices = () => client.get('/api/v1/shiplink/services').then(r => r.data)
export const resolveService      = (name) => client.get(`/api/v1/shiplink/resolve/${name}`).then(r => r.data)
export const setCanary  = (name, body) => client.post(`/api/v1/shiplink/canary/${name}`, body).then(r => r.data)
export const clearCanary = (name) => client.delete(`/api/v1/shiplink/canary/${name}`).then(r => r.data)
