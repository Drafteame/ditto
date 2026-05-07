type MainView = 'requests' | 'sockets' | 'templates' | 'sequences'

interface MainTabsProps {
  activeView: MainView
  connectedClientCount: number
  eventTemplateCount: number
  sequenceCount: number
  onChange: (view: MainView) => void
}

export function MainTabs({
  activeView,
  connectedClientCount,
  eventTemplateCount,
  sequenceCount,
  onChange,
}: MainTabsProps) {
  return (
    <div className="main-tabs">
      <button type="button" className={activeView === 'requests' ? 'active' : ''} onClick={() => onChange('requests')}>
        Requests
      </button>
      <button type="button" className={activeView === 'sockets' ? 'active' : ''} onClick={() => onChange('sockets')}>
        Sockets
        <span className="c">{connectedClientCount}</span>
      </button>
      <button type="button" className={activeView === 'templates' ? 'active' : ''} onClick={() => onChange('templates')}>
        Event Templates
        <span className="c">{eventTemplateCount}</span>
      </button>
      <button type="button" className={activeView === 'sequences' ? 'active' : ''} onClick={() => onChange('sequences')}>
        Sequences
        <span className="c">{sequenceCount}</span>
      </button>
    </div>
  )
}
