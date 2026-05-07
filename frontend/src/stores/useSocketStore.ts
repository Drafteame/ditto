import { create } from 'zustand'
import * as api from '../api'
import type { SocketClient } from '../types'

interface SocketStore {
  connectedClients: SocketClient[]
  loading: boolean
  error: string
  setConnectedClients: (clients: SocketClient[]) => void
  loadClients: () => Promise<void>
}

export const useSocketStore = create<SocketStore>((set) => ({
  connectedClients: [],
  loading: false,
  error: '',
  setConnectedClients: (connectedClients) => set({ connectedClients }),
  loadClients: async () => {
    set({ loading: true, error: '' })
    try {
      const data = await api.fetchSocketClients()
      set({ connectedClients: data.clients, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
}))
