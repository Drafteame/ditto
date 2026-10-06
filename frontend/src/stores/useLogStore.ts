import { create } from 'zustand'
import type { LogEntry, LogEvent } from '../types'

interface LogStore {
  connected: boolean
  logEntries: LogEntry[]
  selectedLogId: string | null
  setConnected: (connected: boolean) => void
  appendLogEvent: (event: LogEvent) => void
  clearLog: () => void
  selectLog: (id: string | null) => void
}

export const useLogStore = create<LogStore>((set) => ({
  connected: false,
  logEntries: [],
  selectedLogId: null,

  setConnected: (connected) => set({ connected }),

  appendLogEvent: (event) => {
    const entry: LogEntry = event
    set((state) => ({ logEntries: [...state.logEntries, entry] }))
  },

  clearLog: () => set({ logEntries: [], selectedLogId: null }),

  selectLog: (selectedLogId) => set({ selectedLogId }),
}))
