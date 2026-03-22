import { useEffect, useState } from 'react'
import { getContainerStats } from '../lib/api'
import { Badge, Card, PageHeader, Spinner } from '../components/ui'

const API = import.meta.env.VITE_API_URL || 'http://localhost:8888'
const getNodes  = () => fetch(`${API}/api/v1/nodes`).then(r => r.json())
const getStacks = () => fetch(`${API}/api/v1/stacks`).then(r => r.json())

const POLL_INTERVAL = 10000

export default function Nodes() {
  const [nodes,   setNodes]   = useState([])
  const [stacks,  setStacks]  = useState([])
  const [stats,   setStats]   = useState([])
  const [loading, setLoading] = useState(true)
  const [lastUpdated, setLastUpdated] = useState(null)

  const fetchAll = async () => {
    try {
      const [nodesData, stacksData, statsData] = await Promise.all([
        getNodes(),
        getStacks().catch(() => ({ stacks: [] })),
        getContainerStats().catch(() => ({ stats: [] })),
      ])
      setNodes(nodesData.nodes || [])
      setStacks(stacksData.stacks || [])
      setStats(statsData.stats || [])
      setLastUpdated(new Date())
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchAll()
    const iv = setInterval(fetchAll, POLL_INTERVAL)
    return () => clearInterval(iv)
  }, [])

  // Group stacks by node
  const stacksByNode = {}
  for (const s of stacks) {
    const node = s.node || 'localhost'
    if (!stacksByNode[node]) stacksByNode[node] = []
    stacksByNode[node].push(s)
  }

  // Build node list — registered nodes + localhost fallback
  const allNodes = [...nodes]
  if (allNodes.length === 0) {
    allNodes.push({
      id: 'localhost', name: 'localhost', hostname: 'localhost',
      status: 'healthy', cpuCores: 0, memTotalMB: 0,
      cpuPercent: 0, memUsedMB: 0, memPercent: 0,
    })
  }

  return (
    <div>
      <PageHeader
        title="Nodes"
        subtitle="Registered nodes and their running services"
        action={
          <div className="flex items-center gap-3">
            {lastUpdated && (
              <span className="text-gray-600 text-xs">
                Updated {lastUpdated.toLocaleTimeString()}
              </span>
            )}
            <div className="w-2 h-2 rounded-full bg-green-400 animate-pulse" />
          </div>
        }
      />

      {loading && <div className="flex justify-center py-12"><Spinner /></div>}

      {/* Node cards */}
      <div className="space-y-4">
        {allNodes.map(node => {
          const nodeStacks = stacksByNode[node.name] || stacksByNode[node.hostname] || []
          const nodeStats  = stats.filter(s =>
            nodeStacks.some(st => st.serviceName === s.serviceName)
          )
          const statusColor = node.status === 'healthy' ? 'bg-green-400' :
                              node.status === 'degraded' ? 'bg-amber-400' : 'bg-gray-500'

          return (
            <Card key={node.id}>
              {/* Node header */}
              <div className="flex items-start justify-between mb-4">
                <div className="flex items-center gap-3">
                  <div className={`w-2.5 h-2.5 rounded-full shrink-0 ${statusColor}`} />
                  <div>
                    <p className="text-gray-100 font-medium text-sm">{node.name}</p>
                    <p className="text-gray-600 text-xs">{node.hostname}</p>
                  </div>
                </div>
                <div className="flex gap-4 text-right">
                  <div>
                    <p className="text-gray-500 text-xs">CPU</p>
                    <p className={`text-sm font-medium ${node.cpuPercent > 70 ? 'text-red-400' : 'text-gray-200'}`}>
                      {node.cpuPercent?.toFixed(1) ?? 0}%
                    </p>
                    <p className="text-gray-600 text-xs">{node.cpuCores} cores</p>
                  </div>
                  <div>
                    <p className="text-gray-500 text-xs">RAM</p>
                    <p className={`text-sm font-medium ${node.memPercent > 70 ? 'text-amber-400' : 'text-gray-200'}`}>
                      {node.memPercent?.toFixed(1) ?? 0}%
                    </p>
                    <p className="text-gray-600 text-xs">
                      {node.memUsedMB} / {node.memTotalMB} MB
                    </p>
                  </div>
                  <div>
                    <p className="text-gray-500 text-xs">Services</p>
                    <p className="text-sm font-medium text-gray-200">{nodeStacks.length}</p>
                  </div>
                </div>
              </div>

              {/* CPU / RAM bars */}
              <div className="grid grid-cols-2 gap-3 mb-4">
                <div>
                  <div className="flex justify-between text-xs text-gray-600 mb-1">
                    <span>CPU</span><span>{node.cpuPercent?.toFixed(1) ?? 0}%</span>
                  </div>
                  <ProgressBar value={node.cpuPercent ?? 0} color={barColor(node.cpuPercent ?? 0)} />
                </div>
                <div>
                  <div className="flex justify-between text-xs text-gray-600 mb-1">
                    <span>Memory</span><span>{node.memPercent?.toFixed(1) ?? 0}%</span>
                  </div>
                  <ProgressBar value={node.memPercent ?? 0} color={barColor(node.memPercent ?? 0)} />
                </div>
              </div>

              {/* Services running on this node */}
              {nodeStacks.length > 0 && (
                <div className="border-t border-gray-800 pt-3">
                  <p className="text-gray-600 text-xs mb-2">Running services</p>
                  <div className="flex flex-wrap gap-2">
                    {nodeStacks.map(s => {
                      const stat = nodeStats.find(ns => ns.serviceName === s.serviceName)
                      return (
                        <div key={s.name}
                          className="flex items-center gap-2 bg-gray-800 border border-gray-700 rounded-lg px-3 py-1.5">
                          <span className={`w-1.5 h-1.5 rounded-full ${
                            s.state === 'running' ? 'bg-green-400' :
                            s.state === 'stopped' ? 'bg-gray-500' : 'bg-amber-400'
                          }`} />
                          <span className="text-gray-300 text-xs font-medium">{s.serviceName}</span>
                          {stat && (
                            <span className="text-gray-600 text-xs">
                              {stat.cpuPercent.toFixed(1)}% CPU · {stat.memUsageMB.toFixed(0)} MB
                            </span>
                          )}
                        </div>
                      )
                    })}
                  </div>
                </div>
              )}

              {nodeStacks.length === 0 && (
                <div className="border-t border-gray-800 pt-3">
                  <p className="text-gray-700 text-xs">No services scheduled to this node</p>
                </div>
              )}
            </Card>
          )
        })}
      </div>

      {/* Container stats table */}
      {stats.length > 0 && (
        <div className="mt-6">
          <p className="text-gray-500 text-xs mb-3 font-medium">All containers</p>
          <div className="space-y-2">
            {stats.map(s => (
              <Card key={s.containerID}>
                <div className="flex items-center gap-4">
                  <div className="w-44 shrink-0">
                    <p className="text-gray-200 text-sm font-medium truncate">{s.serviceName}</p>
                    <p className="text-gray-600 text-xs font-mono">{s.containerID.slice(0, 12)}</p>
                  </div>
                  <div className="flex-1">
                    <div className="flex justify-between text-xs text-gray-500 mb-1">
                      <span>CPU</span><span>{s.cpuPercent.toFixed(1)}%</span>
                    </div>
                    <ProgressBar value={s.cpuPercent} color={barColor(s.cpuPercent)} />
                  </div>
                  <div className="flex-1">
                    <div className="flex justify-between text-xs text-gray-500 mb-1">
                      <span>RAM</span>
                      <span>{s.memUsageMB.toFixed(0)} / {s.memLimitMB > 0 ? s.memLimitMB.toFixed(0) : '∞'} MB</span>
                    </div>
                    <ProgressBar value={s.memPercent} color={barColor(s.memPercent)} />
                  </div>
                  <div className="w-28 shrink-0 text-right">
                    <p className="text-gray-500 text-xs">Network</p>
                    <p className="text-gray-400 text-xs font-mono">↓ {s.netRxMB?.toFixed(1) ?? 0} MB</p>
                    <p className="text-gray-400 text-xs font-mono">↑ {s.netTxMB?.toFixed(1) ?? 0} MB</p>
                  </div>
                </div>
              </Card>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

function ProgressBar({ value, color = 'blue' }) {
  const pct = Math.min(100, Math.max(0, value))
  const colors = { green: 'bg-green-500', amber: 'bg-amber-500', red: 'bg-red-500', blue: 'bg-blue-500' }
  return (
    <div className="w-full bg-gray-800 rounded-full h-1.5">
      <div
        className={`h-1.5 rounded-full transition-all duration-500 ${colors[color] || 'bg-blue-500'}`}
        style={{ width: `${pct}%` }}
      />
    </div>
  )
}

function barColor(pct) {
  if (pct > 80) return 'red'
  if (pct > 50) return 'amber'
  return 'green'
}
