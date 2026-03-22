import { useEffect, useState, useRef, useCallback } from 'react'
import {
  LineChart, Line, AreaChart, Area,
  XAxis, YAxis, CartesianGrid, Tooltip,
  ResponsiveContainer, Legend
} from 'recharts'
import { Button, Badge, Card, Spinner, PageHeader } from '../components/ui'
import { getContainerStats, fetchLogs, listStacks, getStackLedger } from '../lib/api'
import useStore from '../store/shipyard'

const POLL_MS   = 5000
const MAX_POINTS = 30  // keep last 30 data points per series

// ── Colour palette per service ────────────────────────────────────────────────
const COLORS = [
  '#818cf8', '#34d399', '#fb923c', '#f472b6',
  '#38bdf8', '#a78bfa', '#facc15', '#4ade80',
]
const colorFor = (() => {
  const map = {}
  let i = 0
  return (name) => {
    if (!map[name]) map[name] = COLORS[i++ % COLORS.length]
    return map[name]
  }
})()

// ── Shared tab bar ────────────────────────────────────────────────────────────
function TabBar({ tabs, active, onChange }) {
  return (
    <div className="flex gap-1 border-b border-gray-800 mb-5">
      {tabs.map(t => (
        <button key={t.id} onClick={() => onChange(t.id)}
          className={`px-4 py-2 text-sm transition-colors border-b-2 -mb-px ${
            active === t.id
              ? 'border-indigo-500 text-indigo-300'
              : 'border-transparent text-gray-500 hover:text-gray-300'
          }`}>
          {t.label}
        </button>
      ))}
    </div>
  )
}

// ── Main component ────────────────────────────────────────────────────────────
export default function Observe() {
  const [tab, setTab] = useState('metrics')
  const { containers } = useStore()

  const tabs = [
    { id: 'metrics', label: '📈 Metrics' },
    { id: 'logs',    label: '📋 Logs'    },
    { id: 'events',  label: '🔔 Events'  },
  ]

  return (
    <div>
      <PageHeader title="Observe" subtitle="Live metrics, logs and events for all running services" />
      <TabBar tabs={tabs} active={tab} onChange={setTab} />
      {tab === 'metrics' && <MetricsPanel containers={containers} />}
      {tab === 'logs'    && <LogsPanel containers={containers} />}
      {tab === 'events'  && <EventsPanel />}
    </div>
  )
}

