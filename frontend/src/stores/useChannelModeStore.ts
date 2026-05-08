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
  addChannel: (channel: string, mode?: ChannelMode, rateCapHz?: number) => Promise<void>
  deleteChannel: (channel: string) => Promise<void>
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
      const channels = Array.isArray(data.channels) ? data.channels : []
      channels.forEach(config => { modes[config.channel] = config })
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
      set(current => {
        const modes = { ...current.modes }
        modes[channel] = saved
        return { modes }
      })
    } catch (err) {
      set({ modes: previous, error: (err as Error).message })
      throw err
    }
  },
  addChannel: async (channel, mode = 'mock', rateCapHz = 0) => {
    await get().setMode(channel, mode, rateCapHz)
  },
  deleteChannel: async (channel) => {
    const previous = get().modes
    set(current => {
      const modes = { ...current.modes }
      delete modes[channel]
      return { modes, error: '' }
    })
    try {
      await api.deleteChannelMode(channel)
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
