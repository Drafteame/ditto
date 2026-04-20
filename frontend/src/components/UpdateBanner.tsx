import type { UpdateInfo } from '../types'
import { Download } from './icons'

interface UpdateBannerProps {
  info: UpdateInfo
  onDismiss: () => void
}

export function UpdateBanner({ info, onDismiss }: UpdateBannerProps) {
  const handleDownload = (e: React.MouseEvent) => {
    e.preventDefault()
    window.open(info.download_url, '_blank')
  }

  return (
    <div className="update">
      <Download />
      <span className="flex-1">
        Ditto {info.latest} is available (you have {info.current}).
      </span>
      <button type="button" onClick={handleDownload} className="link">
        Download
      </button>
      <button type="button" onClick={onDismiss} className="dismiss">
        Dismiss
      </button>
    </div>
  )
}