// ── Metrics panel ─────────────────────────────────────────────────────────────
function MetricsPanel({ containers }) {
  const [series, setSeries] = useState({}) // serviceName → [{time, cpu, mem}]
  const [error,  setError]  = useState(null)

  useEffect(() => {
    let alive = true
    const tick = async () => {
      try {
        const data = await getContainerStats()
        const stats = data.stats || []
        if (!alive) return

        const now = new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
        setSeries(prev => {
          const next = { ...prev }
          stats.forEach(s => {
            const key = s.serviceName || s.containerName
            const existing = next[key] || []
            const updated = [...existing, {
              time:    now,
              cpu:     +s.cpuPercent.toFixed(1),
              mem:     +s.memUsageMB.toFixed(0),
              memPct:  +s.memPercent.toFixed(1),
            }]
            next[key] = updated.slice(-MAX_POINTS)
          })
          return next
        })
        setError(null)
      } catch (err) {
        setError(err.message)
      }
    }
    tick()
    const iv = setInterval(tick, POLL_MS)
    return () => { alive = false; clearInterval(iv) }
  }, [])

  const services = Object.keys(series)

  if (error) return (
    <div className="bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-4 text-sm">{error}</div>
  )

  if (services.length === 0) return (
    <div className="text-center py-16 text-gray-600">
      <p className="text-4xl mb-3">📈</p>
      <p className="text-sm">No running containers to measure</p>
    </div>
  )

  return (
    <div className="space-y-6">
      {/* Summary cards */}
      <div className="grid grid-cols-3 gap-3">
        <SummaryCard label="Services"   value={services.length} unit="" color="text-indigo-400" />
        <SummaryCard
          label="Avg CPU"
          value={avgLast(series, 'cpu').toFixed(1)}
          unit="%"
          color={avgLast(series, 'cpu') > 70 ? 'text-red-400' : 'text-green-400'}
        />
        <SummaryCard
          label="Total RAM"
          value={(totalLast(series, 'mem') / 1024).toFixed(2)}
          unit="GB"
          color="text-blue-400"
        />
      </div>

      {/* CPU chart */}
      <Card>
        <p className="text-gray-400 text-xs mb-4 font-medium">CPU usage %</p>
        <ResponsiveContainer width="100%" height={200}>
          <LineChart data={mergeSeriesByTime(series)}>
            <CartesianGrid strokeDasharray="3 3" stroke="#1f2937" />
            <XAxis dataKey="time" tick={{ fill: '#6b7280', fontSize: 11 }} interval="preserveStartEnd" />
            <YAxis domain={[0, 100]} tick={{ fill: '#6b7280', fontSize: 11 }} unit="%" width={40} />
            <Tooltip contentStyle={{ background: '#111827', border: '1px solid #374151', borderRadius: 8 }}
              labelStyle={{ color: '#9ca3af' }} itemStyle={{ color: '#e5e7eb' }} />
            <Legend wrapperStyle={{ fontSize: 12, color: '#9ca3af' }} />
            {services.map(name => (
              <Line key={name} type="monotone" dataKey={`${name}_cpu`}
                name={name} stroke={colorFor(name)} dot={false} strokeWidth={2} />
            ))}
          </LineChart>
        </ResponsiveContainer>
      </Card>

      {/* Memory chart */}
      <Card>
        <p className="text-gray-400 text-xs mb-4 font-medium">Memory usage (MB)</p>
        <ResponsiveContainer width="100%" height={200}>
          <AreaChart data={mergeSeriesByTime(series)}>
            <CartesianGrid strokeDasharray="3 3" stroke="#1f2937" />
            <XAxis dataKey="time" tick={{ fill: '#6b7280', fontSize: 11 }} interval="preserveStartEnd" />
            <YAxis tick={{ fill: '#6b7280', fontSize: 11 }} unit="MB" width={55} />
            <Tooltip contentStyle={{ background: '#111827', border: '1px solid #374151', borderRadius: 8 }}
              labelStyle={{ color: '#9ca3af' }} itemStyle={{ color: '#e5e7eb' }} />
            <Legend wrapperStyle={{ fontSize: 12, color: '#9ca3af' }} />
            {services.map(name => (
              <Area key={name} type="monotone" dataKey={`${name}_mem`}
                name={name} stroke={colorFor(name)}
                fill={colorFor(name) + '22'} strokeWidth={2} dot={false} />
            ))}
          </AreaChart>
        </ResponsiveContainer>
      </Card>

      {/* Per-service table */}
      <Card>
        <p className="text-gray-400 text-xs mb-3 font-medium">Current usage</p>
        <div className="space-y-2">
          {services.map(name => {
            const pts = series[name] || []
            const last = pts[pts.length - 1] || {}
            return (
              <div key={name} className="flex items-center gap-4">
                <div className="w-3 h-3 rounded-full shrink-0" style={{ background: colorFor(name) }} />
                <span className="text-gray-300 text-sm w-40 truncate">{name}</span>
                <div className="flex-1">
                  <div className="flex justify-between text-xs text-gray-500 mb-1">
                    <span>CPU</span><span>{last.cpu ?? 0}%</span>
                  </div>
                  <div className="bg-gray-800 rounded-full h-1.5">
                    <div className="h-1.5 rounded-full transition-all duration-500"
                      style={{ width: `${Math.min(last.cpu ?? 0, 100)}%`, background: colorFor(name) }} />
                  </div>
                </div>
                <div className="flex-1">
                  <div className="flex justify-between text-xs text-gray-500 mb-1">
                    <span>RAM</span><span>{last.mem ?? 0} MB</span>
                  </div>
                  <div className="bg-gray-800 rounded-full h-1.5">
                    <div className="h-1.5 rounded-full transition-all duration-500"
                      style={{ width: `${Math.min(last.memPct ?? 0, 100)}%`, background: colorFor(name) }} />
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      </Card>
    </div>
  )
}

// ── Logs panel ────────────────────────────────────────────────────────────────
function LogsPanel({ containers }) {
  const [selected, setSelected]   = useState(null)
  const [lines,    setLines]      = useState([])
  const [loading,  setLoading]    = useState(false)
  const [filter,   setFilter]     = useState('')
  const [tail,     setTail]       = useState(200)
  const esRef                     = useRef(null)
  const bottomRef                 = useRef(null)

  const running = containers.filter(c => c.status === 'running')

  useEffect(() => {
    if (running.length > 0 && !selected) setSelected(running[0])
  }, [containers])

  useEffect(() => {
    if (!selected) return
    setLines([])
    setLoading(true)

    // Close previous stream.
    if (esRef.current) { esRef.current.close(); esRef.current = null }

    const API = import.meta.env.VITE_API_URL || 'http://localhost:8888'
    const es = new EventSource(`${API}/api/v1/containers/${selected.containerID}/logs?tail=${tail}`)
    esRef.current = es

    es.addEventListener('log', (e) => {
      try {
        const d = JSON.parse(e.data)
        setLines(prev => [...prev.slice(-1000), d.text || d])
        setLoading(false)
      } catch {}
    })
    es.addEventListener('close', () => es.close())
    es.onerror = () => { setLoading(false) }

    return () => { es.close(); esRef.current = null }
  }, [selected, tail])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [lines])

  const filtered = filter
    ? lines.filter(l => String(l).toLowerCase().includes(filter.toLowerCase()))
    : lines

  const levelColor = (line) => {
    const l = String(line).toLowerCase()
    if (l.includes('error') || l.includes('err ')) return 'text-red-400'
    if (l.includes('warn'))  return 'text-amber-400'
    if (l.includes('info'))  return 'text-blue-400'
    return 'text-gray-300'
  }

  return (
    <div className="flex gap-4 h-full">
      {/* Container selector */}
      <div className="w-48 shrink-0 space-y-1">
        <p className="text-gray-500 text-xs px-1 mb-2">Containers</p>
        {running.length === 0 && (
          <p className="text-gray-600 text-xs px-1">No running containers</p>
        )}
        {running.map(c => (
          <button key={c.containerID} onClick={() => setSelected(c)}
            className={`w-full text-left px-3 py-2 rounded-lg text-xs transition-colors ${
              selected?.containerID === c.containerID
                ? 'bg-indigo-900/40 text-indigo-300'
                : 'text-gray-400 hover:bg-gray-800'
            }`}>
            <p className="font-medium truncate">{c.serviceName}</p>
            <p className="text-gray-600 font-mono">{c.containerID?.slice(0,8)}</p>
          </button>
        ))}
      </div>

      {/* Log viewer */}
      <div className="flex-1 flex flex-col min-h-0">
        <div className="flex items-center gap-3 mb-3">
          <input
            className="flex-1 bg-gray-800 border border-gray-700 rounded-lg px-3 py-1.5 text-xs text-gray-300 focus:outline-none focus:border-indigo-600 placeholder-gray-600"
            placeholder="Filter logs..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
          />
          <select
            value={tail}
            onChange={e => setTail(Number(e.target.value))}
            className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-1.5 text-xs text-gray-400 focus:outline-none"
          >
            {[50, 100, 200, 500].map(n => (
              <option key={n} value={n}>{n} lines</option>
            ))}
          </select>
          <Button size="sm" variant="ghost" onClick={() => setLines([])}
            className="text-gray-500 text-xs">Clear</Button>
        </div>

        <div className="flex-1 bg-gray-950 border border-gray-800 rounded-lg p-3 overflow-auto font-mono text-xs"
          style={{ minHeight: '400px', maxHeight: '600px' }}>
          {loading && <div className="text-gray-600">Connecting...</div>}
          {filtered.map((line, i) => (
            <div key={i} className={`leading-5 ${levelColor(line)}`}>
              {String(line).trimEnd()}
            </div>
          ))}
          <div ref={bottomRef} />
        </div>
      </div>
    </div>
  )
}

// ── Events panel ──────────────────────────────────────────────────────────────
function EventsPanel() {
  const [stacks,  setStacks]  = useState([])
  const [events,  setEvents]  = useState([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const load = async () => {
      try {
        const data = await listStacks()
        const stackList = data.stacks || []
        setStacks(stackList)

        // Load ledger entries for all stacks.
        const allEvents = []
        await Promise.all(stackList.map(async (s) => {
          try {
            const ledger = await getStackLedger(s.name)
            const entries = ledger.entries || []
            entries.forEach(e => allEvents.push({
              ...e,
              stackName: s.name,
              state:     s.state,
            }))
          } catch {}
        }))

        // Sort newest first.
        allEvents.sort((a, b) => new Date(b.recordedAt) - new Date(a.recordedAt))
        setEvents(allEvents)
      } catch (err) {
        console.error(err)
      } finally {
        setLoading(false)
      }
    }
    load()
    const iv = setInterval(load, 10000)
    return () => clearInterval(iv)
  }, [])

  const opColor = (op) => {
    switch (op) {
      case 'deploy':   return 'bg-indigo-900/40 text-indigo-300 border-indigo-800'
      case 'stop':     return 'bg-gray-800 text-gray-400 border-gray-700'
      case 'start':    return 'bg-teal-900/40 text-teal-300 border-teal-800'
      case 'down':     return 'bg-amber-900/40 text-amber-300 border-amber-800'
      case 'destroy':  return 'bg-red-900/40 text-red-300 border-red-800'
      case 'rollback': return 'bg-purple-900/40 text-purple-300 border-purple-800'
      default:         return 'bg-gray-800 text-gray-400 border-gray-700'
    }
  }

  if (loading) return <div className="flex justify-center py-12"><Spinner /></div>

  if (events.length === 0) return (
    <div className="text-center py-16 text-gray-600">
      <p className="text-4xl mb-3">🔔</p>
      <p className="text-sm">No events yet — deploy a service to see activity here</p>
    </div>
  )

  return (
    <div className="space-y-2">
      {/* Stack health summary */}
      <div className="flex flex-wrap gap-2 mb-4">
        {stacks.map(s => (
          <div key={s.name} className="flex items-center gap-2 bg-gray-900 border border-gray-800 rounded-lg px-3 py-1.5">
            <span className={`w-2 h-2 rounded-full ${
              s.state === 'running' ? 'bg-green-400' :
              s.state === 'stopped' ? 'bg-gray-500' :
              s.state === 'failed'  ? 'bg-red-400'  : 'bg-amber-400'
            }`} />
            <span className="text-gray-300 text-xs">{s.name}</span>
            <span className="text-gray-600 text-xs">{s.state}</span>
          </div>
        ))}
      </div>

      {/* Event feed */}
      {events.map((e, i) => (
        <div key={i} className="flex items-start gap-3 p-3 bg-gray-900 border border-gray-800 rounded-lg">
          <span className={`px-2 py-0.5 rounded border text-xs font-medium shrink-0 ${opColor(e.operation)}`}>
            {e.operation || 'event'}
          </span>
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <span className="text-gray-200 text-sm font-medium">{e.stackName}</span>
              <span className="text-gray-600 text-xs">{e.operator || 'user'}</span>
            </div>
            {e.state?.failReason && (
              <p className="text-red-400 text-xs mt-0.5">{e.state.failReason}</p>
            )}
          </div>
          <span className="text-gray-600 text-xs shrink-0">
            {e.recordedAt ? new Date(e.recordedAt).toLocaleTimeString() : ''}
          </span>
        </div>
      ))}
    </div>
  )
}

// ── Helper components ─────────────────────────────────────────────────────────
function SummaryCard({ label, value, unit, color }) {
  return (
    <Card>
      <p className="text-gray-500 text-xs mb-1">{label}</p>
      <p className={`text-2xl font-semibold ${color}`}>
        {value}<span className="text-sm ml-1 text-gray-500">{unit}</span>
      </p>
    </Card>
  )
}

// ── Data helpers ──────────────────────────────────────────────────────────────

// Merge per-service time series into a flat array for Recharts.
// { whoami: [{time, cpu}], grafana: [{time, cpu}] }
// → [{time, whoami_cpu, whoami_mem, grafana_cpu, grafana_mem}]
function mergeSeriesByTime(series) {
  const names  = Object.keys(series)
  if (names.length === 0) return []

  const longest = names.reduce((a, b) =>
    series[a].length > series[b].length ? a : b)

  return series[longest].map((pt, i) => {
    const row = { time: pt.time }
    names.forEach(name => {
      const pts = series[name]
      const p   = pts[i] || pts[pts.length - 1] || {}
      row[`${name}_cpu`] = p.cpu ?? 0
      row[`${name}_mem`] = p.mem ?? 0
    })
    return row
  })
}

function avgLast(series, key) {
  const names = Object.keys(series)
  if (names.length === 0) return 0
  const vals = names.map(n => {
    const pts = series[n]
    return pts.length > 0 ? pts[pts.length - 1][key] ?? 0 : 0
  })
  return vals.reduce((a, b) => a + b, 0) / vals.length
}

function totalLast(series, key) {
  return Object.keys(series).reduce((sum, name) => {
    const pts = series[name]
    return sum + (pts.length > 0 ? pts[pts.length - 1][key] ?? 0 : 0)
  }, 0)
}
