import { useEffect, useState } from 'react'
import { Button, Badge, Card, Spinner, PageHeader } from '../components/ui'
import useStore from '../store/shipyard'

const API = import.meta.env.VITE_API_URL || 'http://localhost:8888'

const api = {
  list:   (q, cat) => {
    const params = new URLSearchParams()
    if (q)   params.set('q', q)
    if (cat) params.set('category', cat)
    return fetch(`${API}/api/v1/templates?${params}`).then(r => r.json())
  },
  deploy: (id, body) => fetch(`${API}/api/v1/templates/${id}/deploy`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).then(r => r.json()),
}

const categoryMeta = {
  database:   { label: 'Database',    icon: '🗄️' },
  monitoring: { label: 'Monitoring',  icon: '📊' },
  storage:    { label: 'Storage',     icon: '💾' },
  devtools:   { label: 'Dev tools',   icon: '🛠️' },
  messaging:  { label: 'Messaging',   icon: '📨' },
  security:   { label: 'Security',    icon: '🔒' },
  web:        { label: 'Web',         icon: '🌐' },
}

const profileColors = {
  eco:         'text-teal-400 bg-teal-900/30 border-teal-800',
  balanced:    'text-indigo-400 bg-indigo-900/30 border-indigo-800',
  performance: 'text-amber-400 bg-amber-900/30 border-amber-800',
  max:         'text-red-400 bg-red-900/30 border-red-800',
}

