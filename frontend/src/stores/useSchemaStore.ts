import { create } from 'zustand'

interface SchemaStore {
  loadedPackIds: string[]
  setLoadedPackIds: (ids: string[]) => void
}

export const useSchemaStore = create<SchemaStore>((set) => ({
  loadedPackIds: [],
  setLoadedPackIds: (loadedPackIds) => set({ loadedPackIds }),
}))
