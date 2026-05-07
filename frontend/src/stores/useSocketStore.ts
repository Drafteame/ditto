import { create } from 'zustand'

interface SocketClient {
  id: string
  subscriptions: string[]
}

interface SocketStore {
  connectedClients: SocketClient[]
  setConnectedClients: (clients: SocketClient[]) => void
}

export const useSocketStore = create<SocketStore>((set) => ({
  connectedClients: [],
  setConnectedClients: (connectedClients) => set({ connectedClients }),
}))
