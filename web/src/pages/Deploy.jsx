import { useEffect, useState } from 'react'
import useStore from '../store/shipyard'
import { scanServiceFiles, deployService } from '../lib/api'
import { Button, Badge, Card, Spinner, PageHeader, EmptyState } from '../components/ui'

const PLATFORMS = [
  { value: '',           label: 'Auto-detect' },
  { value: 'docker',     label: 'Docker' },
  { value: 'compose',    label: 'Docker Compose' },
  { value: 'kubernetes', label: 'Kubernetes' },
  { value: 'nomad',      label: 'Nomad' },
  { value: 'podman',     label: 'Podman' },
]

const platformColor = {
  docker: 'blue', compose: 'teal', kubernetes: 'indigo',
  nomad: 'amber', podman: 'gray', '': 'gray',
}

export default function Deploy() {
  const { services, servicesLoading, fetchServices, addContainer } = useStore()

  useEffect(() => { fetchServices() }, [])

  if (!servicesLoading && services.length === 0) {
    return (
      <div>
        <PageHeader title="Deploy" subtitle="Deploy onboarded services to any platform" />
        <EmptyState
          title="No services onboarded"
          subtitle="Go to Services and onboard a GitHub repo first"
        />
      </div>
    )
  }

  return (
    <div>
      <PageHeader
        title="Deploy"
        subtitle="Select a platform and file for each service, then deploy"
      />
      {servicesLoading && <div className="flex justify-center py-12"><Spinner /></div>}
      <div className="grid gap-4">
        {services.map(svc => (
          <ServiceDeployCard key={svc.name} service={svc} onDeployed={addContainer} />
        ))}
      </div>
    </div>
  )
}

function ServiceDeployCard({ service, onDeployed }) {
  const [platform, setPlatform]   = useState('')
  const [file, setFile]           = useState('')
  const [stackName, setStackName] = useState('')
  const [files, setFiles]         = useState(null)
  const [loadingFiles, setLoadingFiles] = useState(false)
  const [deploying, setDeploying] = useState(false)
  const [lastDeploy, setLastDeploy] = useState(null)
  const [error, setError]         = useState(null)

  // Scan files when the card mounts.
  useEffect(() => {
    setLoadingFiles(true)
    scanServiceFiles(service.name)
      .then(data => {
        setFiles(data.files)
        // Pre-select the auto-detected platform.
        if (data.files?.autoDetected) {
          setPlatform(data.files.autoDetected)
        }
      })
      .catch(() => {})
      .finally(() => setLoadingFiles(false))
  }, [service.name])

  // Build the file options for the selected platform.
  const fileOptions = buildFileOptions(files, platform)

  const handleDeploy = async () => {
    setDeploying(true)
    setError(null)
    try {
      const result = await deployService(service.name, platform, file, stackName)
      onDeployed(result.container)
      setLastDeploy(result.container)
    } catch (err) {
      setError(err.response?.data?.error || err.message)
    } finally {
      setDeploying(false)
    }
  }

  return (
    <Card>
      <div className="flex items-start justify-between gap-6">
        {/* Left: service info */}
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 mb-1 flex-wrap">
            <h2 className="text-gray-100 font-medium">{service.name}</h2>
            {service.engine && <Badge color={platformColor[service.engine] || 'gray'}>{service.engine}</Badge>}
            {service.repoURL && (
              <a href={service.repoURL} target="_blank" rel="noreferrer"
                className="text-xs text-indigo-400 hover:text-indigo-300">
                {service.repoURL.replace('https://github.com/', '')} ↗
              </a>
            )}
          </div>
          {service.description && (
            <p className="text-gray-500 text-sm mb-3">{service.description}</p>
          )}

          {/* Deploy controls */}
          <div className="flex flex-wrap gap-3 items-end">
            {/* Platform picker */}
            <div>
              <label className="block text-gray-500 text-xs mb-1">Platform</label>
              <select
                value={platform}
                onChange={e => { setPlatform(e.target.value); setFile('') }}
                className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-indigo-500 min-w-40"
              >
                {PLATFORMS.map(p => (
                  <option key={p.value} value={p.value}>{p.label}</option>
                ))}
              </select>
            </div>

            {/* File picker */}
            <div>
              <label className="block text-gray-500 text-xs mb-1">
                File {loadingFiles && <span className="text-gray-600">(scanning...)</span>}
              </label>
              <select
                value={file}
                onChange={e => setFile(e.target.value)}
                disabled={loadingFiles || fileOptions.length === 0}
                className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 focus:outline-none focus:border-indigo-500 min-w-48 disabled:opacity-50"
              >
                <option value="">Auto-detect</option>
                {fileOptions.map(f => (
                  <option key={f} value={f}>{f}</option>
                ))}
              </select>
            </div>

            {/* Stack name */}
            <div>
              <label className="block text-gray-500 text-xs mb-1">Stack name <span className="text-gray-600">(optional)</span></label>
              <input
                value={stackName}
                onChange={e => setStackName(e.target.value)}
                placeholder="mystack"
                className="bg-gray-800 border border-gray-700 rounded-lg px-3 py-2 text-sm text-gray-200 placeholder-gray-600 focus:outline-none focus:border-indigo-500 w-32"
              />
            </div>

            <Button onClick={handleDeploy} disabled={deploying}>
              {deploying ? <><Spinner size="sm" /><span className="ml-2">Deploying...</span></> : 'Deploy'}
            </Button>
          </div>

          {error && (
            <div className="mt-3 bg-red-900/30 border border-red-800 text-red-300 rounded-lg p-3 text-sm">
              {error}
            </div>
          )}

          {lastDeploy && (
            <div className="mt-3 bg-green-900/20 border border-green-800 rounded-lg p-3">
              <p className="text-green-300 text-sm font-medium mb-1">Deployed successfully</p>
              <div className="flex flex-wrap gap-2">
                <Badge color="gray">{lastDeploy.containerID?.slice(0, 12)}</Badge>
                {lastDeploy.ports && Object.entries(lastDeploy.ports).map(([name, port]) => (
                  <a key={name} href={`http://localhost:${port}`} target="_blank" rel="noreferrer"
                    className="text-xs text-indigo-400 hover:text-indigo-300 bg-gray-800 px-2 py-0.5 rounded font-mono">
                    {name}: :{port} ↗
                  </a>
                ))}
              </div>
            </div>
          )}
        </div>

        {/* Right: detected files summary */}
        {files && (
          <div className="shrink-0 text-right">
            <p className="text-gray-600 text-xs mb-1">Detected</p>
            <div className="flex flex-col gap-1 items-end">
              {files.dockerfiles?.map(f => <Badge key={f} color="blue">{f}</Badge>)}
              {files.composeFiles?.map(f => <Badge key={f} color="teal">{f}</Badge>)}
              {files.k8sDirs?.map(f => <Badge key={f} color="indigo">{f}/</Badge>)}
              {files.nomadFiles?.map(f => <Badge key={f} color="amber">{f}</Badge>)}
            </div>
          </div>
        )}
      </div>
    </Card>
  )
}

// buildFileOptions returns file choices for the selected platform.
function buildFileOptions(files, platform) {
  if (!files) return []
  switch (platform) {
    case 'docker':
    case 'podman':
      return files.dockerfiles || []
    case 'compose':
      return files.composeFiles || []
    case 'kubernetes':
      return files.k8sDirs || []
    case 'nomad':
      return files.nomadFiles || []
    default:
      return [
        ...(files.dockerfiles || []),
        ...(files.composeFiles || []),
        ...(files.k8sDirs || []),
        ...(files.nomadFiles || []),
      ]
  }
}
