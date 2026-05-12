import { create } from 'zustand'
import * as api from '../api'
import type {
  EventTemplate,
  EventTemplateDispatchRequest,
  EventTemplateDispatchResult,
} from '../types'

interface EventTemplateStore {
  templates: EventTemplate[]
  loading: boolean
  error: string
  loadTemplates: () => Promise<void>
  saveTemplate: (template: Partial<EventTemplate>, id?: string) => Promise<EventTemplate>
  deleteTemplate: (id: string) => Promise<void>
  dispatchTemplate: (
    id: string,
    variables?: Record<string, unknown>,
    overrides?: Pick<EventTemplateDispatchRequest, 'channel_override' | 'adapter_override'>,
  ) => Promise<EventTemplateDispatchResult>
}

export const useEventTemplateStore = create<EventTemplateStore>((set) => ({
  templates: [],
  loading: false,
  error: '',
  loadTemplates: async () => {
    set({ loading: true, error: '' })
    try {
      const data = await api.fetchEventTemplates()
      set({ templates: data.templates, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  saveTemplate: async (template, id) => {
    set({ loading: true, error: '' })
    try {
      const saved = await api.saveEventTemplate(template, id)
      const data = await api.fetchEventTemplates()
      set({ templates: data.templates, loading: false })
      return saved
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
  deleteTemplate: async (id) => {
    set({ loading: true, error: '' })
    try {
      await api.deleteEventTemplate(id)
      const data = await api.fetchEventTemplates()
      set({ templates: data.templates, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
  dispatchTemplate: async (id, variables = {}, overrides = {}) => {
    try {
      return await api.dispatchEventTemplate(id, { variables, ...overrides })
    } catch (err) {
      set({ error: (err as Error).message })
      throw err
    }
  },
}))
