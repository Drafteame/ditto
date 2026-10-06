import type { LogPayloadMetadata } from '../types'
import { Download } from './icons'

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`
}

export function decodePayloadText(metadata?: LogPayloadMetadata): string | null {
  if (!metadata?.raw_base64) return null
  try {
    const binary = atob(metadata.raw_base64)
    return new TextDecoder().decode(Uint8Array.from(binary, char => char.charCodeAt(0)))
  } catch {
    return null
  }
}

export function DownloadCapture({ metadata, filename }: { metadata?: LogPayloadMetadata; filename: string }) {
  if (!metadata?.raw_base64) return null
  return <button type="button" className="btn ghost" onClick={() => {
    const binary = atob(metadata.raw_base64!)
    const bytes = Uint8Array.from(binary, char => char.charCodeAt(0))
    const url = URL.createObjectURL(new Blob([bytes], { type: metadata.content_type || 'application/octet-stream' }))
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    link.click()
    URL.revokeObjectURL(url)
  }}><Download /> Download captured bytes</button>
}
