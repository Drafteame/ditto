import { useState, useCallback } from 'react'
import type { Mock, ServerInfo } from '../types'
import * as api from '../api'

interface SidebarProps {
  open: boolean
  mocks: Mock[]
  serverInfo: ServerInfo | null
  onClose: () => void
  onMocksChanged: () => void
  onEditMock: (index: number) => void
  onCreateMock: () => void
  showToast: (message: string, kind?: 'warn') => void
}

export function Sidebar({
  open,
  mocks,
  serverInfo,
  onClose,
  onMocksChanged,
  onEditMock,
  onCreateMock,
  showToast,
}: SidebarProps) {
  return (
    <>
      {/* Overlay for mobile */}
      <div
        className={`fixed inset-0 bg-black/40 z-40 md:hidden ${open ? 'block' : 'hidden'}`}
        onClick={onClose}
      />
      <aside
        className={`
          w-[300px] min-w-[300px] border-r border-dt-border bg-dt-surface flex flex-col overflow-hidden
          max-md:fixed max-md:left-[-300px] max-md:top-0 max-md:h-screen max-md:z-50 max-md:transition-[left] max-md:duration-200
          ${open ? 'max-md:!left-0 max-md:shadow-[4px_0_20px_rgba(0,0,0,0.4)]' : ''}
        `}
      >
        {/* Close button (mobile) */}
        <div className="hidden max-md:flex justify-end px-3 pt-2">
          <button
            className="bg-transparent border-none text-dt-muted text-2xl cursor-pointer px-1.5 leading-none hover:text-dt-text"
            onClick={onClose}
          >
            &times;
          </button>
        </div>

        <PortPanel serverInfo={serverInfo} showToast={showToast} />
        <TargetPanel serverInfo={serverInfo} onChanged={onMocksChanged} />
        <ConnectPanel serverInfo={serverInfo} />
        <MocksPanel
          mocks={mocks}
          onMocksChanged={onMocksChanged}
          onEditMock={onEditMock}
          onCreateMock={onCreateMock}
          showToast={showToast}
        />
      </aside>
    </>
  )
}

// --- Port Panel ---

