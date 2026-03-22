import { useEffect, useState, useRef, useCallback } from 'react'
import useStore from '../store/shipyard'
import { onboardGithub, onboardZip, spawnIDE, stopIDE, streamOnboardProgress, cancelOnboard, getManifest, saveManifest } from '../lib/api'

const API = import.meta.env.VITE_API_URL || 'http://localhost:8888'
const saveToCatalog = (name) =>
  fetch(`${API}/api/v1/catalog/save/${name}`, { method: 'POST' }).then(r => r.json())
import { Button, Badge, Card, Spinner, PageHeader, EmptyState } from '../components/ui'

const engineColor = {
  docker: 'blue', compose: 'teal', kubernetes: 'indigo',
  k3s: 'indigo', nomad: 'amber', podman: 'gray'
}

export default function Services() {
  const { services, servicesLoading, servicesError, fetchServices, removeService } = useStore()
  const [showOnboard, setShowOnboard] = useState(false)
  const [ideState, setIdeState] = useState({}) // name → { loading, url, error }
  const [manifestEditor, setManifestEditor] = useState(null) // { name, content, loading, saving, error }

  useEffect(() => { fetchServices() }, [])

  const handleSpawnIDE = async (name) => {
    setIdeState(s => ({ ...s, [name]: { loading: true } }))
    try {
      const result = await spawnIDE(name)
      const url = result.instance?.proxyURL || result.instance?.directURL
      setIdeState(s => ({ ...s, [name]: { loading: false, url } }))
      window.open(url, '_blank')
    } catch (err) {
      setIdeState(s => ({ ...s, [name]: { loading: false, error: err.response?.data?.error || err.message } }))
    }
  }

  const handleStopIDE = async (name) => {
    try {
      await stopIDE(name)
      setIdeState(s => ({ ...s, [name]: {} }))
    } catch {}
  }

  const handleSaveToCatalog = async (name) => {
    try {
      await saveToCatalog(name)
      alert(`"${name}" saved to catalog`)
    } catch (err) {
      alert(`Failed: ${err.message}`)
    }
  }

  const handleOpenManifest = async (name) => {
    setManifestEditor({ name, content: '', loading: true, saving: false, error: null })
    try {
      const data = await getManifest(name)
      setManifestEditor({ name, content: data.content, loading: false, saving: false, error: null })
    } catch (err) {
      setManifestEditor(s => ({ ...s, loading: false, error: err.response?.data?.error || err.message }))
    }
  }

  const handleSaveManifest = async () => {
    if (!manifestEditor) return
    setManifestEditor(s => ({ ...s, saving: true, error: null }))
    try {
      await saveManifest(manifestEditor.name, manifestEditor.content)
      setManifestEditor(s => ({ ...s, saving: false }))
    } catch (err) {
      setManifestEditor(s => ({ ...s, saving: false, error: err.response?.data?.error || err.message }))
    }
  }

  return (
    <>
      <PageHeader
        title="Services"
        subtitle="Onboarded services — go to Deploy tab to run them"
        action={<Button onClick={() => setShowOnboard(true)}>+ Onboard service</Button>}
      />

      {showOnboard && (
        <OnboardWizard
          onClose={() => setShowOnboard(false)}
          onSuccess={() => { setShowOnboard(false); fetchServices() }}
        />
      )}

      {servicesLoading && <div className="flex justify-center py-12"><Spinner /></div>}

      {servicesError && (
        <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-4 text-sm mb-4">
          {servicesError}
        </div>
      )}

      {!servicesLoading && services.length === 0 && (
        <EmptyState
          title="No services onboarded"
          subtitle="Paste a GitHub URL to onboard any open source project instantly"
          action={<Button onClick={() => setShowOnboard(true)}>Onboard your first service</Button>}
        />
      )}

      <div className="grid gap-4">
        {services.map(svc => {
          const ide = ideState[svc.name] || {}
          return (
            <Card key={svc.name}>
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-1 flex-wrap">
                    <h2 className="text-gray-100 font-medium">{svc.name}</h2>
                    {svc.engine && <Badge color={engineColor[svc.engine] || 'gray'}>{svc.engine}</Badge>}
                    <Badge color="gray">{svc.source}</Badge>
                    {svc.repoURL && (
                      <a href={svc.repoURL} target="_blank" rel="noreferrer"
                        className="text-xs text-indigo-400 hover:text-indigo-300">
                        {svc.repoURL.replace('https://github.com/', '')} ↗
                      </a>
                    )}
                  </div>

                  {svc.description && (
                    <p className="text-gray-400 text-sm mb-2">{svc.description}</p>
                  )}

                  <div className="flex flex-wrap gap-1 mb-3">
                    {(svc.tags || []).map(tag => <Badge key={tag} color="gray">{tag}</Badge>)}
                  </div>

                  {/* IDE controls */}
                  <div className="flex items-center gap-3 flex-wrap">
                    {ide.url ? (
                      <>
                        <a href={ide.url} target="_blank" rel="noreferrer"
                          className="text-xs text-teal-400 hover:text-teal-300 bg-gray-800 border border-teal-800 px-3 py-1.5 rounded-lg font-medium">
                          Open IDE ↗
                        </a>
                        <Button size="sm" variant="ghost" onClick={() => handleStopIDE(svc.name)}
                          className="text-gray-500 hover:text-red-400 text-xs">
                          Stop IDE
                        </Button>
                      </>
                    ) : (
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={ide.loading}
                        onClick={() => handleSpawnIDE(svc.name)}
                      >
                        {ide.loading
                          ? <><Spinner size="sm" /><span className="ml-2">Starting IDE...</span></>
                          : 'Open in IDE'
                        }
                      </Button>
                    )}

                    {ide.error && (
                      <span className="text-red-400 text-xs">{ide.error}</span>
                    )}

                    <Button
                      size="sm" variant="ghost"
                      onClick={() => handleOpenManifest(svc.name)}
                      className="text-gray-500 hover:text-amber-400 text-xs"
                    >
                      Edit config
                    </Button>

                    <Button
                      size="sm" variant="ghost"
                      onClick={() => handleSaveToCatalog(svc.name)}
                      className="text-gray-500 hover:text-indigo-400 text-xs"
                    >
                      Save to catalog
                    </Button>

                    <span className="text-gray-600 text-xs">
                      Go to <span className="text-indigo-400">Deploy</span> tab to run this service
                    </span>
                  </div>
                </div>

                <Button
                  variant="ghost" size="sm"
                  onClick={() => confirm(`Remove ${svc.name}?`) && removeService(svc.name)}
                  className="text-gray-500 hover:text-red-400 shrink-0"
                >✕</Button>
              </div>
            </Card>
          )
        })}
      </div>

    {/* Config editor modal */}
    {manifestEditor && (
      <div className="fixed inset-0 z-50 flex items-center justify-center"
        style={{ background: 'rgba(0,0,0,0.7)' }}
        onClick={(e) => { if (e.target === e.currentTarget) setManifestEditor(null) }}>
        <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-3xl mx-4 flex flex-col"
          style={{ maxHeight: '85vh' }}>
          <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
            <div>
              <p className="text-gray-100 font-medium text-sm">Edit config</p>
              <p className="text-gray-500 text-xs mt-0.5">{manifestEditor.name} / shipfile.yml</p>
            </div>
            <div className="flex items-center gap-3">
              <Button size="sm" variant="ghost"
                className="text-gray-500 hover:text-gray-200"
                onClick={() => setManifestEditor(null)}>Cancel</Button>
              <Button size="sm"
                disabled={manifestEditor.saving || manifestEditor.loading}
                onClick={handleSaveManifest}>
                {manifestEditor.saving
                  ? <><Spinner size="sm" /><span className="ml-2">Saving...</span></>
                  : 'Save'}
              </Button>
            </div>
          </div>
          {manifestEditor.error && (
            <div className="mx-5 mt-3 bg-red-900/30 border border-red-800 text-red-300 rounded-lg px-4 py-2 text-xs">
              {manifestEditor.error}
            </div>
          )}
          <div className="flex-1 overflow-hidden p-4">
            {manifestEditor.loading
              ? <div className="flex justify-center py-12"><Spinner /></div>
              : <textarea
                  className="w-full bg-gray-950 text-gray-200 text-xs font-mono border border-gray-800 rounded-lg p-4 resize-none focus:outline-none focus:border-indigo-700"
                  style={{ minHeight: '480px', width: '100%' }}
                  value={manifestEditor.content}
                  onChange={e => setManifestEditor(s => ({ ...s, content: e.target.value }))}
                  spellCheck={false}
                />
            }
          </div>
          <div className="px-5 py-3 border-t border-gray-800">
            <p className="text-gray-600 text-xs">
              Changes are validated before saving. Previous config backed up as shipfile.yml.bak
            </p>
          </div>
        </div>
      </div>
    )}
  </>
  )
}

