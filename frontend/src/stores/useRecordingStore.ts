import { create } from 'zustand'
import * as api from '../api'
import type { RecordedFrame, RecordingManifest } from '../types'

interface RecordingStore {
  recordings: RecordingManifest[]
  activeId: string
  selected: RecordingManifest | null
  frames: RecordedFrame[]
  loading: boolean
  error: string
  loadRecordings: () => Promise<void>
  startRecording: (name: string, description?: string) => Promise<void>
  stopRecording: (id: string) => Promise<void>
  loadRecording: (id: string) => Promise<void>
  loadFrames: (id: string, channel: string, offset?: number) => Promise<void>
}

export const useRecordingStore = create<RecordingStore>((set) => ({
  recordings: [],
  activeId: '',
  selected: null,
  frames: [],
  loading: false,
  error: '',
  loadRecordings: async () => {
    set({ loading: true, error: '' })
    try {
      const data = await api.fetchRecordings()
      set({ recordings: data.recordings, activeId: data.active_id, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  startRecording: async (name, description = '') => {
    set({ loading: true, error: '' })
    try {
      await api.startRecording({ name, description })
      const data = await api.fetchRecordings()
      set({ recordings: data.recordings, activeId: data.active_id, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
  stopRecording: async (id) => {
    await api.stopRecording(id)
    const data = await api.fetchRecordings()
    set({ recordings: data.recordings, activeId: data.active_id })
  },
  loadRecording: async (id) => {
    const manifest = await api.fetchRecording(id)
    set({ selected: manifest })
  },
  loadFrames: async (id, channel, offset = 0) => {
    const data = await api.fetchRecordingFrames(id, channel, offset, 100)
    set({ frames: data.frames })
  },
}))
