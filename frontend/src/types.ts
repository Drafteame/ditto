export interface LogEvent {
  timestamp: string
  type: 'MOCK' | 'PROXY' | 'MISS' | 'SOCKET'
  method: string
  path: string
  status: number
  duration_ms: number
  response_body?: string
  source?: string
  request_headers?: Record<string, string[]>
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

export interface SocketClient {
  id: string
  adapter: string
  remote_addr: string
  connected_at: string
  subscriptions: string[]
}

export interface SocketClientsResponse {
  clients: SocketClient[]
}

export interface SocketDispatchRequest {
  channel: string
  payload: unknown
  adapter?: string
  type_name?: string
}

export interface AdapterProfileSummary {
  name: string
  base_adapter: string
  subprotocols: string[]
  type_aliases: Record<string, string>
}

export type AdapterProfilesResponse = AdapterProfileSummary[]

export interface SocketDispatchResult {
  delivered: number
  dropped?: string[]
  errors?: string[]
}

export interface EventTemplateVariable {
  name: string
  description?: string
  default?: string
}

export interface EventTemplate {
  version: number
  id: string
  name: string
  description?: string
  channel: string
  adapter?: string
  type_name?: string
  payload: unknown
  variables?: EventTemplateVariable[]
  created_at: string
  updated_at: string
}

export interface EventTemplatesResponse {
  templates: EventTemplate[]
}

export interface EventTemplateDispatchRequest {
  variables?: Record<string, unknown>
  channel_override?: string
  adapter_override?: string
}

export interface EventTemplateDispatchResult extends SocketDispatchResult {
  resolved_payload: unknown
  missing_variables?: string[]
  invalid_casts?: Array<{ name: string; kind: string; value: string }>
}

export type SequenceOnEnd = 'loop' | 'stay' | 'reset'

export interface EventSequenceStep {
  id: string
  name?: string
  delay_ms: number
  template_ref?: string
  channel?: string
  adapter?: string
  type_name?: string
  payload?: unknown
  vars_override?: Record<string, unknown>
}

export interface EventSequence {
  version: number
  id: string
  name: string
  description?: string
  steps: EventSequenceStep[]
  vars?: Record<string, unknown>
  on_end: SequenceOnEnd
  created_at: string
  updated_at: string
}

export interface EventSequencesResponse {
  sequences: EventSequence[]
}

export type PlayerStatus = 'idle' | 'playing' | 'paused' | 'completed' | 'stopped' | 'error'

export interface PlayerState {
  sequence_id: string
  status: PlayerStatus
  current_step: number
  total_steps: number
  speed: number
  started_at?: string
  updated_at: string
  last_error?: string
  last_dispatch_summary?: string
}

export interface PlayerEvent {
  type: 'state' | 'step' | 'waiting' | 'error' | 'completed' | 'stopped' | 'looped'
  state: PlayerState
  sequence_id: string
  step_id?: string
  step_index?: number
  delay_ms?: number
  dispatch_summary?: string
  error?: string
  at: string
}

export interface SequencePlayRequest {
  vars?: Record<string, unknown>
  start_step?: number
  speed?: number
}

export interface SequenceSeekRequest {
  step: number
}

export interface SequenceSpeedRequest {
  speed: number
}

export interface SequenceStatesResponse {
  states: PlayerState[]
}

export interface SchemaField {
  name: string
  json_name: string
  type: string
  number: number
  repeated: boolean
  map: boolean
  optional: boolean
  oneof?: string
  message_type?: string
  enum_type?: string
}

export interface SchemaTypeDescriptor {
  full_name: string
  name: string
  package: string
  file: string
  pack_id: string
  fields: SchemaField[]
  example_json: unknown
}

export interface SchemaPack {
  id: string
  name: string
  path: string
  loaded_at: string
  types: SchemaTypeDescriptor[]
}

export interface SchemaPacksResponse {
  packs: SchemaPack[]
}

export interface SchemaTypesResponse {
  types: SchemaTypeDescriptor[]
}
