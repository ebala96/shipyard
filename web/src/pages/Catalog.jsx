import { useEffect, useState } from 'react'
import { Button, Badge, Card, Spinner, PageHeader, EmptyState } from '../components/ui'
import useStore from '../store/shipyard'

const API = import.meta.env.VITE_API_URL || 'http://localhost:8888'

const api = {
  list:   () => fetch(`${API}/api/v1/catalog`).then(r => r.json()),
  get:    (name) => fetch(`${API}/api/v1/catalog/${name}`).then(r => r.json()),
  del:    (name) => fetch(`${API}/api/v1/catalog/${name}`, { method: 'DELETE' }).then(r => r.json()),
  import: (body) => fetch(`${API}/api/v1/catalog/import`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
  }).then(r => r.json()),
  save:   (name) => fetch(`${API}/api/v1/catalog/save/${name}`, { method: 'POST' }).then(r => r.json()),
  deploy: (name, body) => fetch(`${API}/api/v1/catalog/${name}/deploy`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
  }).then(r => r.json()),
}

const profileColors = {
  eco:         { bg: 'bg-teal-900/30',   border: 'border-teal-800',   text: 'text-teal-300',   dot: 'bg-teal-400' },
  balanced:    { bg: 'bg-indigo-900/30', border: 'border-indigo-800', text: 'text-indigo-300', dot: 'bg-indigo-400' },
  performance: { bg: 'bg-amber-900/30',  border: 'border-amber-800',  text: 'text-amber-300',  dot: 'bg-amber-400' },
  max:         { bg: 'bg-red-900/30',    border: 'border-red-800',    text: 'text-red-300',    dot: 'bg-red-400' },
}

const importModeLabel = {
  ai:      { label: 'AI', color: 'indigo' },
  repo:    { label: 'Repo', color: 'teal' },
  catalog: { label: 'Saved', color: 'gray' },
  manual:  { label: 'Manual', color: 'gray' },
}

