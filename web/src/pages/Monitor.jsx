import { useEffect, useState, useRef } from 'react'
import useStore from '../store/shipyard'
import { fetchLogs, streamLogs, execInContainer, startVNC, stopVNC, shareVNC } from '../lib/api'
import { Button, Badge, Card, Spinner, PageHeader, EmptyState, statusColor } from '../components/ui'

export default function Monitor() {
  const { containers, updateContainerStatus, removeContainerFromStore, refreshContainerStatus } = useStore()
  const [selected, setSelected] = useState(null)

  // Poll status every 5 seconds for all containers
  useEffect(() => {
    if (containers.length === 0) return
    const interval = setInterval(() => {
      containers.forEach(c => refreshContainerStatus(c.containerID))
    }, 5000)
    return () => clearInterval(interval)
  }, [containers])

  if (containers.length === 0) {
    return (
      <div>
        <PageHeader title="Monitor" subtitle="Running containers and their health" />
        <EmptyState
          title="No containers running"
          subtitle="Deploy a service from the Services tab to see it here"
        />
      </div>
    )
  }

  return (
    <div className="flex gap-4 h-full">
      {/* Container list */}
      <div className="w-72 shrink-0 space-y-2">
        <PageHeader title="Monitor" />
        {containers.map(c => (
          <button
            key={c.containerID}
            onClick={() => setSelected(c)}
            className={`w-full text-left p-3 rounded-lg border transition-colors ${
              selected?.containerID === c.containerID
                ? 'bg-indigo-900/40 border-indigo-700'
                : 'bg-gray-900 border-gray-800 hover:border-gray-600'
            }`}
          >
            <div className="flex items-center justify-between mb-1">
              <span className="text-gray-200 text-sm font-medium truncate">{c.serviceName}</span>
              <Badge color={statusColor(c.status)}>{c.status}</Badge>
            </div>
            <div className="flex items-center gap-2">
              <Badge color="gray">{c.mode}</Badge>
              <span className="text-gray-600 text-xs font-mono">{c.containerID?.slice(0, 8)}</span>
            </div>
          </button>
        ))}
      </div>

      {/* Detail panel */}
      <div className="flex-1 min-w-0">
        {selected
          ? <ContainerDetail container={selected} onRemoved={() => { removeContainerFromStore(selected.containerID); setSelected(null) }} />
          : <div className="flex items-center justify-center h-64 text-gray-600 text-sm">Select a container</div>
        }
      </div>
    </div>
  )
}

