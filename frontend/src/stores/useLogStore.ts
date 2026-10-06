import { create } from 'zustand'
import type { LogEntry, LogEvent } from '../types'
import { mergeLogEvents } from '../logStream'

interface LogStore {
  connected: boolean
  logEntries: LogEntry[]
  selectedLogId: string | null
  selectedEntry: LogEntry | null
  gapNotice: string | null
  setConnected: (connected: boolean) => void
  appendLogEvents: (events: LogEvent[]) => void
  clearLog: () => void
  selectLog: (id: string | null) => void
  selectSummary: (entry: LogEntry) => void
  setGapNotice: (notice: string | null) => void
  setSelectedEntry: (entry: LogEntry | null) => void
}

const MAX_LOG_ENTRIES = 5000

export const useLogStore = create<LogStore>((set) => ({
  connected: false,
  logEntries: [],
  selectedLogId: null,
  selectedEntry: null,
  gapNotice: null,

  setConnected: (connected) => set({ connected }),

  appendLogEvents: (events) => set((state) => {
    if (!events.length) return state
    const ids = new Set(state.logEntries.map(entry => entry.id))
    const selectedUpdate = events.find(event => event.type !== 'GAP' && event.id === state.selectedLogId && !ids.has(event.id))
    const logEntries = mergeLogEvents(state.logEntries, events, MAX_LOG_ENTRIES)
    if (logEntries === state.logEntries) return state
    return { logEntries, selectedEntry: selectedUpdate ?? state.selectedEntry }
  }),

  clearLog: () => set({ logEntries: [], selectedLogId: null, selectedEntry: null }),

  selectLog: (selectedLogId) => set((state) => ({
    selectedLogId,
    selectedEntry: selectedLogId ? state.logEntries.find(entry => entry.id === selectedLogId) ?? null : null,
  })),
  selectSummary: (entry) => set({ selectedLogId: entry.id, selectedEntry: entry }),

  setGapNotice: (gapNotice) => set({ gapNotice }),
  setSelectedEntry: (selectedEntry) => set({ selectedEntry }),
}))