export default function Catalog() {
  const [blueprints, setBlueprints] = useState([])
  const [profiles,   setProfiles]   = useState([])
  const [loading,    setLoading]    = useState(true)
  const [selected,   setSelected]   = useState(null)
  const [showImport, setShowImport] = useState(false)
  const { syncContainers } = useStore()

  const load = async () => {
    setLoading(true)
    try {
      const data = await api.list()
      setBlueprints(data.blueprints || [])
      setProfiles(data.profiles || [])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [])

  const handleDelete = async (name) => {
    if (!confirm(`Remove blueprint "${name}" from catalog?`)) return
    await api.del(name)
    load()
  }

  return (
    <div>
      <PageHeader
        title="Catalog"
        subtitle="Reusable service blueprints — save, share and deploy with a power profile"
        action={
          <div className="flex gap-2">
            <Button variant="secondary" onClick={() => setShowImport(true)}>
              + AI Import
            </Button>
          </div>
        }
      />

      {loading && <div className="flex justify-center py-12"><Spinner /></div>}

      {!loading && blueprints.length === 0 && (
        <EmptyState
          title="No blueprints yet"
          subtitle='Save a service from the Services tab, or import a GitHub repo with AI'
        />
      )}

      {/* Blueprint grid */}
      <div className="grid grid-cols-1 gap-4">
        {blueprints.map(bp => {
          const mode = importModeLabel[bp.importMode] || { label: bp.importMode, color: 'gray' }
          return (
            <Card key={bp.name}>
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-1 flex-wrap">
                    <h2 className="text-gray-100 font-medium">{bp.name}</h2>
                    <Badge color={mode.color}>{mode.label}</Badge>
                    {(bp.tags || []).map(t => <Badge key={t} color="gray">{t}</Badge>)}
                  </div>
                  {bp.description && (
                    <p className="text-gray-400 text-sm mb-3">{bp.description}</p>
                  )}
                  {bp.gitURL && (
                    <p className="text-gray-600 text-xs mb-3 font-mono truncate">{bp.gitURL}</p>
                  )}

                  {/* Power profile picker */}
                  <div className="flex flex-wrap gap-2 mt-2">
                    {profiles.map(p => {
                      const c = profileColors[p.name] || profileColors.balanced
                      return (
                        <button
                          key={p.name}
                          onClick={() => setSelected({ bp, profile: p.name })}
                          className={`flex items-center gap-1.5 px-3 py-1.5 rounded-lg border text-xs font-medium
                            transition-all hover:opacity-100 opacity-70
                            ${c.bg} ${c.border} ${c.text}`}
                          title={p.description}
                        >
                          <span className={`w-1.5 h-1.5 rounded-full ${c.dot}`} />
                          {p.label}
                          <span className="text-gray-500 ml-1">
                            {p.cpuMillis >= 1000 ? `${p.cpuMillis/1000}` : `0.${p.cpuMillis/100}`}c / {p.memoryMB >= 1024 ? `${p.memoryMB/1024}GB` : `${p.memoryMB}MB`}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </div>

                <button
                  onClick={() => handleDelete(bp.name)}
                  className="text-gray-600 hover:text-red-400 text-sm shrink-0 transition-colors"
                >✕</button>
              </div>
            </Card>
          )
        })}
      </div>

      {/* Deploy modal */}
      {selected && (
        <DeployModal
          blueprint={selected.bp}
          profile={selected.profile}
          profiles={profiles}
          onClose={() => setSelected(null)}
          onDeploy={async (name, profile, params) => {
            const res = await api.deploy(name, { profile, parameters: params })
            if (res.error) throw new Error(res.error)
            // Sync so Monitor tab sees the new container immediately.
            await syncContainers()
            setSelected(null)
            return res
          }}
        />
      )}

      {/* Import modal */}
      {showImport && (
        <ImportModal
          onClose={() => setShowImport(false)}
          onImport={async (gitURL, mode) => {
            const res = await api.import({ gitURL, mode })
            if (res.error) throw new Error(res.error)
            setShowImport(false)
            load()
          }}
        />
      )}
    </div>
  )
}

// ── Deploy modal ──────────────────────────────────────────────────────────────

function DeployModal({ blueprint, profile: initialProfile, profiles, onClose, onDeploy }) {
  const [profile,   setProfile]   = useState(initialProfile)
  const [deploying, setDeploying] = useState(false)
  const [result,    setResult]    = useState(null)
  const [error,     setError]     = useState(null)

  const selectedProfile = profiles.find(p => p.name === profile) || profiles[1]

  const handle = async () => {
    setDeploying(true)
    setError(null)
    try {
      const res = await onDeploy(blueprint.name, profile, {})
      setResult(res)
    } catch (err) {
      setError(err.message)
    } finally {
      setDeploying(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'rgba(0,0,0,0.7)' }}
      onClick={e => { if (e.target === e.currentTarget && !result) onClose() }}>
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-lg mx-4">

        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
          <div>
            <p className="text-gray-100 font-medium text-sm">Deploy blueprint</p>
            <p className="text-gray-500 text-xs mt-0.5">{blueprint.name}</p>
          </div>
          <button onClick={onClose} className="text-gray-500 hover:text-gray-200 text-sm">✕</button>
        </div>

        {result ? (
          <div className="p-5 space-y-3">
            <div className="bg-teal-900/30 border border-teal-800 text-teal-300 rounded-lg p-4 text-sm">
              <p className="font-medium mb-1">Deployed successfully</p>
              <p className="text-xs opacity-70">{result.message}</p>
              {result.container && (
                <p className="text-xs font-mono mt-2 opacity-60">{result.container.containerID?.slice(0, 12)}</p>
              )}
            </div>
            <p className="text-gray-500 text-xs">Check the Monitor tab to see the running container.</p>
            <div className="flex justify-end">
              <Button onClick={onClose}>Done</Button>
            </div>
          </div>
        ) : (
          <>
            <div className="p-5 space-y-4">
              <div>
                <p className="text-gray-400 text-xs mb-2">Power profile</p>
                <div className="grid grid-cols-2 gap-2">
                  {profiles.map(p => {
                    const pc = profileColors[p.name] || profileColors.balanced
                    const active = profile === p.name
                    return (
                      <button
                        key={p.name}
                        onClick={() => setProfile(p.name)}
                        className={`p-3 rounded-lg border text-left transition-all ${
                          active
                            ? `${pc.bg} ${pc.border} ${pc.text}`
                            : 'bg-gray-800 border-gray-700 text-gray-400 hover:border-gray-600'
                        }`}
                      >
                        <div className="flex items-center gap-1.5 mb-1">
                          <span className={`w-1.5 h-1.5 rounded-full ${active ? pc.dot : 'bg-gray-600'}`} />
                          <span className="text-xs font-medium">{p.label}</span>
                        </div>
                        <p className="text-xs opacity-60">
                          {p.cpuMillis >= 1000 ? `${p.cpuMillis/1000}` : `0.${p.cpuMillis/100}`} core{p.cpuMillis >= 2000 ? 's' : ''} · {p.memoryMB >= 1024 ? `${p.memoryMB/1024}GB` : `${p.memoryMB}MB`} RAM · {p.replicas} replica{p.replicas > 1 ? 's' : ''}
                        </p>
                      </button>
                    )
                  })}
                </div>
              </div>
              {selectedProfile && (
                <p className="text-gray-500 text-xs">{selectedProfile.description}</p>
              )}
              {error && (
                <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-xs">{error}</div>
              )}
            </div>
            <div className="px-5 py-4 border-t border-gray-800 flex justify-end gap-3">
              <Button variant="ghost" onClick={onClose} className="text-gray-500">Cancel</Button>
              <Button onClick={handle} disabled={deploying}>
                {deploying ? <><Spinner size="sm" /><span className="ml-2">Deploying...</span></> : `Deploy with ${selectedProfile?.label || profile}`}
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

// ── Import modal ──────────────────────────────────────────────────────────────

function ImportModal({ onClose, onImport }) {
  const [gitURL,     setGitURL]     = useState('')
  const [mode,       setMode]       = useState('ai')
  const [importing,  setImporting]  = useState(false)
  const [error,      setError]      = useState(null)

  const handle = async () => {
    if (!gitURL.trim()) { setError('GitHub URL is required'); return }
    setImporting(true)
    setError(null)
    try {
      await onImport(gitURL.trim(), mode)
    } catch (err) {
      setError(err.message)
      setImporting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'rgba(0,0,0,0.7)' }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="bg-gray-900 border border-gray-700 rounded-xl w-full max-w-md mx-4">

        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
          <p className="text-gray-100 font-medium text-sm">Import from GitHub</p>
          <button onClick={onClose} className="text-gray-500 hover:text-gray-200 text-sm">✕</button>
        </div>

        <div className="p-5 space-y-4">
          <div>
            <label className="text-gray-400 text-xs block mb-1.5">GitHub URL</label>
            <input
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-gray-200 text-sm focus:outline-none focus:border-indigo-600"
              placeholder="https://github.com/user/repo"
              value={gitURL}
              onChange={e => setGitURL(e.target.value)}
            />
          </div>

          <div>
            <label className="text-gray-400 text-xs block mb-1.5">Import mode</label>
            <div className="flex gap-2">
              {[
                { value: 'ai',   label: 'AI analysis',     desc: 'Claude reads the repo and generates a config' },
                { value: 'repo', label: 'Format detection', desc: 'Detect engine from Dockerfile / compose.yml' },
              ].map(opt => (
                <button
                  key={opt.value}
                  onClick={() => setMode(opt.value)}
                  className={`flex-1 p-3 rounded-lg border text-left text-xs transition-all ${
                    mode === opt.value
                      ? 'bg-indigo-900/40 border-indigo-700 text-indigo-300'
                      : 'bg-gray-800 border-gray-700 text-gray-400 hover:border-gray-600'
                  }`}
                >
                  <p className="font-medium mb-0.5">{opt.label}</p>
                  <p className="opacity-60">{opt.desc}</p>
                </button>
              ))}
            </div>
          </div>

          {error && (
            <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-xs">
              {error}
            </div>
          )}
        </div>

        <div className="px-5 py-4 border-t border-gray-800 flex justify-end gap-3">
          <Button variant="ghost" onClick={onClose} className="text-gray-500">Cancel</Button>
          <Button onClick={handle} disabled={importing || !gitURL.trim()}>
            {importing ? <><Spinner size="sm" /><span className="ml-2">Importing...</span></> : 'Import'}
          </Button>
        </div>
      </div>
    </div>
  )
}