function PortPanel({
  serverInfo,
  showToast,
}: {
  serverInfo: ServerInfo | null
  showToast: (message: string, kind?: 'warn') => void
}) {
  const [portValue, setPortValue] = useState<string | null>(null)
  const [portError, setPortError] = useState('')
  const [suggestions, setSuggestions] = useState<number[]>([])

  const displayPort = portValue ?? String(serverInfo?.port ?? 8888)

  const handleChangePort = useCallback(async () => {
    const port = parseInt(displayPort)
    if (!port || port < 1024 || port > 65535) {
      setPortError('Port must be between 1024 and 65535')
      return
    }
    setPortError('')
    setSuggestions([])

    try {
      const data = await api.changePort(port)
      if (data.error) {
        setPortError(data.error)
        if (data.suggestions?.length) setSuggestions(data.suggestions)
        return
      }
      showToast(`Port changed to ${data.port}, reconnecting...`)
      await api.waitForPort(data.port!)
      window.location.href = `http://localhost:${data.port}/__ditto__/`
    } catch (err) {
      setPortError(`Failed to change port: ${(err as Error).message}`)
    }
  }, [portValue, displayPort, showToast])

  const selectPort = useCallback((p: number) => {
    setPortValue(String(p))
    setPortError('')
    setSuggestions([])
    // Auto-submit
    api.changePort(p).then(async (data) => {
      if (data.port) {
        showToast(`Port changed to ${data.port}, reconnecting...`)
        await api.waitForPort(data.port)
        window.location.href = `http://localhost:${data.port}/__ditto__/`
      }
    })
  }, [showToast])

  return (
    <div className="border-b border-dt-border">
      <h2 className="sidebar-heading !border-b-0 !pb-1">Port</h2>
      <div className="flex gap-1.5 px-4 pb-2">
        <input
          type="number"
          min={1024}
          max={65535}
          value={displayPort}
          onChange={e => setPortValue(e.target.value)}
          className="w-20 bg-dt-bg border border-dt-border text-dt-text px-2.5 py-1.5 rounded-md text-xs font-mono focus:outline-none focus:border-dt-accent"
          onKeyDown={e => e.key === 'Enter' && handleChangePort()}
        />
        <button onClick={handleChangePort} className="btn text-xs px-3 py-1.5">
          Set
        </button>
      </div>
      {portError && (
        <div className="px-4 pb-2 text-[11px] text-dt-red">{portError}</div>
      )}
      {suggestions.length > 0 && (
        <div className="px-4 pb-3 flex gap-1.5 flex-wrap">
          {suggestions.map(p => (
            <button
              key={p}
              onClick={() => selectPort(p)}
              className="bg-dt-bg border border-dt-border text-dt-accent px-2.5 py-0.5 rounded text-[11px] font-mono cursor-pointer hover:bg-[rgba(88,166,255,0.1)] hover:border-dt-accent"
            >
              {p}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// --- Target Panel ---

function TargetPanel({
  serverInfo,
  onChanged,
}: {
  serverInfo: ServerInfo | null
  onChanged: () => void
}) {
  const [targetValue, setTargetValue] = useState<string | null>(null)
  const displayTarget = targetValue ?? serverInfo?.target ?? ''

  const handleUpdate = useCallback(async () => {
    const url = displayTarget.trim()
    if (!url) return
    try {
      await api.updateTarget(url)
      onChanged()
    } catch (err) {
      alert('Failed to set target: ' + (err as Error).message)
    }
  }, [targetValue, displayTarget, onChanged])

  return (
    <div className="border-b border-dt-border">
      <h2 className="sidebar-heading !border-b-0 !pb-1">Target URL</h2>
      <div className="flex gap-1.5 px-4 pb-3.5">
        <input
          type="text"
          value={displayTarget}
          onChange={e => setTargetValue(e.target.value)}
          placeholder="https://api.example.com"
          className="flex-1 bg-dt-bg border border-dt-border text-dt-text px-2.5 py-1.5 rounded-md text-xs font-mono focus:outline-none focus:border-dt-accent"
          onKeyDown={e => e.key === 'Enter' && handleUpdate()}
        />
        <button onClick={handleUpdate} className="btn text-xs px-3 py-1.5">
          Set
        </button>
      </div>
    </div>
  )
}

// --- Connect Panel ---

function ConnectPanel({ serverInfo }: { serverInfo: ServerInfo | null }) {
  const [copiedUrl, setCopiedUrl] = useState<string | null>(null)

  if (!serverInfo) return null

  const scheme = serverInfo.https ? 'https' : 'http'
  const urls = [
    { label: 'Android emulator', url: `${scheme}://10.0.2.2:${serverInfo.port}` },
    { label: 'iOS simulator', url: `${scheme}://localhost:${serverInfo.port}` },
    ...(serverInfo.local_ips?.length
      ? [{ label: 'Physical device', url: `${scheme}://${serverInfo.local_ips[0]}:${serverInfo.port}` }]
      : []),
  ]

  const copyUrl = (url: string) => {
    navigator.clipboard.writeText(url).then(() => {
      setCopiedUrl(url)
      setTimeout(() => setCopiedUrl(null), 1200)
    })
  }

  return (
    <div className="border-b border-dt-border">
      <h2 className="sidebar-heading !border-b-0 !pb-1">Connect your app</h2>
      <div className="px-4 pb-3.5">
        {urls.map(({ label, url }) => (
          <div key={label} className="flex items-center justify-between gap-2 mb-1.5">
            <span className="text-[11px] text-dt-muted min-w-[90px] shrink-0">{label}</span>
            <span
              onClick={() => copyUrl(url)}
              title="Click to copy"
              className={`font-mono text-xs px-2 py-0.5 rounded cursor-pointer select-all break-all flex-1 ${
                copiedUrl === url
                  ? 'text-dt-green bg-[rgba(63,185,80,0.1)]'
                  : 'text-dt-accent bg-[rgba(88,166,255,0.08)] hover:bg-[rgba(88,166,255,0.15)]'
              }`}
            >
              {copiedUrl === url ? 'Copied!' : url}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

// --- Mocks Panel ---

function MocksPanel({
  mocks,
  onMocksChanged,
  onEditMock,
  onCreateMock,
  showToast,
}: {
  mocks: Mock[]
  onMocksChanged: () => void
  onEditMock: (index: number) => void
  onCreateMock: () => void
  showToast: (message: string, kind?: 'warn') => void
}) {
  const handleToggle = useCallback(async (index: number) => {
    try {
      const result = await api.toggleMock(index)
      if (result.disabled_duplicates?.length) {
        showToast(`${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`, 'warn')
      }
      onMocksChanged()
    } catch (err) {
      console.error('Failed to toggle mock:', err)
    }
  }, [onMocksChanged, showToast])

  const handleDelete = useCallback(async (index: number) => {
    if (!confirm('Delete this mock? The JSON file will be removed.')) return
    try {
      await api.deleteMock(index)
      onMocksChanged()
    } catch (err) {
      console.error('Failed to delete mock:', err)
    }
  }, [onMocksChanged])

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <h2 className="sidebar-heading">
        Mocks
        <span className="bg-dt-border text-dt-muted text-[11px] px-[7px] py-px rounded-[10px] font-semibold">
          {mocks.length}
        </span>
        <button
          onClick={onCreateMock}
          title="Create new mock"
          className="ml-auto bg-dt-border hover:bg-[#3d444d] text-dt-text border border-dt-border rounded px-2 py-0.5 text-xs cursor-pointer"
        >
          +
        </button>
      </h2>
      <ul className="list-none overflow-y-auto flex-1">
        {mocks.map((mock, index) => (
          <MockItem
            key={index}
            mock={mock}
            index={index}
            onToggle={handleToggle}
            onEdit={onEditMock}
            onDelete={handleDelete}
          />
        ))}
      </ul>
    </div>
  )
}

const METHOD_COLORS: Record<string, string> = {
  get: 'text-dt-green',
  post: 'text-dt-accent',
  put: 'text-dt-orange',
  delete: 'text-dt-red',
  patch: 'text-dt-orange',
}

function MockItem({
  mock,
  index,
  onToggle,
  onEdit,
  onDelete,
}: {
  mock: Mock
  index: number
  onToggle: (i: number) => void
  onEdit: (i: number) => void
  onDelete: (i: number) => void
}) {
  const methodColor = METHOD_COLORS[mock.method.toLowerCase()] || 'text-dt-text'
  const pills = getMatchPills(mock.match)

  return (
    <li
      className={`flex items-center gap-2 px-4 py-2 border-b border-dt-border transition-colors hover:bg-[rgba(88,166,255,0.06)] flex-wrap ${
        !mock.enabled ? 'opacity-40' : ''
      }`}
    >
      <label className="toggle" onClick={e => e.stopPropagation()}>
        <input
          type="checkbox"
          checked={mock.enabled}
          onChange={() => onToggle(index)}
        />
        <span className="slider" />
      </label>
      <span className={`text-[11px] font-bold font-mono min-w-[44px] ${methodColor}`}>
        {mock.method}
      </span>
      <span
        className="text-xs font-mono whitespace-nowrap overflow-hidden text-ellipsis flex-1 cursor-pointer hover:text-dt-accent"
        title={mock.path}
        onClick={() => onEdit(index)}
      >
        {mock.path}
      </span>
      <div className="flex gap-1">
        <button
          className="bg-transparent border-none text-dt-muted cursor-pointer px-1 py-0.5 text-sm rounded hover:bg-dt-border hover:text-dt-text"
          onClick={() => onEdit(index)}
          title="Edit"
        >
          &#9998;
        </button>
        <button
          className="bg-transparent border-none text-dt-muted cursor-pointer px-1 py-0.5 text-sm rounded hover:bg-[rgba(248,81,73,0.15)] hover:text-dt-red"
          onClick={() => onDelete(index)}
          title="Delete"
        >
          &#10005;
        </button>
      </div>
      {pills.length > 0 && (
        <div className="flex flex-wrap gap-1 w-full pl-10 pb-0.5">
          {pills.map((pill, i) => (
            <span
              key={i}
              className="text-[10px] font-mono bg-[rgba(210,153,34,0.12)] text-dt-orange px-1.5 py-px rounded whitespace-nowrap max-w-[200px] overflow-hidden text-ellipsis"
              title={pill}
            >
              {pill}
            </span>
          ))}
        </div>
      )}
    </li>
  )
}

function getMatchPills(match?: Mock['match']): string[] {
  if (!match) return []
  const pills: string[] = []
  if (match.query) {
    Object.entries(match.query).forEach(([k, v]) => pills.push(`?${k}=${v}`))
  }
  if (match.headers) {
    Object.entries(match.headers).forEach(([k, v]) => pills.push(`${k}: ${v}`))
  }
  if (match.body && Object.keys(match.body).length > 0) {
    pills.push(`body: ${JSON.stringify(match.body)}`)
  }
  return pills
}
