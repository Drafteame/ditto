import type { UpdateInfo } from '../types'
import * as api from '../api'
import { Download } from './icons'

interface UpdateBannerProps {
  info: UpdateInfo
  onDismiss: () => void
}

export function UpdateBanner({ info, onDismiss }: UpdateBannerProps) {
  const handleDownload = (e: React.MouseEvent) => {
    e.preventDefault()
    api.openUrl(info.download_url)
  }

  return (
    <div className="update">
      <Download />
      <span className="flex-1">
        Ditto {info.latest} is available (you have {info.current}).
      </span>
      <a href={info.download_url} onClick={handleDownload} className="link">
        Download
      </a>
      <button type="button" onClick={onDismiss} className="dismiss">
        Dismiss
      </button>
    </div>
  )
}
