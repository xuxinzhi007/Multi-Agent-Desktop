import { useEffect, useState } from 'react'
import type { Flight, Mode, View } from './logic'
import { GAP } from './logic'

type Props = {
  view: View
  onHome: () => void
  onFlightDone: (id: string) => void
  assistOn?: boolean
  onToggleAssist?: () => void
}

export function Island({ view, onHome, onFlightDone, assistOn, onToggleAssist }: Props) {
  const [reduce, setReduce] = useState(false)
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => setReduce(mq.matches)
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    if (!reduce || view.paused || view.flights.length === 0) return
    const id = view.flights[0].id
    const t = window.setTimeout(() => onFlightDone(id), 200)
    return () => window.clearTimeout(t)
  }, [reduce, view.paused, view.flights, onFlightDone])

  const mode = view.mode
  return (
    <div
      className={`island mode-${mode}${view.paused ? ' is-paused' : ''}`}
      data-mode={mode}
      role="status"
      aria-label={view.label}
      style={{ width: Math.min(view.width + (onToggleAssist ? 36 : 0), 308), ['--speed' as string]: String(view.speed) }}
    >
      <span key={mode} className="island__rim" />
      {view.progress > 0 && (
        <span className="island__meter" style={{ ['--p' as string]: String(view.progress) }} />
      )}
      <button type="button" className="home" aria-label="回到主窗口" onClick={onHome}>
        <Core mode={mode} />
      </button>
      {mode === 'thinking' && (
        <span className="sparks" aria-hidden>
          <i />
          <i />
          <i />
        </span>
      )}
      {view.dots.length > 0 && (
        <div className="track" style={{ width: view.track, ['--span' as string]: String(view.span) }}>
          {mode === 'collaborating' && view.flights.length === 0 && <i className="bead" aria-hidden />}
          {view.dots.map((d, i) => (
            <span
              key={d.id}
              className={`dot${d.wait ? ' dot--wait' : ''}${d.run ? ' dot--run' : ''}${d.bad ? ' dot--bad' : ''}`}
              title={d.name}
              style={{
                background: d.color,
                transform: `translate3d(${mode === 'completed' ? 0 : i * GAP}px, -50%, 0)`,
              }}
            >
              <i key={d.hot ? view.hotKey : d.id} className={d.hot ? 'dot__core is-hit' : 'dot__core'} />
            </span>
          ))}
          {!reduce &&
            view.flights.map((f) => (
              <Envelope key={f.id} flight={f} dots={view.dots} onDone={onFlightDone} />
            ))}
        </div>
      )}
      {onToggleAssist && (
        <button
          type="button"
          className={assistOn ? 'watch is-on' : 'watch'}
          aria-pressed={!!assistOn}
          onClick={onToggleAssist}
        >
          协助
        </button>
      )}
    </div>
  )
}

function Envelope({
  flight,
  dots,
  onDone,
}: {
  flight: Flight
  dots: View['dots']
  onDone: (id: string) => void
}) {
  const from = dots.findIndex((d) => d.id === flight.from)
  const to = dots.findIndex((d) => d.id === flight.to)
  if (from < 0 || to < 0) return null
  return (
    <b
      className="envelope"
      style={{
        ['--x0' as string]: String(from * GAP),
        ['--x1' as string]: String(to * GAP),
        ['--c' as string]: flight.color,
      }}
      onAnimationEnd={(e) => {
        if (e.animationName !== 'env-fly') return
        onDone(flight.id)
      }}
    />
  )
}

function Core({ mode }: { mode: Mode }) {
  if (mode === 'completed') {
    return (
      <svg className="mark" viewBox="0 0 16 16" aria-hidden>
        <path d="M3.4 8.2 L6.5 11.2 L12.6 4.8" />
      </svg>
    )
  }
  if (mode === 'error') {
    return (
      <svg className="mark mark--err" viewBox="0 0 16 16" aria-hidden>
        <path d="M4.2 4.2 L11.8 11.8 M11.8 4.2 L4.2 11.8" />
      </svg>
    )
  }
  return (
    <svg className="core" viewBox="0 0 24 24" aria-hidden>
      <circle className="core__ring" cx="12" cy="12" r="8.25" />
      <circle className="core__heart" cx="12" cy="12" r="2.7" />
    </svg>
  )
}
