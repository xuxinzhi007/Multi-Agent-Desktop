import { useEffect, useRef, useState } from 'react'

export type Activity = {
  id: string
  type: 'prompt' | 'route' | 'tool' | 'status' | 'error'
  fromName: string
  fromColor: string
  toName?: string
  text: string
  at: number
}

function activityIcon(type: Activity['type']): string {
  switch (type) {
    case 'prompt': return '💬'
    case 'route': return '✉'
    case 'tool': return '🔧'
    case 'status': return '●'
    case 'error': return '❌'
  }
}

function activityColor(type: Activity['type']): string {
  switch (type) {
    case 'prompt': return 'var(--text-secondary)'
    case 'route': return 'var(--accent-bright)'
    case 'tool': return 'var(--warning)'
    case 'status': return 'var(--success)'
    case 'error': return 'var(--error)'
  }
}

export default function ActivityFeed({ activities }: { activities: Activity[] }) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const [expanded, setExpanded] = useState(false)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [activities.length])

  const visible = expanded ? activities : activities.slice(-5)

  return (
    <div
      style={{
        position: 'fixed',
        bottom: 16,
        right: 16,
        width: 280,
        background: 'var(--bg-elevated)',
        border: '1px solid var(--border-default)',
        borderRadius: 'var(--radius-md)',
        boxShadow: 'var(--shadow-lg)',
        zIndex: 50,
        overflow: 'hidden',
      }}
    >
      <div
        onClick={() => setExpanded(!expanded)}
        style={{
          padding: '8px 12px',
          borderBottom: expanded ? '1px solid var(--border-subtle)' : 'none',
          cursor: 'pointer',
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          fontSize: 'var(--text-xs)',
          fontWeight: 600,
          color: 'var(--text-secondary)',
        }}
      >
        <span>📊 活动</span>
        <span style={{ marginLeft: 'auto', color: 'var(--text-disabled)' }}>
          {activities.length}
        </span>
        <span style={{ color: 'var(--text-disabled)', fontSize: 10 }}>
          {expanded ? '▼' : '▲'}
        </span>
      </div>
      {expanded && (
        <div
          ref={scrollRef}
          style={{
            maxHeight: 200,
            overflowY: 'auto',
            padding: '4px 0',
          }}
        >
          {visible.length === 0 && (
            <div style={{ padding: '12px', fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', textAlign: 'center' }}>
              暂无活动
            </div>
          )}
          {visible.map((a) => (
            <div
              key={a.id}
              style={{
                padding: '6px 12px',
                fontSize: 'var(--text-xs)',
                borderBottom: '1px solid var(--border-subtle)',
                display: 'flex',
                gap: 6,
                alignItems: 'flex-start',
              }}
            >
              <span style={{ flexShrink: 0 }}>{activityIcon(a.type)}</span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                  <span
                    style={{
                      width: 12,
                      height: 12,
                      borderRadius: 'var(--radius-sm)',
                      background: a.fromColor,
                      display: 'inline-block',
                      flexShrink: 0,
                    }}
                  />
                  <span style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                    {a.fromName}
                  </span>
                  {a.toName && (
                    <>
                      <span style={{ color: 'var(--text-disabled)' }}>→</span>
                      <span style={{ color: 'var(--text-secondary)' }}>{a.toName}</span>
                    </>
                  )}
                </div>
                <div
                  style={{
                    color: activityColor(a.type),
                    marginTop: 2,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {a.text}
                </div>
              </div>
              <span style={{ color: 'var(--text-disabled)', fontSize: 10, flexShrink: 0 }}>
                {new Date(a.at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
