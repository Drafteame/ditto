import { create } from 'zustand'
import * as api from '../api'
import { nextCursor } from '../sequence'
import type { LogEvent, Mock, ServerInfo } from '../types'

interface MockStore {
  mocks: Mock[]
  serverInfo: ServerInfo | null
  loadMocks: () => Promise<void>
  reloadMocks: () => Promise<void>
  advanceSequenceCursor: (event: LogEvent) => void
}

export const useMockStore = create<MockStore>((set, get) => ({
  mocks: [],
  serverInfo: null,

  loadMocks: async () => {
    try {
      const data = await api.fetchMocks()
      set({ mocks: data.mocks, serverInfo: data.info })
    } catch (err) {
      console.error('Failed to load mocks:', err)
    }
  },

  reloadMocks: async () => {
    await api.reloadMocks()
    await get().loadMocks()
  },

  advanceSequenceCursor: (event) => {
    if (
      event.type !== 'MOCK' ||
      !event.sequence_step ||
      !event.sequence_len ||
      typeof event.mock_index !== 'number'
    ) {
      return
    }

    set((state) => {
      const idx = event.mock_index!
      const target = state.mocks[idx]
      if (!target?.sequence) return state

      const next = nextCursor(event.sequence_step!, event.sequence_len!, target.sequence.on_end)
      if (target.sequence.current_step === next) return state

      const mocks = state.mocks.slice()
      mocks[idx] = {
        ...target,
        sequence: { ...target.sequence, current_step: next },
      }
      return { mocks }
    })
  },
}))
