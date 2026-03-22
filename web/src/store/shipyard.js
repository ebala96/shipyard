import { create } from 'zustand'
import * as api from '../lib/api'

const useStore = create((set, get) => ({

  // ── Services ────────────────────────────────────────────────────────────────
  services: [],
  servicesLoading: false,
  servicesError: null,

  fetchServices: async () => {
    set({ servicesLoading: true, servicesError: null })
    try {
      const data = await api.listServices()
      set({ services: data.services || [], servicesLoading: false })
    } catch (err) {
      set({ servicesError: err.message, servicesLoading: false })
    }
  },

  removeService: async (name) => {
    await api.deleteService(name)
    set(state => ({ services: state.services.filter(s => s.name !== name) }))
  },

  // ── Containers ──────────────────────────────────────────────────────────────
  containers: [],
  containersLoading: false,

  // syncContainers fetches all running Shipyard containers from Docker.
  // Called on app startup so previously deployed services appear in Monitor.
  syncContainers: async () => {
    set({ containersLoading: true })
    try {
      const data = await api.listContainers()
      // Merge with existing in-session containers — avoid duplicates.
      const fetched = data.containers || []
      set(state => {
        const existingIDs = new Set(state.containers.map(c => c.containerID))
        const newOnes = fetched.filter(c => !existingIDs.has(c.containerID))
        // Update status of existing ones from Docker's live state.
        const updated = state.containers.map(c => {
          const live = fetched.find(f => f.containerID === c.containerID)
          return live ? { ...c, status: live.status } : c
        })
        return { containers: [...updated, ...newOnes], containersLoading: false }
      })
    } catch {
      set({ containersLoading: false })
    }
  },

  addContainer: (container) => {
    if (!container) return
    set(state => {
      // Update if already exists, otherwise add.
      const exists = state.containers.find(c => c.containerID === container.containerID)
      if (exists) {
        return {
          containers: state.containers.map(c =>
            c.containerID === container.containerID ? { ...c, ...container } : c
          )
        }
      }
      return { containers: [...state.containers, { ...container, status: container.status || 'running' }] }
    })
  },

  updateContainerStatus: (id, status) =>
    set(state => ({
      containers: state.containers.map(c =>
        c.containerID === id ? { ...c, status } : c
      )
    })),

  removeContainerFromStore: (id) =>
    set(state => ({
      containers: state.containers.filter(c => c.containerID !== id)
    })),

  refreshContainerStatus: async (id) => {
    try {
      const data = await api.getContainerStatus(id)
      get().updateContainerStatus(id, data.status)
    } catch {
      get().updateContainerStatus(id, 'unknown')
    }
  },

  // ── UI state ────────────────────────────────────────────────────────────────
  activeTab: 'services',
  setActiveTab: (tab) => set({ activeTab: tab }),

}))

export default useStore
