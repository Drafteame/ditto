import { useEffect, useRef, useCallback } from 'react'
import * as api from '../api'
import { X } from './icons'

interface QRModalProps {
  onClose: () => void
}

export function QRModal({ onClose }: QRModalProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const urlRef = useRef<HTMLParagraphElement>(null)

  useEffect(() => {
    api
      .fetchQR()
      .then(async ({ blob, url }) => {
        const img = await createImageBitmap(blob)
        const canvas = canvasRef.current
        if (!canvas) return
        canvas.width = img.width
        canvas.height = img.height
        const ctx = canvas.getContext('2d')
        ctx?.drawImage(img, 0, 0)
        if (urlRef.current) urlRef.current.textContent = url
      })
      .catch(err => {
        console.error('Failed to generate QR code:', err)
      })
  }, [])

  const handleOverlayMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (e.target === e.currentTarget) onClose()
    },
    [onClose],
  )

  return (
    <div onMouseDown={handleOverlayMouseDown} className="modal-scrim">
      <div
        onMouseDown={e => e.stopPropagation()}
        className="modal"
        style={{ width: 360 }}
      >
        <div className="modal-head">
          <h2>Open on your phone</h2>
          <div className="flex-1" />
          <button type="button" className="btn ghost icon" onClick={onClose} aria-label="Close">
            <X />
          </button>
        </div>
        <div className="modal-body text-center">
          <p className="text-[12.5px] text-fg-2 mb-4 leading-relaxed">
            Scan this QR code with your phone camera to open the Ditto dashboard.
          </p>
          <canvas ref={canvasRef} className="rounded-md bg-white p-3 mx-auto" />
          <p
            ref={urlRef}
            className="font-mono text-[11px] text-accent mt-4 break-all"
          />
        </div>
      </div>
    </div>
  )
}
