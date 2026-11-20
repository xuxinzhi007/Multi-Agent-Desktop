import { useEffect, useRef } from 'react'
import type { Room } from '../mygo'

export default function FeishuRoomView({ room }: { room: Room }) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const lines = room.messages ?? []

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [room.id, lines.length])

  return (
    <div className="c-col" style={{ flex: 1 }}>
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          height: 52,
          gap: 'var(--space-2)',
          padding: '0 var(--space-4)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span
          style={{
            width: 24,
            height: 24,
            borderRadius: 'var(--radius-sm)',
            background: 'var(--accent-dim)',
            color: 'var(--accent-bright)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 12,
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          飞
        </span>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, lineHeight: 1.3 }}>
            {room.name || '飞书会话'}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            由{room.agentName || '本机会话'}回答。这里只能看记录。
          </div>
        </div>
      </header>
      <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4)' }}>
        {lines.length === 0 ? (
          <div className="c-empty">还没有记下消息</div>
        ) : (
          lines.map((line) => {
            const name = line.self ? '机器人' : line.from || '有人'
            return (
              <div key={line.id} style={{ display: 'flex', gap: 'var(--space-3)', marginBottom: 'var(--space-4)' }}>
                <span
                  style={{
                    width: 22,
                    height: 22,
                    borderRadius: 'var(--radius-sm)',
                    background: line.self ? 'var(--accent-bright)' : 'var(--bg-surface-active)',
                    color: line.self ? 'var(--bg-base)' : 'var(--text-secondary)',
                    display: 'grid',
                    placeItems: 'center',
                    fontSize: 11,
                    fontWeight: 700,
                    flexShrink: 0,
                    marginTop: 1,
                  }}
                >
                  {name.slice(0, 1)}
                </span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 6,
                      marginBottom: 3,
                      fontSize: 'var(--text-xs)',
                      fontWeight: 600,
                      color: line.self ? 'var(--accent-bright)' : 'var(--text-tertiary)',
                    }}
                  >
                    {name}
                    {line.at > 0 && (
                      <span style={{ marginLeft: 'auto', fontWeight: 400, color: 'var(--text-disabled)' }}>
                        {new Date((line.at > 1e12 ? line.at : line.at * 1000)).toLocaleTimeString('zh-CN', {
                          hour: '2-digit',
                          minute: '2-digit',
                        })}
                      </span>
                    )}
                  </div>
                  <div
                    style={{
                      fontSize: 'var(--text-md)',
                      lineHeight: 1.7,
                      wordBreak: 'break-word',
                      color: 'var(--text-secondary)',
                      whiteSpace: 'pre-wrap',
                    }}
                  >
                    {line.text}
                  </div>
                </div>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