export default function Templates() {
  const [templates,  setTemplates]  = useState([])
  const [grouped,    setGrouped]    = useState({})
  const [loading,    setLoading]    = useState(true)
  const [search,     setSearch]     = useState('')
  const [activecat,  setActiveCat]  = useState('')
  const [deploying,  setDeploying]  = useState(null) // template ID being deployed
  const { syncContainers } = useStore()

  const load = async () => {
    setLoading(true)
    try {
      const data = await api.list(search, activecat)
      setTemplates(data.templates || [])
      setGrouped(data.grouped || {})
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { load() }, [search, activecat])

  const handleDeploy = async (template, params = {}) => {
    setDeploying(template.id)
    try {
      const res = await api.deploy(template.id, { parameters: params })
      if (res.error) throw new Error(res.error)
      await syncContainers()
      return res
    } finally {
      setDeploying(null)
    }
  }

  const categories = Object.keys(categoryMeta)

  return (
    <div>
      <PageHeader
        title="Templates"
        subtitle="One-click deployment of popular self-hosted services"
      />

      {/* Search + category filter */}
      <div className="flex gap-3 mb-6">
        <input
          className="flex-1 bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-indigo-600 placeholder-gray-600"
          placeholder="Search templates..."
          value={search}
          onChange={e => setSearch(e.target.value)}
        />
        <div className="flex gap-1.5 flex-wrap">
          <button
            onClick={() => setActiveCat('')}
            className={`px-3 py-1.5 rounded-lg text-xs border transition-colors ${
              activecat === ''
                ? 'bg-indigo-900/40 border-indigo-700 text-indigo-300'
                : 'bg-gray-800 border-gray-700 text-gray-400 hover:border-gray-600'
            }`}
          >
            All
          </button>
          {categories.map(cat => {
            const meta = categoryMeta[cat]
            return (
              <button
                key={cat}
                onClick={() => setActiveCat(activecat === cat ? '' : cat)}
                className={`px-3 py-1.5 rounded-lg text-xs border transition-colors ${
                  activecat === cat
                    ? 'bg-indigo-900/40 border-indigo-700 text-indigo-300'
                    : 'bg-gray-800 border-gray-700 text-gray-400 hover:border-gray-600'
                }`}
              >
                {meta.icon} {meta.label}
              </button>
            )
          })}
        </div>
      </div>

      {loading && <div className="flex justify-center py-12"><Spinner /></div>}

      {!loading && templates.length === 0 && (
        <div className="text-center py-16 text-gray-600">
          <p className="text-4xl mb-3">📭</p>
          <p className="text-sm">No templates match your search</p>
        </div>
      )}

      {/* Grouped by category */}
      {!loading && !activecat && !search && Object.keys(grouped).map(cat => {
        const tmplList = grouped[cat] || []
        const meta = categoryMeta[cat] || { label: cat, icon: '📦' }
        return (
          <div key={cat} className="mb-8">
            <div className="flex items-center gap-2 mb-3">
              <span className="text-lg">{meta.icon}</span>
              <h2 className="text-gray-300 text-sm font-medium">{meta.label}</h2>
              <span className="text-gray-700 text-xs">{tmplList.length}</span>
            </div>
            <div className="grid grid-cols-1 gap-3">
              {tmplList.map(t => (
                <TemplateCard
                  key={t.id}
                  template={t}
                  deploying={deploying === t.id}
                  onDeploy={handleDeploy}
                />
              ))}
            </div>
          </div>
        )
      })}

      {/* Flat list for search/filter */}
      {!loading && (activecat || search) && (
        <div className="grid grid-cols-1 gap-3">
          {templates.map(t => (
            <TemplateCard
              key={t.id}
              template={t}
              deploying={deploying === t.id}
              onDeploy={handleDeploy}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// ── Template card ─────────────────────────────────────────────────────────────

function TemplateCard({ template: t, deploying, onDeploy }) {
  const [expanded, setExpanded] = useState(false)
  const [params,   setParams]   = useState({})
  const [result,   setResult]   = useState(null)
  const [error,    setError]    = useState(null)

  const profileColor = profileColors[t.defaultProfile] || profileColors.balanced
  const hasParams = t.parameters && t.parameters.length > 0

  const handleDeploy = async () => {
    setError(null)
    setResult(null)
    try {
      const res = await onDeploy(t, params)
      setResult(res)
    } catch (err) {
      setError(err.message)
    }
  }

  return (
    <Card>
      <div className="flex items-start gap-4">
        {/* Icon */}
        <div className="text-2xl shrink-0 w-10 text-center">{t.icon}</div>

        {/* Info */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap mb-1">
            <h3 className="text-gray-100 font-medium text-sm">{t.name}</h3>
            <span className={`px-2 py-0.5 rounded border text-xs font-medium ${profileColor}`}>
              {t.defaultProfile}
            </span>
            {(t.tags || []).slice(0, 3).map(tag => (
              <Badge key={tag} color="gray">{tag}</Badge>
            ))}
          </div>
          <p className="text-gray-400 text-xs mb-3">{t.description}</p>

          {/* Success result */}
          {result && (
            <div className="bg-teal-900/30 border border-teal-800 text-teal-300 rounded-lg p-3 text-xs mb-3">
              <p className="font-medium mb-1">✅ {result.message}</p>
              {result.ports && Object.entries(result.ports).map(([name, port]) => (
                <p key={name} className="font-mono opacity-70">
                  {name} → <a href={`http://localhost:${port}`} target="_blank" rel="noreferrer"
                    className="underline hover:text-teal-200">localhost:{port}</a>
                </p>
              ))}
            </div>
          )}

          {/* Error */}
          {error && (
            <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-xs mb-3">
              {error}
            </div>
          )}

          {/* Parameters */}
          {expanded && hasParams && (
            <div className="space-y-2 mb-3">
              {t.parameters.map(p => (
                <div key={p.name}>
                  <label className="text-gray-500 text-xs block mb-1">
                    {p.label}{p.required && <span className="text-red-500 ml-1">*</span>}
                  </label>
                  <input
                    type={p.secret ? 'password' : 'text'}
                    className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-1.5 text-xs text-gray-200 focus:outline-none focus:border-indigo-600"
                    placeholder={p.default}
                    value={params[p.name] ?? p.default}
                    onChange={e => setParams(prev => ({ ...prev, [p.name]: e.target.value }))}
                  />
                </div>
              ))}
            </div>
          )}

          {/* Actions */}
          <div className="flex items-center gap-2">
            <Button
              size="sm"
              onClick={handleDeploy}
              disabled={deploying}
            >
              {deploying
                ? <><Spinner size="sm" /><span className="ml-2">Deploying...</span></>
                : '▶ Deploy'
              }
            </Button>

            {hasParams && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => setExpanded(e => !e)}
                className="text-gray-500 text-xs"
              >
                {expanded ? 'Hide config' : '⚙ Configure'}
              </Button>
            )}

            {t.docURL && (
              <a
                href={t.docURL}
                target="_blank"
                rel="noreferrer"
                className="text-gray-600 hover:text-gray-400 text-xs ml-auto"
              >
                Docs ↗
              </a>
            )}
          </div>
        </div>
      </div>
    </Card>
  )
}
