import { useState, useCallback } from 'react'
import type { Mock, ServerInfo } from '../types'
import * as api from '../api'
import { ChevronLeft, ChevronRight, Copy, Edit, Trash, X } from './icons'
import { useConfirm } from './ConfirmDialog'

interface SidebarProps {
  open: boolean
  collapsed: boolean
  mocks: Mock[]
  serverInfo: ServerInfo | null
  onClose: () => void
  onCollapse: () => void
  onMocksChanged: () => void
  onEditMock: (index: number) => void
  onCreateMock: () => void
  showToast: (message: string, kind?: 'warn') => void
}

export function Sidebar({
  open,
  collapsed,
  mocks,
  serverInfo,
  onClose,
  onCollapse,
  onMocksChanged,
  onEditMock,
  onCreateMock,
  showToast,
}: SidebarProps) {
  return (
    <>
      {/* Overlay for mobile */}
      <div
        className={`fixed inset-0 bg-black/50 z-40 md:hidden ${open ? 'block' : 'hidden'}`}
        onClick={onClose}
      />
      <aside
        className={`
          w-[300px] min-w-[300px] border-r border-line bg-bg-1 flex flex-col overflow-hidden flex-shrink-0
          max-md:fixed max-md:left-[-320px] max-md:top-0 max-md:h-screen max-md:z-50 max-md:transition-[left] max-md:duration-200
          ${open ? 'max-md:!left-0 max-md:shadow-lg' : ''}
          ${collapsed ? 'md:hidden' : ''}
        `}
      >
        {/* Close button (mobile) */}
        <div className="hidden max-md:flex justify-end px-2 pt-2">
          <button type="button" className="btn ghost icon" onClick={onClose} aria-label="Close sidebar">
            <X />
          </button>
        </div>

        <PortPanel serverInfo={serverInfo} showToast={showToast} onCollapse={onCollapse} />
        <TargetPanel serverInfo={serverInfo} onChanged={onMocksChanged} showToast={showToast} />
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

export function CollapsedSidebarRail({ onExpand }: { onExpand: () => void }) {
  return (
    <div className="sidebar-rail max-md:hidden">
      <button
        type="button"
        onClick={onExpand}
        data-tip="Expand sidebar (⌘\)"
        data-tip-side="right"
        aria-label="Expand sidebar"
      >
        <ChevronRight />
      </button>
    </div>
  )
}

// --- Port Panel ---

function PortPanel({
  serverInfo,
  showToast,
  onCollapse,
}: {
  serverInfo: ServerInfo | null
  showToast: (message: string, kind?: 'warn') => void
  onCollapse: () => void
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
  }, [displayPort, showToast])

  const selectPort = useCallback(
    (p: number) => {
      setPortValue(String(p))
      setPortError('')
      setSuggestions([])
      api.changePort(p).then(async data => {
        if (data.port) {
          showToast(`Port changed to ${data.port}, reconnecting...`)
          await api.waitForPort(data.port)
          window.location.href = `http://localhost:${data.port}/__ditto__/`
        }
      })
    },
    [showToast],
  )

  return (
    <div className="sb-section">
      <div className="sb-label">
        <span>Port</span>
        <button
          type="button"
          className="sb-collapse-btn max-md:hidden"
          onClick={onCollapse}
          data-tip="Collapse sidebar (⌘\)"
          data-tip-side="left"
          aria-label="Collapse sidebar"
        >
          <ChevronLeft size={14} />
        </button>
      </div>
      <div className="field">
        <input
          type="number"
          min={1024}
          max={65535}
          value={displayPort}
          onChange={e => setPortValue(e.target.value)}
          className="input"
          onKeyDown={e => e.key === 'Enter' && handleChangePort()}
        />
        <button type="button" onClick={handleChangePort} className="btn">
          Set
        </button>
      </div>
      {portError && <div className="mt-2 text-[11px] text-err">{portError}</div>}
      {suggestions.length > 0 && (
        <div className="mt-2 flex gap-1.5 flex-wrap">
          {suggestions.map(p => (
            <button
              key={p}
              type="button"
              onClick={() => selectPort(p)}
              className="bg-bg-0 border border-line text-accent px-2 py-0.5 rounded-sm text-[11px] font-mono cursor-pointer hover:border-accent hover:bg-[color-mix(in_oklch,var(--accent)_12%,transparent)]"
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
  showToast,
}: {
  serverInfo: ServerInfo | null
  onChanged: () => void
  showToast: (message: string, kind?: 'warn') => void
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
      showToast(`Failed to set target: ${(err as Error).message}`, 'warn')
    }
  }, [displayTarget, onChanged, showToast])

  return (
    <div className="sb-section">
      <div className="sb-label">Target URL</div>
      <div className="field">
        <input
          type="text"
          value={displayTarget}
          onChange={e => setTargetValue(e.target.value)}
          placeholder="https://api.example.com"
          className="input"
          onKeyDown={e => e.key === 'Enter' && handleUpdate()}
        />
        <button type="button" onClick={handleUpdate} className="btn">
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
    { label: 'Android', url: `${scheme}://10.0.2.2:${serverInfo.port}` },
    { label: 'iOS sim', url: `${scheme}://localhost:${serverInfo.port}` },
    ...(serverInfo.local_ips?.length
      ? [{ label: 'Device', url: `${scheme}://${serverInfo.local_ips[0]}:${serverInfo.port}` }]
      : []),
  ]

  const copyUrl = (url: string) => {
    navigator.clipboard.writeText(url).then(() => {
      setCopiedUrl(url)
      setTimeout(() => setCopiedUrl(null), 1200)
    })
  }

  return (
    <div className="sb-section">
      <div className="sb-label">Connect your app</div>
      <div className="connect">
        {urls.map(({ label, url }) => {
          const isCopied = copiedUrl === url
          return (
            <div key={label} className="contents">
              <span className="label">{label}</span>
              <span
                onClick={() => copyUrl(url)}
                title="Click to copy"
                className={`url ${isCopied ? 'copied' : ''}`}
              >
                {isCopied ? 'Copied!' : url}
              </span>
              <button type="button" className="copy" onClick={() => copyUrl(url)} title="Copy">
                <Copy />
              </button>
            </div>
          )
        })}
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
  const handleToggle = useCallback(
    async (index: number) => {
      try {
        const result = await api.toggleMock(index)
        if (result.disabled_duplicates?.length) {
          showToast(`${result.disabled_duplicates.length} duplicate mock(s) auto-disabled`, 'warn')
        }
        onMocksChanged()
      } catch (err) {
        console.error('Failed to toggle mock:', err)
      }
    },
    [onMocksChanged, showToast],
  )

  const confirm = useConfirm()

  const handleDelete = useCallback(
    async (index: number, mock: Mock) => {
      const ok = await confirm({
        title: 'Delete mock?',
        message: (
          <>
            <code className="font-mono text-fg-0">
              {mock.method.toUpperCase()} {mock.path}
            </code>{' '}
            will be removed and its JSON file deleted from disk.
          </>
        ),
        confirmLabel: 'Delete',
        danger: true,
      })
      if (!ok) return
      try {
        await api.deleteMock(index)
        onMocksChanged()
        showToast('Mock deleted')
      } catch (err) {
        console.error('Failed to delete mock:', err)
        showToast(`Failed to delete mock: ${(err as Error).message}`, 'warn')
      }
    },
    [confirm, onMocksChanged, showToast],
  )

  return (
    <div className="flex-1 flex flex-col overflow-hidden min-h-0">
      <div className="sb-section pb-2">
        <div className="sb-label">
          <span>
            Mocks <span className="count">{mocks.length}</span>
          </span>
          <button type="button" className="link" onClick={onCreateMock} title="Create new mock">
            + New
          </button>
        </div>
      </div>
      <div className="flex-1 overflow-y-auto min-h-0">
        {mocks.length === 0 ? (
          <div className="px-4 py-6 text-center text-[11.5px] text-fg-3">
            No mocks yet. Click <span className="text-accent font-semibold">+ New</span> to create one.
          </div>
        ) : (
          mocks.map((mock, index) => (
            <MockItem
              key={index}
              mock={mock}
              index={index}
              onToggle={handleToggle}
              onEdit={onEditMock}
              onDelete={handleDelete}
            />
          ))
        )}
      </div>
    </div>
  )
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
  onDelete: (i: number, mock: Mock) => void
}) {
  const methodUpper = mock.method.toUpperCase()
  const pills = getMatchPills(mock.match)

  return (
    <div className={`mock-row ${mock.enabled ? '' : 'disabled'}`} onClick={() => onEdit(index)}>
      <button
        type="button"
        className={`switch ${mock.enabled ? 'on' : ''}`}
        onClick={e => {
          e.stopPropagation()
          onToggle(index)
        }}
        aria-label={mock.enabled ? 'Disable mock' : 'Enable mock'}
      />
      <span className={`method ${methodUpper}`}>{methodUpper}</span>
      <span className="mock-path" title={mock.path}>
        {mock.path}
      </span>
      <div className="mock-actions">
        <button
          type="button"
          className="icon-btn"
          onClick={e => {
            e.stopPropagation()
            onEdit(index)
          }}
          title="Edit mock"
          aria-label="Edit"
        >
          <Edit size={14} />
        </button>
        <button
          type="button"
          className="icon-btn danger"
          onClick={e => {
            e.stopPropagation()
            onDelete(index, mock)
          }}
          title="Delete mock"
          aria-label="Delete"
        >
          <Trash size={14} />
        </button>
      </div>
      {pills.length > 0 && (
        <div className="mock-match-pills">
          {pills.map((pill, i) => (
            <span key={i} className="pill-q" title={pill}>
              {pill}
            </span>
          ))}
        </div>
      )}
    </div>
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
