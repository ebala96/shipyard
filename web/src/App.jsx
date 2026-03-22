import { useEffect } from 'react'
import Services from './pages/Services'
import Deploy   from './pages/Deploy'
import Scale    from './pages/Scale'
import Monitor  from './pages/Monitor'
import Nodes    from './pages/Nodes'
import Catalog  from './pages/Catalog'
import Observe    from './pages/Observe'
import Templates  from './pages/Templates'
import useStore from './store/shipyard'

const NAV = [
  { id: 'services', label: 'Services', icon: '⚓', badge: 'services' },
  { id: 'catalog',  label: 'Catalog',  icon: '📦', badge: null },
  { id: 'deploy',   label: 'Deploy',   icon: '🚀', badge: null },
  { id: 'scale',    label: 'Scale',    icon: '⚡', badge: null },
  { id: 'monitor',  label: 'Monitor',  icon: '📡', badge: 'containers' },
  { id: 'templates', label: 'Templates', icon: '📋', badge: null },
  { id: 'observe',   label: 'Observe',   icon: '🔭', badge: null },
  { id: 'nodes',    label: 'Nodes',    icon: '🖥',  badge: null },
]

export default function App() {
  const { activeTab, setActiveTab, containers, services, syncContainers, fetchServices } = useStore()

  // On startup — sync running containers from Docker and load services.
  // This restores the Monitor tab state after a server restart.
  useEffect(() => {
    fetchServices()
    syncContainers()
  }, [])

  const badgeCounts = {
    services:   services.length,
    containers: containers.length,
  }

  return (
    <div className="min-h-screen bg-gray-950 flex">
      {/* Sidebar */}
      <aside className="w-56 bg-gray-900 border-r border-gray-800 flex flex-col shrink-0">
        <div className="px-5 py-5 border-b border-gray-800">
          <div className="flex items-center gap-2">
            <span className="text-2xl">⚓</span>
            <div>
              <p className="text-gray-100 font-semibold text-sm">Shipyard</p>
              <p className="text-gray-500 text-xs">service platform</p>
            </div>
          </div>
        </div>

        <nav className="flex-1 px-3 py-4 space-y-1">
          {NAV.map(item => {
            const count = item.badge ? badgeCounts[item.badge] : 0
            return (
              <button
                key={item.id}
                onClick={() => setActiveTab(item.id)}
                className={`w-full flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm transition-colors ${
                  activeTab === item.id
                    ? 'bg-indigo-900/50 text-indigo-300 font-medium'
                    : 'text-gray-400 hover:bg-gray-800 hover:text-gray-200'
                }`}
              >
                <span>{item.icon}</span>
                <span>{item.label}</span>
                {count > 0 && (
                  <span className={`ml-auto text-xs rounded-full w-5 h-5 flex items-center justify-center ${
                    item.id === 'monitor' ? 'bg-indigo-600 text-white' : 'bg-gray-700 text-gray-300'
                  }`}>
                    {count}
                  </span>
                )}
              </button>
            )
          })}
        </nav>

        <div className="px-5 py-4 border-t border-gray-800 space-y-1">
          <p className="text-gray-600 text-xs">Shipyard v0.1.0</p>
          <a href="http://localhost:9090" target="_blank" rel="noreferrer"
            className="text-xs text-gray-600 hover:text-indigo-400 block">
            Proxy :9090 ↗
          </a>
        </div>
      </aside>

      {/* Main content */}
      <main className="flex-1 overflow-auto p-6">
        {activeTab === 'services' && <Services />}
        {activeTab === 'catalog'  && <Catalog />}
        {activeTab === 'deploy'   && <Deploy />}
        {activeTab === 'scale'    && <Scale />}
        {activeTab === 'monitor'  && <Monitor />}
        {activeTab === 'templates' && <Templates />}
        {activeTab === 'observe'   && <Observe />}
        {activeTab === 'nodes'    && <Nodes />}
      </main>
    </div>
  )
}
