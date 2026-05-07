import { create } from 'zustand'
import * as api from '../api'
import type {
  EventSequence,
  PlayerEvent,
  PlayerState,
  SequencePlayRequest,
} from '../types'

interface SequenceStore {
  sequences: EventSequence[]
  playerStates: Record<string, PlayerState>
  waitingEvents: Record<string, PlayerEvent | undefined>
  loading: boolean
  error: string
  loadSequences: () => Promise<void>
  loadPlayerStates: () => Promise<void>
  saveSequence: (sequence: Partial<EventSequence>, id?: string) => Promise<EventSequence>
  deleteSequence: (id: string) => Promise<void>
  playSequence: (id: string, req?: SequencePlayRequest) => Promise<PlayerState>
  pauseSequence: (id: string) => Promise<PlayerState>
  stopSequence: (id: string) => Promise<PlayerState>
  seekSequence: (id: string, step: number) => Promise<PlayerState>
  setSequenceSpeed: (id: string, speed: number) => Promise<PlayerState>
  applyPlayerEvent: (event: PlayerEvent) => void
}

function mergeState(states: Record<string, PlayerState>, state: PlayerState) {
  return { ...states, [state.sequence_id]: state }
}

function sequenceErrorMessage(err: unknown) {
  const message = (err as Error).message
  if (message.toLowerCase().includes('active player')) {
    return 'Stop the player first.'
  }
  return message
}

export const useSequenceStore = create<SequenceStore>((set) => ({
  sequences: [],
  playerStates: {},
  waitingEvents: {},
  loading: false,
  error: '',
  loadSequences: async () => {
    set({ loading: true, error: '' })
    try {
      const [seqData, stateData] = await Promise.all([
        api.fetchSequences(),
        api.fetchSequenceStates().catch(() => ({ states: [] })),
      ])
      const playerStates: Record<string, PlayerState> = {}
      stateData.states.forEach(state => { playerStates[state.sequence_id] = state })
      set({ sequences: seqData.sequences, playerStates, waitingEvents: {}, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  loadPlayerStates: async () => {
    try {
      const data = await api.fetchSequenceStates()
      const playerStates: Record<string, PlayerState> = {}
      data.states.forEach(state => { playerStates[state.sequence_id] = state })
      set({ playerStates, waitingEvents: {} })
    } catch (err) {
      set({ error: (err as Error).message })
    }
  },
  saveSequence: async (sequence, id) => {
    set({ loading: true, error: '' })
    try {
      const saved = await api.saveSequence(sequence, id)
      const data = await api.fetchSequences()
      set({ sequences: data.sequences, loading: false })
      return saved
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
  deleteSequence: async (id) => {
    const previous = useSequenceStore.getState().sequences
    set({ sequences: previous.filter(sequence => sequence.id !== id), error: '' })
    try {
      await api.deleteSequence(id)
    } catch (err) {
      set({ sequences: previous, error: sequenceErrorMessage(err) })
      throw err
    }
  },
  playSequence: async (id, req = {}) => {
    const state = await api.playSequence(id, req)
    set(current => ({ playerStates: mergeState(current.playerStates, state) }))
    return state
  },
  pauseSequence: async (id) => {
    const state = await api.pauseSequence(id)
    set(current => ({ playerStates: mergeState(current.playerStates, state) }))
    return state
  },
  stopSequence: async (id) => {
    const state = await api.stopSequence(id)
    set(current => ({ playerStates: mergeState(current.playerStates, state) }))
    return state
  },
  seekSequence: async (id, step) => {
    const state = await api.seekSequence(id, { step })
    set(current => ({ playerStates: mergeState(current.playerStates, state) }))
    return state
  },
  setSequenceSpeed: async (id, speed) => {
    const state = await api.setSequenceSpeed(id, { speed })
    set(current => ({ playerStates: mergeState(current.playerStates, state) }))
    return state
  },
  applyPlayerEvent: (event) => {
    set(current => {
      const waitingEvents = { ...current.waitingEvents }
      if (event.type === 'waiting') {
        waitingEvents[event.sequence_id] = event
      } else if (event.type === 'step' || event.type === 'completed' || event.type === 'stopped' || event.type === 'error') {
        delete waitingEvents[event.sequence_id]
      }
      return {
        playerStates: mergeState(current.playerStates, event.state),
        waitingEvents,
      }
    })
  },
}))
