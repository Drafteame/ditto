import { create } from 'zustand'

interface ScenarioStore {
  activeScenarioId: string | null
  setActiveScenarioId: (id: string | null) => void
}

export const useScenarioStore = create<ScenarioStore>((set) => ({
  activeScenarioId: null,
  setActiveScenarioId: (activeScenarioId) => set({ activeScenarioId }),
}))
