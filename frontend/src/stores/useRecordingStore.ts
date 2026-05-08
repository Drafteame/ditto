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

function normalizeManifest(manifest: RecordingManifest): RecordingManifest {
  return {
    ...manifest,
    channels: Array.isArray(manifest.channels) ? manifest.channels : [],
    schema_pack_ids: Array.isArray(manifest.schema_pack_ids) ? manifest.schema_pack_ids : [],
  }
}

function normalizeRecordings(recordings: RecordingManifest[] | null | undefined) {
  return Array.isArray(recordings) ? recordings.map(normalizeManifest) : []
}

export const useRecordingStore = create<RecordingStore>((set, get) => ({
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
      const recordings = normalizeRecordings(data.recordings)
      const selectedId = get().selected?.id
      const selected = selectedId
        ? recordings.find(recording => recording.id === selectedId) ?? get().selected
        : get().selected
      set({ recordings, activeId: data.active_id || '', selected, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  startRecording: async (name, description = '') => {
    set({ loading: true, error: '' })
    try {
      await api.startRecording({ name, description })
      const data = await api.fetchRecordings()
      set({ recordings: normalizeRecordings(data.recordings), activeId: data.active_id || '', loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
  stopRecording: async (id) => {
    await api.stopRecording(id)
    const data = await api.fetchRecordings()
    const recordings = normalizeRecordings(data.recordings)
    const selected = get().selected?.id
      ? recordings.find(recording => recording.id === get().selected?.id) ?? get().selected
      : get().selected
    set({ recordings, activeId: data.active_id || '', selected })
  },
  loadRecording: async (id) => {
    const manifest = await api.fetchRecording(id)
    set({ selected: normalizeManifest(manifest) })
  },
  loadFrames: async (id, channel, offset = 0) => {
    const data = await api.fetchRecordingFrames(id, channel, offset, 100)
    set({ frames: Array.isArray(data.frames) ? data.frames : [] })
  },
}))
