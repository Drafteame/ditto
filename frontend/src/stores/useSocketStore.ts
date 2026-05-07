import { create } from 'zustand'
import type { LogEntry, LogEvent } from '../types'

let nextLogId = 0

interface SocketStore {
  connected: boolean
  logEntries: LogEntry[]
  selectedLogId: string | null
  setConnected: (connected: boolean) => void
  appendLogEvent: (event: LogEvent) => void
  clearLog: () => void
  selectLog: (id: string | null) => void
}

export const useSocketStore = create<SocketStore>((set) => ({
  connected: false,
  logEntries: [],
  selectedLogId: null,

  setConnected: (connected) => set({ connected }),

  appendLogEvent: (event) => {
    const entry: LogEntry = { ...event, id: String(++nextLogId) }
    set((state) => ({ logEntries: [...state.logEntries, entry] }))
  },

  clearLog: () => set({ logEntries: [], selectedLogId: null }),

  selectLog: (selectedLogId) => set({ selectedLogId }),
}))