// ── Onboard wizard ────────────────────────────────────────────────────────────

function OnboardWizard({ onClose, onSuccess }) {
  const [tab, setTab] = useState('github')
  const [error, setError] = useState(null)

  // Progress state
  const [progress, setProgress] = useState(null) // null = not started
  const [steps, setSteps] = useState([])
  const [sessionID, setSessionID] = useState(null)
  const esRef = useRef(null)

  const [url, setUrl] = useState('')
  const [branch, setBranch] = useState('')
  const [subdir, setSubdir] = useState('')
  const [file, setFile] = useState(null)
  const fileRef = useRef()

  const handleCancel = useCallback(async () => {
    if (esRef.current) { esRef.current.close(); esRef.current = null }
    if (sessionID) {
      try { await cancelOnboard(sessionID) } catch {}
    }
    setProgress(null)
    setSteps([])
    setSessionID(null)
  }, [sessionID])

  const handleGithub = async (e) => {
    e.preventDefault()
    if (!url.trim()) { setError('Please enter a GitHub URL'); return }
    setError(null)
    setSteps([])

    try {
      const result = await onboardGithub(url.trim(), branch.trim(), subdir.trim())
      const sid = result.sessionID
      setSessionID(sid)
      setProgress('running')

      esRef.current = streamOnboardProgress(
        sid,
        (event) => setSteps(prev => {
          const idx = prev.findIndex(s => s.id === event.step.id)
          if (idx >= 0) { const next = [...prev]; next[idx] = event.step; return next }
          return [...prev, event.step]
        }),
        (data) => {
          setProgress('done')
          const parsed = typeof data === 'string' ? JSON.parse(data) : data
          setTimeout(() => { onSuccess(); }, 1000)
        },
        (event) => { setProgress('error'); setError(event.message) },
        () => { setProgress(null) }
      )
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    }
  }

  const handleZip = async (e) => {
    e.preventDefault()
    if (!file) { setError('Please select a zip file'); return }
    setLoading(true); setError(null)
    const formData = new FormData()
    formData.append('file', file)
    try {
      await onboardZip(formData)
      onSuccess()
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center z-50 p-4">
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-lg">
        <div className="flex items-center justify-between px-6 py-4 border-b border-gray-800">
          <h2 className="text-gray-100 font-semibold">Onboard a service</h2>
          <button onClick={onClose} className="text-gray-500 hover:text-gray-300 text-lg">✕</button>
        </div>

        <div className="flex border-b border-gray-800">
          {[['github', 'GitHub URL'], ['zip', 'Zip upload']].map(([id, label]) => (
            <button key={id} onClick={() => { setTab(id); setError(null) }}
              className={`flex-1 py-3 text-sm font-medium transition-colors ${
                tab === id ? 'text-indigo-400 border-b-2 border-indigo-500' : 'text-gray-500 hover:text-gray-300'
              }`}>{label}</button>
          ))}
        </div>

        <div className="p-6">
          {tab === 'github' && (
            <form onSubmit={handleGithub} className="space-y-4">
              <div>
                <label className="block text-gray-400 text-sm mb-1.5">GitHub URL <span className="text-red-400">*</span></label>
                <input value={url} onChange={e => setUrl(e.target.value)}
                  placeholder="https://github.com/owner/repo"
                  className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2.5 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:border-indigo-500" />
                <p className="text-gray-600 text-xs mt-1">Any public repo with a Dockerfile or docker-compose.yml</p>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-gray-400 text-sm mb-1.5">Branch <span className="text-gray-600">(optional)</span></label>
                  <input value={branch} onChange={e => setBranch(e.target.value)} placeholder="main"
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:border-indigo-500" />
                </div>
                <div>
                  <label className="block text-gray-400 text-sm mb-1.5">Subdirectory <span className="text-gray-600">(optional)</span></label>
                  <input value={subdir} onChange={e => setSubdir(e.target.value)} placeholder="services/api"
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:border-indigo-500" />
                </div>
              </div>

              {/* Progress dialog */}
              {progress && (
                <div className="bg-gray-800 border border-gray-700 rounded-lg p-4 space-y-2">
                  {steps.map(step => (
                    <div key={step.id} className="flex items-center gap-3">
                      <div className={"w-4 h-4 rounded-full shrink-0 flex items-center justify-center " + (
                        step.status === 'done'    ? 'bg-green-500' :
                        step.status === 'running' ? 'bg-indigo-500 animate-pulse' :
                        step.status === 'error'   ? 'bg-red-500' : 'bg-gray-600'
                      )}>
                        {step.status === 'done' && <span className="text-white text-xs">✓</span>}
                        {step.status === 'error' && <span className="text-white text-xs">✕</span>}
                      </div>
                      <div className="flex-1 min-w-0">
                        <span className={"text-sm " + (step.status === 'done' ? 'text-gray-300' : step.status === 'running' ? 'text-indigo-300 font-medium' : 'text-gray-500')}>
                          {step.label}
                        </span>
                        {step.detail && (
                          <span className="text-xs text-gray-500 ml-2 truncate">{step.detail}</span>
                        )}
                      </div>
                      {step.status === 'running' && <Spinner size="sm" />}
                    </div>
                  ))}
                  {progress === 'done' && (
                    <p className="text-green-400 text-sm font-medium pt-1">Onboarded successfully!</p>
                  )}
                </div>
              )}

              {error && <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-sm">{error}</div>}

              <div className="flex gap-3 justify-end pt-1">
                {progress === 'running' ? (
                  <>
                    <Button variant="danger" size="sm" onClick={handleCancel}>Cancel onboarding</Button>
                  </>
                ) : (
                  <>
                    <Button variant="ghost" onClick={onClose}>Close</Button>
                    {progress !== 'done' && (
                      <Button type="submit" disabled={!url.trim()}>
                        Onboard from GitHub
                      </Button>
                    )}
                  </>
                )}
              </div>
            </form>
          )}

          {tab === 'zip' && (
            <form onSubmit={handleZip} className="space-y-4">
              <p className="text-gray-400 text-sm">
                Upload a <code className="bg-gray-800 px-1 rounded text-xs">.zip</code> containing your service.
                Must include a <code className="bg-gray-800 px-1 rounded text-xs">Dockerfile</code> at the root.
              </p>
              <div className="border-2 border-dashed border-gray-700 hover:border-indigo-500 rounded-lg p-8 text-center cursor-pointer transition-colors"
                onClick={() => fileRef.current?.click()}>
                <input ref={fileRef} type="file" accept=".zip" className="hidden" onChange={e => setFile(e.target.files[0])} />
                {file ? (
                  <div>
                    <p className="text-indigo-400 font-medium">{file.name}</p>
                    <p className="text-gray-500 text-xs mt-1">{(file.size / 1024).toFixed(1)} KB</p>
                  </div>
                ) : (
                  <div>
                    <p className="text-gray-400 text-sm">Click to select a zip file</p>
                    <p className="text-gray-600 text-xs mt-1">or drag and drop</p>
                  </div>
                )}
              </div>
              {error && <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-sm">{error}</div>}
              <div className="flex gap-3 justify-end">
                <Button variant="ghost" onClick={onClose} disabled={loading}>Cancel</Button>
                <Button type="submit" disabled={loading || !file}>
                  {loading ? <><Spinner size="sm" /><span className="ml-2">Uploading...</span></> : 'Upload and onboard'}
                </Button>
              </div>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}