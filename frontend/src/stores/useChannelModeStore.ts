import { create } from 'zustand'
import * as api from '../api'
import type { ChannelConfig, ChannelMode } from '../types'

interface ChannelModeStore {
  modes: Record<string, ChannelConfig>
  liveTarget: string
  loading: boolean
  error: string
  loadModes: () => Promise<void>
  setMode: (channel: string, mode: ChannelMode, rateCapHz?: number) => Promise<void>
  loadLiveTarget: () => Promise<void>
  setLiveTarget: (target: string) => Promise<void>
}

export const useChannelModeStore = create<ChannelModeStore>((set, get) => ({
  modes: {},
  liveTarget: '',
  loading: false,
  error: '',
  loadModes: async () => {
    set({ loading: true, error: '' })
    try {
      const data = await api.fetchChannelModes()
      const modes: Record<string, ChannelConfig> = {}
      data.channels.forEach(config => { modes[config.channel] = config })
      set({ modes, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  setMode: async (channel, mode, rateCapHz = 0) => {
    const previous = get().modes
    const optimistic: ChannelConfig = {
      channel,
      mode,
      rate_cap_hz: rateCapHz,
      updated_at: new Date().toISOString(),
    }
    set({ modes: { ...previous, [channel]: optimistic }, error: '' })
    try {
      const saved = await api.setChannelMode({ channel, mode, rate_cap_hz: rateCapHz })
      set(current => ({ modes: { ...current.modes, [channel]: saved } }))
    } catch (err) {
      set({ modes: previous, error: (err as Error).message })
      throw err
    }
  },
  loadLiveTarget: async () => {
    try {
      const data = await api.fetchLiveTarget()
      set({ liveTarget: data.live_target || '' })
    } catch (err) {
      set({ error: (err as Error).message })
    }
  },
  setLiveTarget: async (target) => {
    await api.updateLiveTarget(target)
    set({ liveTarget: target })
  },
}))
