import { useEffect, useState } from 'react'
import { connect, fetchHealth, SIDECAR_HTTP, type Health, type SidecarEvent } from './lib/ws'

type Status = 'connecting' | 'online' | 'offline'

const STATUS_STYLE: Record<Status, { dot: string; label: string }> = {
  connecting: { dot: '#e8a33d', label: '连接中' },
  online: { dot: '#3ecf8e', label: 'sidecar 已连接' },
  offline: { dot: '#e5484d', label: 'sidecar 未连接' },
}

export default function App() {
  const [status, setStatus] = useState<Status>('connecting')
  const [health, setHealth] = useState<Health | null>(null)
  const [events, setEvents] = useState<SidecarEvent[]>([])
  const [pings, setPings] = useState(0)

  useEffect(() => {
    let alive = true

    fetchHealth()
      .then((h) => {
        if (!alive) return
        setHealth(h)
        setStatus('online')
      })
      .catch(() => alive && setStatus('offline'))

    const close = connect((e) => {
      if (!alive) return
      setStatus('online')
      setEvents((prev) => [e, ...prev].slice(0, 8))
      if (e.event === 'ping') {
        setPings((n) => n + 1)
      }
    })

    return () => {
      alive = false
      close()
    }
  }, [])

  const style = STATUS_STYLE[status]

  return (
    <main style={{
      minHeight: '100vh',
      background: '#0f1115',
      color: '#e6e8ec',
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
      padding: '32px 28px',
      boxSizing: 'border-box',
    }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 4 }}>
        <span style={{
          width: 10, height: 10, borderRadius: '50%',
          background: style.dot, boxShadow: `0 0 10px ${style.dot}`,
        }} />
        <h1 style={{ fontSize: 18, margin: 0, fontWeight: 500, letterSpacing: 0.2 }}>
          multi-agent-desktop
        </h1>
      </header>
      <p style={{ color: style.dot, margin: '0 0 28px', fontSize: 13 }}>{style.label}</p>

      <section style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0,1fr))', gap: 12, marginBottom: 28 }}>
        <Stat label="WS 心跳" value={pings} suffix="次" />
        <Stat label="在线客户端" value={health?.clients ?? 0} suffix="个" />
        <Stat label="端口" value={17389} suffix="" />
      </section>

      <section>
        <h2 style={{ fontSize: 12, color: '#8b919e', letterSpacing: 1, textTransform: 'uppercase', margin: '0 0 12px' }}>
          事件流
        </h2>
        <div style={{
          background: '#171a21', border: '1px solid #242832', borderRadius: 8,
          padding: 14, minHeight: 160, fontSize: 12, lineHeight: 1.9,
        }}>
          {events.length === 0 && <span style={{ color: '#4d525e' }}>等待事件…</span>}
          {events.map((e, i) => (
            <div key={i} style={{ display: 'flex', gap: 10 }}>
              <span style={{ color: '#4d525e', flexShrink: 0 }}>
                {e.ts ? new Date(e.ts * 1000).toLocaleTimeString() : '—'}
              </span>
              <span style={{ color: e.event === 'ping' ? '#5b9dff' : '#3ecf8e', flexShrink: 0 }}>
                {e.event}
              </span>
              <span style={{ color: '#8b919e', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {JSON.stringify(e.data ?? {})}
              </span>
            </div>
          ))}
        </div>
      </section>

      <footer style={{ marginTop: 28, fontSize: 11, color: '#454a55' }}>
        {SIDECAR_HTTP}
      </footer>
    </main>
  )
}

function Stat({ label, value, suffix }: { label: string; value: number; suffix: string }) {
  return (
    <div style={{ background: '#171a21', border: '1px solid #242832', borderRadius: 8, padding: '14px 16px' }}>
      <div style={{ fontSize: 11, color: '#8b919e', marginBottom: 6 }}>{label}</div>
      <div style={{ fontSize: 22, fontWeight: 500 }}>
        {value}
        <span style={{ fontSize: 12, color: '#4d525e', marginLeft: 4 }}>{suffix}</span>
      </div>
    </div>
  )
}