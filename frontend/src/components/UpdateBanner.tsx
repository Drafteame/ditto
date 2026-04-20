import type { UpdateInfo } from '../types'
import { Download } from './icons'

interface UpdateBannerProps {
  info: UpdateInfo
  onDismiss: () => void
}

export function UpdateBanner({ info, onDismiss }: UpdateBannerProps) {
  return (
    <div className="update">
      <Download />
      <span className="flex-1">
        Ditto {info.latest} is available (you have {info.current}).
      </span>
      <a href={info.download_url} target="_blank" rel="noreferrer" className="link">
        Download
      </a>
      <button type="button" onClick={onDismiss} className="dismiss">
        Dismiss
      </button>
    </div>
  )
}
