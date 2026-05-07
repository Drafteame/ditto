import { create } from 'zustand'
import type { MockEditorState } from '../components/MockEditorModal'
import type { UpdateInfo } from '../types'

interface AppUiStore {
  sidebarOpen: boolean
  sidebarCollapsed: boolean
  drawerWidth: number
  updateInfo: UpdateInfo | null
  modalState: MockEditorState | null
  qrOpen: boolean
  setSidebarOpen: (open: boolean) => void
  toggleSidebarOpen: () => void
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
  setDrawerWidth: (width: number) => void
  setUpdateInfo: (info: UpdateInfo | null) => void
  setModalState: (state: MockEditorState | null) => void
  setQrOpen: (open: boolean) => void
}

export const useAppUiStore = create<AppUiStore>((set) => ({
  sidebarOpen: false,
  sidebarCollapsed: false,
  drawerWidth: 480,
  updateInfo: null,
  modalState: null,
  qrOpen: false,

  setSidebarOpen: (sidebarOpen) => set({ sidebarOpen }),
  toggleSidebarOpen: () => set((state) => ({ sidebarOpen: !state.sidebarOpen })),
  setSidebarCollapsed: (sidebarCollapsed) => set({ sidebarCollapsed }),
  toggleSidebarCollapsed: () => set((state) => ({ sidebarCollapsed: !state.sidebarCollapsed })),
  setDrawerWidth: (drawerWidth) => set({ drawerWidth }),
  setUpdateInfo: (updateInfo) => set({ updateInfo }),
  setModalState: (modalState) => set({ modalState }),
  setQrOpen: (qrOpen) => set({ qrOpen }),
}))
