import { useState } from 'react'
import useStore from '../store/shipyard'
import { Button, Badge, Card, Spinner, PageHeader, EmptyState, statusColor } from '../components/ui'

const API_BASE = import.meta.env.VITE_API_URL || 'http://localhost:8888'

async function scaleService(name, instances, mode = 'production', stackName = '') {
  const r = await fetch(`${API_BASE}/api/v1/services/${name}/scale`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ instances, mode, stackName }),
  })
  if (!r.ok) {
    const err = await r.json()
    throw new Error(err.error || 'Scale failed')
  }
  return r.json()
}

export default function Scale() {
  const { containers, services } = useStore()
  const [selected, setSelected] = useState(null)

  // Group containers by serviceName
  const groups = {}
  containers.forEach(c => {
    if (!groups[c.serviceName]) groups[c.serviceName] = []
    groups[c.serviceName].push(c)
  })

  if (Object.keys(groups).length === 0) {
    return (
      <div>
        <PageHeader title="Scale" subtitle="Manage instances, resources and autoscaling" />
        <EmptyState
          title="No running services"
          subtitle="Deploy a service first to configure scaling"
        />
      </div>
    )
  }

  return (
    <div className="flex gap-4">
      {/* Service list */}
      <div className="w-64 shrink-0 space-y-2">
        <PageHeader title="Scale" />
        {Object.entries(groups).map(([name, instances]) => (
          <button
            key={name}
            onClick={() => setSelected(name)}
            className={`w-full text-left p-3 rounded-lg border transition-colors ${
              selected === name
                ? 'bg-indigo-900/40 border-indigo-700'
                : 'bg-gray-900 border-gray-800 hover:border-gray-600'
            }`}
          >
            <div className="flex items-center justify-between mb-1">
              <span className="text-gray-200 text-sm font-medium truncate">{name}</span>
              <Badge color="gray">{instances.length}x</Badge>
            </div>
            <div className="flex gap-1 flex-wrap">
              {[...new Set(instances.map(i => i.status))].map(s => (
                <Badge key={s} color={statusColor(s)}>{s}</Badge>
              ))}
            </div>
          </button>
        ))}
      </div>

      {/* Scale panel */}
      <div className="flex-1 min-w-0">
        {selected
          ? <ScalePanel
              serviceName={selected}
              instances={groups[selected]}
              service={services.find(s => s.name === selected)}
            />
          : <div className="flex items-center justify-center h-64 text-gray-600 text-sm">Select a service</div>
        }
      </div>
    </div>
  )
}

