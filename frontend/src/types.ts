export interface LogEvent {
  timestamp: string
  type: 'MOCK' | 'PROXY' | 'MISS'
  method: string
  path: string
  status: number
  duration_ms: number
  response_body?: string
  mock_index?: number
  sequence_step?: number
  sequence_len?: number
}

export interface LogEntry extends LogEvent {
  id: string
}

export interface MockMatch {
  query?: Record<string, string>
  headers?: Record<string, string>
  body?: Record<string, unknown>
}

export type ResponseMode = 'static' | 'sequence'

export interface SequenceStep {
  status: number
  headers?: Record<string, string>
  body: unknown
  delay_ms?: number
}

export interface Sequence {
  steps: SequenceStep[]
  on_end: 'loop' | 'stay' | 'reset'
  current_step?: number
}

export interface Mock {
  method: string
  path: string
  status: number
  body: unknown
  headers?: Record<string, string>
  delay_ms?: number
  enabled: boolean
  match?: MockMatch
  response_mode?: ResponseMode
  sequence?: Sequence
}

export interface ServerInfo {
  port: number
  target: string
  https: boolean
  mocks_dir: string
  local_ips: string[]
  version: string
}

export interface MocksResponse {
  mocks: Mock[]
  info: ServerInfo
}

export interface UpdateInfo {
  current: string
  latest: string
  available: boolean
  download_url: string
}

export interface Toast {
  id: string
  message: string
  kind?: 'warn'
}
