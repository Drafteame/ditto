import type {
  Mock,
  MocksResponse,
  EventTemplate,
  EventTemplateDispatchRequest,
  EventTemplateDispatchResult,
  EventTemplatesResponse,
  SchemaPacksResponse,
  SchemaPack,
  SchemaTypesResponse,
  SocketClientsResponse,
  SocketDispatchRequest,
  SocketDispatchResult,
  UpdateInfo,
} from './types'

const API_BASE = '/__ditto__/api'

export async function fetchMocks(): Promise<MocksResponse> {
  const res = await fetch(`${API_BASE}/mocks`)
  return res.json()
}

export async function toggleMock(index: number): Promise<{ disabled_duplicates?: string[] }> {
  const res = await fetch(`${API_BASE}/mocks/${index}/toggle`, { method: 'POST' })
  return res.json().catch(() => ({}))
}

export async function reloadMocks(): Promise<void> {
  await fetch(`${API_BASE}/mocks/reload`, { method: 'POST' })
}

export async function deleteMock(index: number): Promise<void> {
  const res = await fetch(`${API_BASE}/mocks/${index}`, { method: 'DELETE' })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
}

export async function resetSequence(index: number): Promise<void> {
  const res = await fetch(`${API_BASE}/mocks/${index}/sequence/reset`, { method: 'POST' })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
}

export async function saveMock(
  mock: Omit<Mock, 'enabled'>,
  editingIndex: number | null
): Promise<{ disabled_duplicates?: string[] }> {
  const url = editingIndex !== null
    ? `${API_BASE}/mocks/${editingIndex}`
    : `${API_BASE}/mocks`
  const method = editingIndex !== null ? 'PUT' : 'POST'

  const res = await fetch(url, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(mock),
  })

  if (!res.ok) {
    const text = await res.text()
    throw new Error(text)
  }

  return res.json().catch(() => ({}))
}

export async function updateTarget(target: string): Promise<void> {
  const res = await fetch(`${API_BASE}/target/save`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ target }),
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text)
  }
}

export async function changePort(port: number): Promise<{
  port?: number
  error?: string
  suggestions?: number[]
}> {
  const res = await fetch(`${API_BASE}/port`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ port }),
  })
  return res.json()
}

export async function fetchUpdateCheck(): Promise<UpdateInfo> {
  const res = await fetch(`${API_BASE}/update-check`)
  return res.json()
}

export async function fetchQR(): Promise<{ blob: Blob; url: string }> {
  const res = await fetch(`${API_BASE}/qr`)
  const url = res.headers.get('X-Ditto-QR-URL') || ''
  const blob = await res.blob()
  return { blob, url }
}

export async function openInBrowser(): Promise<void> {
  try {
    await fetch(`${API_BASE}/open-browser`, { method: 'POST' })
  } catch {
    window.open(window.location.href, '_blank')
  }
}

export async function openUrl(url: string): Promise<void> {
  try {
    await fetch(`${API_BASE}/open-url`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    })
  } catch {
    window.open(url, '_blank')
  }
}

export async function fetchSocketClients(): Promise<SocketClientsResponse> {
  const res = await fetch(`${API_BASE}/socket/clients`)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function dispatchSocketEvent(req: SocketDispatchRequest): Promise<SocketDispatchResult> {
  const res = await fetch(`${API_BASE}/socket/dispatch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function fetchEventTemplates(): Promise<EventTemplatesResponse> {
  const res = await fetch(`${API_BASE}/event-templates`)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function fetchEventTemplate(id: string): Promise<EventTemplate> {
  const res = await fetch(`${API_BASE}/event-templates/${encodeURIComponent(id)}`)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function saveEventTemplate(template: Partial<EventTemplate>, id?: string): Promise<EventTemplate> {
  const url = id
    ? `${API_BASE}/event-templates/${encodeURIComponent(id)}`
    : `${API_BASE}/event-templates`
  const res = await fetch(url, {
    method: id ? 'PUT' : 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(template),
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function deleteEventTemplate(id: string): Promise<void> {
  const res = await fetch(`${API_BASE}/event-templates/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
}

export async function dispatchEventTemplate(
  id: string,
  req: EventTemplateDispatchRequest,
): Promise<EventTemplateDispatchResult> {
  const res = await fetch(`${API_BASE}/event-templates/${encodeURIComponent(id)}/dispatch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  const text = await res.text().catch(() => '')
  let data: EventTemplateDispatchResult | null = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = null
  }
  if (!res.ok) {
    const missing = data?.missing_variables?.length
      ? `Missing variables: ${data.missing_variables.join(', ')}`
      : ''
    const invalid = data?.invalid_casts?.length
      ? `Invalid casts: ${data.invalid_casts.map(item => `${item.kind}:${item.name}`).join(', ')}`
      : ''
    throw new Error([missing, invalid, text || `HTTP ${res.status}`].filter(Boolean).join(' / '))
  }
  if (!data) throw new Error(`HTTP ${res.status}`)
  return data
}

export async function fetchSchemaPacks(): Promise<SchemaPacksResponse> {
  const res = await fetch(`${API_BASE}/schemas/packs`)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function fetchSchemaTypes(): Promise<SchemaTypesResponse> {
  const res = await fetch(`${API_BASE}/schemas/types`)
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function uploadSchemaPack(file: File): Promise<SchemaPack> {
  const body = new FormData()
  body.append('pack', file)
  const res = await fetch(`${API_BASE}/schemas/packs`, {
    method: 'POST',
    body,
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
  return res.json()
}

export async function deleteSchemaPack(id: string): Promise<void> {
  const res = await fetch(`${API_BASE}/schemas/packs/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(text || `HTTP ${res.status}`)
  }
}

export async function waitForPort(port: number, maxAttempts = 30): Promise<void> {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      await fetch(`http://localhost:${port}/__ditto__/api/mocks`, { mode: 'no-cors' })
      return
    } catch {
      await new Promise(r => setTimeout(r, 200))
    }
  }
}
