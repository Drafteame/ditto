import type { EventSequence, PlayerEvent, PlayerState } from '../types'
import { Pause, Play, Stop } from './icons'

const SPEEDS = [
  { label: '0.5x', value: 0.5 },
  { label: '1x', value: 1 },
  { label: '2x', value: 2 },
  { label: '5x', value: 5 },
  { label: '10x', value: 10 },
  { label: 'Max', value: 0 },
]

interface SequencePlayerViewProps {
  sequence: EventSequence
  state?: PlayerState
  waitingEvent?: PlayerEvent
  onPlay: () => Promise<void>
  onPause: () => Promise<void>
  onStop: () => Promise<void>
  onSeek: (step: number) => Promise<void>
  onSpeed: (speed: number) => Promise<void>
}

export function SequencePlayerView({
  sequence,
  state,
  waitingEvent,
  onPlay,
  onPause,
  onStop,
  onSeek,
  onSpeed,
}: SequencePlayerViewProps) {
  const status = state?.status ?? 'idle'
  const current = state?.current_step ?? 0
  const speed = state?.speed ?? 1
  const maxDelay = Math.max(...sequence.steps.map(step => step.delay_ms || 0), 1)
  const canPause = status === 'playing'
  const canStop = status === 'playing' || status === 'paused'
  const waitingDelay = waitingEvent?.step_index === current ? waitingEvent.delay_ms : undefined

  return (
    <section className="sequence-player">
      <div className="sequence-player-head">
        <div>
          <div className="template-name">{sequence.name}</div>
          <div className="template-id">{sequence.id}</div>
        </div>
        <span className={`player-status ${status}`}>{status}</span>
      </div>

      <div className="sequence-timeline">
        {sequence.steps.map((step, index) => {
          const active = (status === 'playing' || status === 'paused') && index === current
          const complete = (status === 'completed' && index <= current) || index < current
          const errored = status === 'error' && index === current
          const width = Math.max(72, Math.round(((step.delay_ms || 0) / maxDelay) * 180))
          return (
            <button
              key={step.id}
              type="button"
              className={`timeline-node ${active ? 'active' : ''} ${complete ? 'complete' : ''} ${status === 'paused' && active ? 'paused' : ''} ${errored ? 'error' : ''}`}
              style={{ minWidth: width }}
              onClick={() => onSeek(index)}
              title={`Seek to step ${index + 1}`}
            >
              <span>{index + 1}</span>
              <strong>{step.name || step.template_ref || step.channel || 'Step'}</strong>
              <small>{step.delay_ms || 0}ms</small>
            </button>
          )
        })}
      </div>

      <div className="transport">
        <button type="button" className="btn primary" onClick={onPlay}>
          <Play /> Play
        </button>
        <button type="button" className="btn ghost" onClick={onPause} disabled={!canPause}>
          <Pause /> Pause
        </button>
        <button type="button" className="btn ghost" onClick={onStop} disabled={!canStop}>
          <Stop /> Stop
        </button>
        <select className="select speed-select" value={speed} onChange={e => onSpeed(Number(e.target.value))}>
          {SPEEDS.map(item => (
            <option key={item.label} value={item.value}>{item.label}</option>
          ))}
        </select>
        <div className="transport-summary">
          Step {Math.min(current + 1, sequence.steps.length)} / {sequence.steps.length}
          {waitingDelay !== undefined && (status === 'playing' || status === 'paused') ? ` · next step in ${waitingDelay}ms` : ''}
          {state?.last_dispatch_summary ? ` · ${state.last_dispatch_summary}` : ''}
        </div>
      </div>

      {state?.last_error && <div className="socket-error">{state.last_error}</div>}
    </section>
  )
}
