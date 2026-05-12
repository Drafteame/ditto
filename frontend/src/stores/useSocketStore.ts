import { create } from 'zustand'
import * as api from '../api'
import type { AdapterProfileSummary, SocketClient } from '../types'

export interface AdapterOption {
  value: string
  label: string
  builtin: boolean
}

const BUILTIN_ADAPTERS: AdapterOption[] = [
  { value: 'raw', label: 'Raw', builtin: true },
  { value: 'appsync', label: 'AppSync', builtin: true },
]

export function buildAdapterOptions(profiles: AdapterProfileSummary[]): AdapterOption[] {
  const seen = new Set<string>(BUILTIN_ADAPTERS.map(option => option.value))
  const dynamic: AdapterOption[] = []
  for (const profile of profiles) {
    if (!profile.name || seen.has(profile.name)) continue
    seen.add(profile.name)
    dynamic.push({ value: profile.name, label: profile.name, builtin: false })
  }
  dynamic.sort((a, b) => a.label.localeCompare(b.label))
  return [...BUILTIN_ADAPTERS, ...dynamic]
}

interface SocketStore {
  connectedClients: SocketClient[]
  loading: boolean
  error: string
  loadClients: () => Promise<void>
  adapterProfiles: AdapterProfileSummary[]
  adapterProfilesLoading: boolean
  adapterProfilesError: string
  loadAdapterProfiles: () => Promise<void>
}

export const useSocketStore = create<SocketStore>((set) => ({
  connectedClients: [],
  loading: false,
  error: '',
  loadClients: async () => {
    set({ loading: true, error: '' })
    try {
      const data = await api.fetchSocketClients()
      set({ connectedClients: data.clients, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  adapterProfiles: [],
  adapterProfilesLoading: false,
  adapterProfilesError: '',
  loadAdapterProfiles: async () => {
    set({ adapterProfilesLoading: true, adapterProfilesError: '' })
    try {
      const profiles = await api.fetchAdapterProfiles()
      set({ adapterProfiles: profiles ?? [], adapterProfilesLoading: false })
    } catch (err) {
      set({ adapterProfilesLoading: false, adapterProfilesError: (err as Error).message })
    }
  },
}))