function ScalePanel({ serviceName, instances, service }) {
  const { addContainer, syncContainers } = useStore()

  const [instances_count, setInstancesCount] = useState(instances.length)
  const [cpu, setCpu] = useState(0.5)
  const [memory, setMemory] = useState('256m')
  const [autoscaleEnabled, setAutoscaleEnabled] = useState(false)
  const [minInstances, setMinInstances] = useState(1)
  const [maxInstances, setMaxInstances] = useState(5)
  const [targetCPU, setTargetCPU] = useState(70)
  const [targetMem, setTargetMem] = useState(80)
  const [lbEnabled, setLbEnabled] = useState(instances.length > 1)
  const [lbStrategy, setLbStrategy] = useState('round-robin')
  const [saving, setSaving] = useState(false)
  const [result, setResult] = useState(null)

  const mode = instances[0]?.mode || 'production'

  const handleApply = async () => {
    setSaving(true)
    setResult(null)
    try {
      const res = await scaleService(serviceName, instances_count, mode)
      setResult(res.message)
      // Sync containers from Docker so Monitor shows the correct state.
      await syncContainers()
    } catch (err) {
      alert(`Failed: ${err.message}`)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      {/* Instances */}
      <Card>
        <p className="text-gray-300 font-medium mb-4">Instances</p>
        <div className="flex items-center gap-4 mb-2">
          <input
            type="range" min={1} max={10} value={instances_count}
            onChange={e => setInstancesCount(Number(e.target.value))}
            className="flex-1"
          />
          <span className="text-gray-200 font-medium w-8 text-right">{instances_count}</span>
        </div>
        <p className="text-gray-500 text-xs">Currently running: {instances.length} instance{instances.length !== 1 ? 's' : ''}</p>
        <div className="flex gap-2 mt-3 flex-wrap">
          {instances.map((c, i) => (
            <div key={c.containerID} className="flex items-center gap-1.5 bg-gray-800 rounded-lg px-3 py-1.5">
              <div className={`w-2 h-2 rounded-full ${c.status === 'running' ? 'bg-green-400' : 'bg-red-400'}`} />
              <span className="text-gray-300 text-xs font-mono">#{i + 1} {c.containerID?.slice(0, 8)}</span>
            </div>
          ))}
        </div>
      </Card>

      {/* Resources */}
      <Card>
        <p className="text-gray-300 font-medium mb-4">Resources <span className="text-gray-500 text-sm font-normal">per instance</span></p>
        <div className="grid grid-cols-2 gap-4">
          <div>
            <label className="block text-gray-400 text-sm mb-1.5">CPU cores</label>
            <div className="flex items-center gap-3">
              <input type="range" min={0.1} max={8} step={0.1} value={cpu}
                onChange={e => setCpu(Number(e.target.value))} className="flex-1" />
              <span className="text-gray-200 text-sm w-10 text-right">{cpu.toFixed(1)}</span>
            </div>
          </div>
          <div>
            <label className="block text-gray-400 text-sm mb-1.5">Memory</label>
            <select value={memory} onChange={e => setMemory(e.target.value)}
              className="w-full bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-indigo-500">
              {['128m','256m','512m','1g','2g','4g','8g'].map(m => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </div>
        </div>
      </Card>

      {/* Autoscale */}
      <Card>
        <div className="flex items-center justify-between mb-4">
          <p className="text-gray-300 font-medium">Autoscale</p>
          <Toggle enabled={autoscaleEnabled} onChange={setAutoscaleEnabled} />
        </div>

        {autoscaleEnabled && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-4">
              <LabeledSlider label="Min instances" value={minInstances} min={1} max={10}
                onChange={setMinInstances} />
              <LabeledSlider label="Max instances" value={maxInstances} min={1} max={20}
                onChange={setMaxInstances} />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <LabeledSlider label="Scale up CPU %" value={targetCPU} min={10} max={95}
                onChange={setTargetCPU} unit="%" />
              <LabeledSlider label="Scale up memory %" value={targetMem} min={10} max={95}
                onChange={setTargetMem} unit="%" />
            </div>
            <p className="text-gray-500 text-xs">
              Shipyard will add instances when avg CPU exceeds {targetCPU}% or memory exceeds {targetMem}%, up to {maxInstances} max.
              Scale down when usage drops below half the thresholds, down to {minInstances} min.
            </p>
          </div>
        )}
      </Card>

      {/* Load balancer */}
      <Card>
        <div className="flex items-center justify-between mb-4">
          <p className="text-gray-300 font-medium">Load balancer</p>
          <Toggle enabled={lbEnabled} onChange={setLbEnabled} />
        </div>

        {lbEnabled && (
          <div className="space-y-3">
            <div>
              <label className="block text-gray-400 text-sm mb-1.5">Strategy</label>
              <div className="flex gap-2">
                {['round-robin', 'least-conn', 'ip-hash'].map(s => (
                  <button
                    key={s}
                    onClick={() => setLbStrategy(s)}
                    className={`px-3 py-1.5 rounded-lg text-xs font-medium transition-colors ${
                      lbStrategy === s
                        ? 'bg-indigo-600 text-white'
                        : 'bg-gray-800 text-gray-400 hover:text-gray-200'
                    }`}
                  >{s}</button>
                ))}
              </div>
            </div>
            <p className="text-gray-500 text-xs">
              Traffic to {serviceName} will be distributed across all {instances.length} running instances using {lbStrategy}.
            </p>
          </div>
        )}
      </Card>

      {/* Apply */}
      <div className="flex items-center justify-between">
        {result && <p className="text-green-400 text-sm">{result}</p>}
        <Button onClick={handleApply} disabled={saving} className="ml-auto">
          {saving ? <><Spinner size="sm" /><span className="ml-2">Scaling...</span></> : 'Apply scale config'}
        </Button>
      </div>
    </div>
  )
}

// ── Small reusable sub-components ─────────────────────────────────────────────

function Toggle({ enabled, onChange }) {
  return (
    <button
      onClick={() => onChange(!enabled)}
      className={`w-11 h-6 rounded-full transition-colors relative ${enabled ? 'bg-indigo-600' : 'bg-gray-700'}`}
    >
      <span className={`absolute top-0.5 w-5 h-5 bg-white rounded-full transition-transform shadow ${enabled ? 'translate-x-5' : 'translate-x-0.5'}`} />
    </button>
  )
}

function LabeledSlider({ label, value, min, max, onChange, unit = '' }) {
  return (
    <div>
      <label className="block text-gray-400 text-sm mb-1.5">{label}</label>
      <div className="flex items-center gap-3">
        <input type="range" min={min} max={max} value={value}
          onChange={e => onChange(Number(e.target.value))} className="flex-1" />
        <span className="text-gray-200 text-sm w-10 text-right">{value}{unit}</span>
      </div>
    </div>
  )
}