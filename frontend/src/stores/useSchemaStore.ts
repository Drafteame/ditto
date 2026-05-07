import { create } from 'zustand'
import * as api from '../api'
import type { SchemaPack, SchemaTypeDescriptor } from '../types'

interface SchemaStore {
  packs: SchemaPack[]
  types: SchemaTypeDescriptor[]
  loading: boolean
  error: string
  loadSchemas: () => Promise<void>
  uploadPack: (file: File) => Promise<void>
}

export const useSchemaStore = create<SchemaStore>((set) => ({
  packs: [],
  types: [],
  loading: false,
  error: '',
  loadSchemas: async () => {
    set({ loading: true, error: '' })
    try {
      const [packs, types] = await Promise.all([
        api.fetchSchemaPacks(),
        api.fetchSchemaTypes(),
      ])
      set({ packs: packs.packs, types: types.types, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
    }
  },
  uploadPack: async (file) => {
    set({ loading: true, error: '' })
    try {
      await api.uploadSchemaPack(file)
      const [packs, types] = await Promise.all([
        api.fetchSchemaPacks(),
        api.fetchSchemaTypes(),
      ])
      set({ packs: packs.packs, types: types.types, loading: false })
    } catch (err) {
      set({ loading: false, error: (err as Error).message })
      throw err
    }
  },
}))
