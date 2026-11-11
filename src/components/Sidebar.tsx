import { useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'
import type { Agent } from '../lib/ws'

type Props = {
  agents: Agent[]
  activeId: string | null
  connected: boolean
  version: string
  onSelect: (id: string) => void
  onCreate: (name: string, role: string) => void
  onDelete: (id: string) => void
  onOpenSettings: () => void
}

const STATUS_COLOR: Record<string, string> = {
  idle: 'var(--dim)',
  running: 'var(--amber)',
  done: 'var(--green)',
  error: 'var(--red)',
}

export default function Sidebar(p: Props) {
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [role, setRole] = useState(ROLE_PRESETS[0].role)

  const submit = () => {
    p.onCreate(name.trim(), role)
    setName('')
    setAdding(false)
  }

  return (
    <aside
      style={{
        width: 244,
        flexShrink: 0,
        background: 'var(--panel)',
        borderRight: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
      }}
    >
      <div style={{ padding: '14px 16px 10px', display: 'flex', alignItems: 'center', gap: 9 }}>
        <span
          style={{
            width: 8,
            height: 8,
            borderRadius: '50%',
            background: p.connected ? 'var(--green)' : 'var(--red)',
            boxShadow: `0 0 8px ${p.connected ? 'var(--green)' : 'var(--red)'}`,
            flexShrink: 0,
          }}
          className={p.connected ? '' : 'blink'}
        />
        <div style={{ minWidth: 0 }}>
          <div style={{ fontWeight: 600, fontSize: 13, letterSpacing: 0.2 }}>Agent 舰队</div>
          <div style={{ fontSize: 10.5, color: 'var(--dim)' }}>
            {p.agents.length} 个会话 · v{p.version || '—'}
          </div>
        </div>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '4px 8px' }}>
        {p.agents.length === 0 && !adding && (
          <div
            style={{
              padding: '26px 14px',
              textAlign: 'center',
              color: 'var(--dim)',
              fontSize: 11.5,
              lineHeight: 1.8,
            }}
          >
            还没有 agent
            <br />
            点下面「新建 Agent」开始
          </div>
        )}

        {p.agents.map((a) => {
          const active = a.id === p.activeId
          return (
            <div
              key={a.id}
              onClick={() => p.onSelect(a.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 9,
                padding: '9px 10px',
                borderRadius: 7,
                cursor: 'pointer',
                marginBottom: 2,
                background: active ? 'var(--panel-2)' : 'transparent',
                borderLeft: `2px solid ${active ? a.color : 'transparent'}`,
                transition: 'background 0.12s',
              }}
              onMouseEnter={(e) => {
                if (!active) e.currentTarget.style.background = '#171b23'
              }}
              onMouseLeave={(e) => {
                if (!active) e.currentTarget.style.background = 'transparent'
              }}
            >
              <span
                style={{
                  width: 7,
                  height: 7,
                  borderRadius: '50%',
                  background: STATUS_COLOR[a.status] ?? 'var(--dim)',
                  flexShrink: 0,
                }}
                className={a.status === 'running' ? 'blink' : ''}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 12.5,
                    fontWeight: 500,
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {a.name}
                </div>
                <div
                  style={{
                    fontSize: 10.5,
                    color: 'var(--dim)',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {a.role}
                </div>
              </div>
              <button
                onClick={(e) => {
                  e.stopPropagation()
                  p.onDelete(a.id)
                }}
                title="删除"
                style={{
                  color: 'var(--dim)',
                  fontSize: 15,
                  lineHeight: 1,
                  padding: '2px 4px',
                  borderRadius: 4,
                  opacity: 0.6,
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.color = 'var(--red)'
                  e.currentTarget.style.opacity = '1'
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.color = 'var(--dim)'
                  e.currentTarget.style.opacity = '0.6'
                }}
              >
                ×
              </button>
            </div>
          )
        })}

        {adding && (
          <div
            className="fade-up"
            style={{
              background: 'var(--panel-2)',
              border: '1px solid var(--border)',
              borderRadius: 7,
              padding: 10,
              marginTop: 6,
            }}
          >
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit()
                if (e.key === 'Escape') setAdding(false)
              }}
              placeholder="Agent 名字"
              style={{ width: '100%', marginBottom: 7, fontSize: 12 }}
            />
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              style={{ width: '100%', marginBottom: 8, fontSize: 11.5 }}
            >
              {ROLE_PRESETS.map((r) => (
                <option key={r.name} value={r.role}>
                  {r.name}
                </option>
              ))}
            </select>
            <div style={{ display: 'flex', gap: 6 }}>
              <button
                onClick={submit}
                style={{
                  flex: 1,
                  background: 'var(--accent)',
                  color: '#fff',
                  padding: '6px 0',
                  borderRadius: 5,
                  fontSize: 11.5,
                  fontWeight: 500,
                }}
              >
                创建
              </button>
              <button
                onClick={() => setAdding(false)}
                style={{
                  flex: 1,
                  background: 'var(--panel)',
                  border: '1px solid var(--border)',
                  padding: '6px 0',
                  borderRadius: 5,
                  fontSize: 11.5,
                  color: 'var(--muted)',
                }}
              >
                取消
              </button>
            </div>
          </div>
        )}
      </div>

      <div style={{ padding: 10, borderTop: '1px solid var(--border)', display: 'grid', gap: 6 }}>
        {!adding && (
          <button
            onClick={() => setAdding(true)}
            style={{
              background: 'var(--panel-2)',
              border: '1px solid var(--border)',
              padding: '8px 0',
              borderRadius: 6,
              fontSize: 12,
              fontWeight: 500,
              color: 'var(--text)',
            }}
            onMouseEnter={(e) => (e.currentTarget.style.borderColor = 'var(--accent)')}
            onMouseLeave={(e) => (e.currentTarget.style.borderColor = 'var(--border)')}
          >
            + 新建 Agent
          </button>
        )}
        <button
          onClick={p.onOpenSettings}
          style={{
            padding: '7px 0',
            borderRadius: 6,
            fontSize: 11.5,
            color: 'var(--muted)',
          }}
          onMouseEnter={(e) => (e.currentTarget.style.color = 'var(--text)')}
          onMouseLeave={(e) => (e.currentTarget.style.color = 'var(--muted)')}
        >
          ⚙ 设置
        </button>
      </div>
    </aside>
  )
}