function ContainerDetail({ container, onRemoved }) {
  const { updateContainerStatus, containers } = useStore()

  // Read live status from store so it updates when stop/start is clicked.
  const liveContainer = containers.find(c => c.containerID === container.containerID) || container
  const liveStatus = liveContainer.status || container.status
  const [logs, setLogs] = useState([])
  const [loadingLogs, setLoadingLogs] = useState(true)
  const [execCmd, setExecCmd] = useState('')
  const [execOutput, setExecOutput] = useState(null)
  const [execLoading, setExecLoading] = useState(false)
  const [vncSession, setVncSession] = useState(liveContainer.vnc || null)
  const [vncLoading, setVncLoading] = useState(false)
  const [shareLink, setShareLink] = useState(null)
  const [shareLoading, setShareLoading] = useState(false)
  const [shareCopied, setShareCopied] = useState(false)
  const logsEndRef = useRef()
  const esRef = useRef()

  useEffect(() => {
    setLogs([])
    setLoadingLogs(true)

    // Fetch snapshot first
    fetchLogs(container.containerID, 100)
      .then(data => {
        setLogs(data.lines || [])
        setLoadingLogs(false)
      })
      .catch(() => setLoadingLogs(false))

    // Then open SSE stream for live updates
    esRef.current = streamLogs(
      container.containerID,
      50,
      (line) => setLogs(prev => [...prev.slice(-500), line]), // keep last 500 lines
      () => {}
    )

    return () => esRef.current?.close()
  }, [container.containerID])

  useEffect(() => {
    logsEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [logs])

  const handleExec = async () => {
    if (!execCmd.trim()) return
    setExecLoading(true)
    try {
      // Run through sh -c so pipes, quotes, and redirects all work.
      // Falls back to direct split if sh is not available (scratch containers).
      let cmd
      try {
        const result = await execInContainer(container.containerID, ['sh', '-c', execCmd.trim()])
        setExecOutput(result.output)
        return
      } catch {
        cmd = execCmd.trim().split(' ')
      }
      const result = await execInContainer(container.containerID, cmd)
      setExecOutput(result.output)
    } catch (err) {
      setExecOutput(`Error: ${err.response?.data?.error || err.message}`)
    } finally {
      setExecLoading(false)
    }
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <Card>
        <div className="flex items-start justify-between">
          <div>
            <div className="flex items-center gap-2 mb-2">
              <h2 className="text-gray-100 font-semibold">{container.serviceName}</h2>
              <Badge color={statusColor(liveStatus)}>{liveStatus}</Badge>
              <Badge color="gray">{container.mode}</Badge>
              {container.ide && (
                <Badge color="indigo">IDE</Badge>
              )}
            </div>
            <p className="text-gray-500 text-xs font-mono mb-3">{container.containerID}</p>
            <div className="flex flex-wrap gap-2">
              {container.ports && Object.entries(container.ports).map(([name, port]) => (
                <a
                  key={name}
                  href={`http://localhost:${port}`}
                  target="_blank"
                  rel="noreferrer"
                  className="text-xs text-indigo-400 hover:text-indigo-300 bg-gray-800 px-2 py-1 rounded font-mono"
                >
                  {name}: :{port} ↗
                </a>
              ))}
              {container.ide && (
                <a
                  href={container.ide.url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-xs text-teal-400 hover:text-teal-300 bg-gray-800 px-2 py-1 rounded font-mono"
                >
                  code-server :{container.ide.hostPort} ↗
                </a>
              )}
            </div>
          </div>
          <div className="flex gap-2 flex-wrap justify-end">
            <Button size="sm" variant="secondary"
              onClick={async () => {
                const { startContainer } = await import('../lib/api')
                await startContainer(container.containerID)
                updateContainerStatus(container.containerID, 'running')
              }}>Start</Button>
            <Button size="sm" variant="secondary"
              onClick={async () => {
                const { stopContainer } = await import('../lib/api')
                await stopContainer(container.containerID, container.serviceName, container.mode)
                updateContainerStatus(container.containerID, 'exited')
              }}>Stop</Button>
            <Button size="sm" variant="secondary"
              onClick={async () => {
                const { restartContainer } = await import('../lib/api')
                await restartContainer(container.containerID)
                updateContainerStatus(container.containerID, 'running')
              }}>Restart</Button>
            <Button size="sm" variant="danger"
              onClick={async () => {
                if (!confirm('Remove this container?')) return
                const { removeContainer } = await import('../lib/api')
                await removeContainer(container.containerID, true, container.serviceName, container.mode)
                onRemoved()
              }}>Remove</Button>
          </div>
        </div>
      </Card>

      {/* Logs */}
      <Card className="!p-0 overflow-hidden">
        <div className="flex items-center justify-between px-4 py-3 border-b border-gray-800">
          <span className="text-gray-400 text-sm font-medium">Logs</span>
          <Button size="sm" variant="ghost" onClick={() => setLogs([])}>Clear</Button>
        </div>
        <div className="h-64 overflow-y-auto p-4 bg-gray-950">
          {loadingLogs && <div className="flex justify-center py-4"><Spinner /></div>}
          {logs.map((line, i) => (
            <div key={i} className={`log-line ${line.stream === 'stderr' ? 'log-stderr' : 'log-stdout'}`}>
              {line.text}
            </div>
          ))}
          <div ref={logsEndRef} />
        </div>
      </Card>

      {/* Exec */}
      <Card>
        <p className="text-gray-400 text-sm font-medium mb-3">Exec into container</p>
        <div className="flex gap-2 mb-3">
          <input
            value={execCmd}
            onChange={e => setExecCmd(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && handleExec()}
            placeholder="e.g. ls -la"
            className="flex-1 bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 font-mono placeholder-gray-600 focus:outline-none focus:border-indigo-500"
          />
          <Button onClick={handleExec} disabled={execLoading || !execCmd.trim()}>
            {execLoading ? <Spinner size="sm" /> : 'Run'}
          </Button>
        </div>
        {execOutput !== null && (
          <pre className="bg-gray-950 rounded-lg p-3 text-xs font-mono text-gray-300 overflow-x-auto whitespace-pre-wrap">
            {execOutput || '(no output)'}
          </pre>
        )}
      </Card>

      {/* IDE panel — only shown when code-server sidecar is running */}
      {container.ide && (
        <Card className="!p-0 overflow-hidden">
          <div className="flex items-center justify-between px-4 py-3 border-b border-gray-800">
            <div className="flex items-center gap-2">
              <span className="text-teal-400 text-sm font-medium">code-server IDE</span>
              <Badge color="indigo">dev mode</Badge>
            </div>
            <div className="flex items-center gap-3">
              <span className="text-gray-500 text-xs font-mono">:{container.ide.hostPort}</span>
              <a
                href={container.ide.url}
                target="_blank"
                rel="noreferrer"
                className="text-xs text-teal-400 hover:text-teal-300"
              >
                open in new tab ↗
              </a>
            </div>
          </div>
          <iframe
            src={container.ide.url}
            className="w-full border-0"
            style={{ height: '600px' }}
            title={`IDE — ${container.serviceName}`}
          />
        </Card>
      )}

      {/* VNC panel — live screen of a running GUI container */}
      <Card>
        <div className="flex items-center justify-between mb-3">
          <div className="flex items-center gap-2">
            <span className="text-purple-400 text-sm font-medium">VNC Screen Share</span>
            {vncSession && <Badge color="green">connected</Badge>}
          </div>
          <div className="flex items-center gap-3">
            {vncSession && (
              <>
                <span className="text-gray-500 text-xs font-mono">:{vncSession.hostPort}</span>
                <a
                  href={vncSession.url}
                  target="_blank"
                  rel="noreferrer"
                  className="text-xs text-purple-400 hover:text-purple-300"
                >
                  open in new tab ↗
                </a>
              </>
            )}
            {vncSession ? (
              <>
                <Button
                  size="sm"
                  variant="secondary"
                  disabled={shareLoading}
                  onClick={async () => {
                    setShareLoading(true)
                    try {
                      const data = await shareVNC(container.serviceName)
                      setShareLink(data.viewURL)
                      setShareCopied(false)
                    } catch (err) {
                      console.error('VNC share failed:', err)
                    } finally {
                      setShareLoading(false)
                    }
                  }}
                >
                  {shareLoading ? <Spinner size="sm" /> : 'Share'}
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  disabled={vncLoading}
                  onClick={async () => {
                    setVncLoading(true)
                    try {
                      await stopVNC(container.serviceName)
                      setVncSession(null)
                      setShareLink(null)
                    } catch (err) {
                      console.error('VNC stop failed:', err)
                    } finally {
                      setVncLoading(false)
                    }
                  }}
                >
                  {vncLoading ? <Spinner size="sm" /> : 'Disconnect'}
                </Button>
              </>
            ) : (
              <Button
                size="sm"
                variant="secondary"
                disabled={vncLoading}
                onClick={async () => {
                  setVncLoading(true)
                  try {
                    const data = await startVNC(container.serviceName)
                    setVncSession({ url: data.url, hostPort: data.hostPort })
                  } catch (err) {
                    console.error('VNC start failed:', err)
                  } finally {
                    setVncLoading(false)
                  }
                }}
              >
                {vncLoading ? <Spinner size="sm" /> : 'Connect'}
              </Button>
            )}
          </div>
        </div>
        {vncSession ? (
          <>
            <iframe
              src={vncSession.url}
              className="w-full border-0 rounded-lg"
              style={{ height: '600px' }}
              title={`VNC — ${container.serviceName}`}
            />
            {shareLink && (
              <div className="mt-3 p-3 bg-gray-900 rounded-lg border border-purple-900">
                <p className="text-gray-400 text-xs mb-2">Share this link — the other user opens it in their browser:</p>
                <div className="flex items-center gap-2">
                  <code className="flex-1 text-purple-300 text-xs break-all font-mono">{shareLink}</code>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      navigator.clipboard.writeText(shareLink)
                      setShareCopied(true)
                      setTimeout(() => setShareCopied(false), 2000)
                    }}
                  >
                    {shareCopied ? '✓ Copied' : 'Copy'}
                  </Button>
                </div>
              </div>
            )}
          </>
        ) : (
          <p className="text-gray-600 text-xs">
            Requires the container to have a VNC server (Xvfb + x11vnc) running on port 5900,
            or set <code className="text-gray-500">vnc.port</code> in the shipfile.
          </p>
        )}
      </Card>
    </div>
  )
}