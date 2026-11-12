import { useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'
import { Sidecar, type Agent } from '../mygo'

type Props = {
  agents: Agent[]
  activeId: string | null
  connected: boolean
  version: string
  provider: string
  onSelect: (id: string) => void
  onCreate: (name: string, role: string, backend: string) => void
  onDelete: (id: string) => void
  onOpenSettings: () => void
}

export default function Sidebar(p: Props) {
  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [role, setRole] = useState(ROLE_PRESETS[0].role)
  const [backend, setBackend] = useState('mock')

  const submit = () => {
    const n = name.trim()
    if (!n) return
    p.onCreate(n, role, backend)
    setName('')
    setAdding(false)
  }

  const running = p.agents.filter((a) => a.status === 'running').length

  return (
    <aside
      className="c-col"
      style={{
        width: 'var(--sidebar-w)',
        flexShrink: 0,
        background: 'var(--bg-chrome)',
        borderRight: '1px solid var(--border-subtle)',
      }}
    >
      {/* 顶部 */}
      <div
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '0 var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span
          className={`c-dot ${p.connected ? 'c-dot--connected' : 'c-dot--error'} ${p.connected ? '' : 'blink'}`}
        />
        <span style={{ fontSize: 'var(--text-md)', fontWeight: 600, letterSpacing: '-0.01em' }}>
          Agent
        </span>
        <span
          className="mono"
          style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginTop: 1 }}
        >
          v{p.version || '—'}
        </span>
        <button
          className="c-icon-btn"
          style={{ marginLeft: 'auto' }}
          onClick={p.onOpenSettings}
          title="设置"
        >
          ⚙
        </button>
      </div>

      {/* 统计条 */}
      <div
        style={{
          display: 'flex',
          gap: 'var(--space-3)',
          padding: '9px var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          fontSize: 'var(--text-xs)',
          color: 'var(--text-tertiary)',
          flexShrink: 0,
        }}
      >
        <span>
          共 <b style={{ color: 'var(--text-secondary)', fontWeight: 600 }}>{p.agents.length}</b>
        </span>
        {running > 0 && (
          <span style={{ color: 'var(--warning)' }}>
            运行 <b style={{ fontWeight: 600 }}>{running}</b>
          </span>
        )}
        <span style={{ marginLeft: 'auto', color: 'var(--text-disabled)' }}>{p.provider}</span>
      </div>

      {/* 列表 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-2)' }}>
        {p.agents.length === 0 && !adding && (
          <div className="c-empty" style={{ padding: 'var(--space-6) var(--space-3)' }}>
            还没有 agent
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 8, color: 'var(--text-disabled)' }}>
              点下面新建，可同时建多个并行跑
            </div>
          </div>
        )}

        {p.agents.map((a) => {
          const active = a.id === p.activeId
          return (
            <div
              key={a.id}
              onClick={() => p.onSelect(a.id)}
              style={{
                position: 'relative',
                display: 'flex',
                alignItems: 'center',
                gap: 'var(--space-2)',
                padding: '8px 10px 8px 11px',
                borderRadius: 'var(--radius-md)',
                cursor: 'pointer',
                marginBottom: 1,
                background: active ? 'var(--bg-surface-active)' : 'transparent',
                transition: 'background var(--dur-fast) var(--ease)',
              }}
              onMouseEnter={(e) => {
                if (!active) e.currentTarget.style.background = 'var(--bg-surface)'
              }}
              onMouseLeave={(e) => {
                if (!active) e.currentTarget.style.background = 'transparent'
              }}
            >
              {active && (
                <span
                  style={{
                    position: 'absolute',
                    left: 0,
                    top: '50%',
                    transform: 'translateY(-50%)',
                    width: 2,
                    height: 16,
                    borderRadius: 'var(--radius-full)',
                    background: a.color,
                  }}
                />
              )}
              <span
                style={{
                  width: 19,
                  height: 19,
                  borderRadius: 'var(--radius-sm)',
                  background: a.color,
                  color: 'var(--bg-base)',
                  display: 'grid',
                  placeItems: 'center',
                  fontSize: 10,
                  fontWeight: 700,
                  flexShrink: 0,
                  opacity: a.status === 'idle' ? 0.55 : 1,
                }}
              >
                {a.name.slice(0, 1).toUpperCase()}
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 5,
                    fontSize: 'var(--text-base)',
                    fontWeight: active ? 600 : 500,
                    color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
                  }}
                >
                  <span
                    style={{
                      whiteSpace: 'nowrap',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                    }}
                  >
                    {a.name}
                  </span>
                  <span
                    className={`c-dot c-dot--${a.status} ${a.status === 'running' || a.status === 'queued' ? 'blink' : ''}`}
                    style={{ width: 5, height: 5 }}
                  />
                </div>
                <div
                  style={{
                    fontSize: 'var(--text-xs)',
                    color: 'var(--text-disabled)',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {a.status === 'running'
                    ? '正在处理…'
                    : a.status === 'queued'
                      ? `排队中 (${a.queueLen})`
                      : a.messages.length > 1
                        ? a.messages[a.messages.length - 1].content.slice(0, 30) + (a.messages[a.messages.length - 1].content.length > 30 ? '…' : '')
                        : a.role}
                </div>
              </div>
              <button
                className="c-icon-btn c-icon-btn--danger"
                onClick={(e) => {
                  e.stopPropagation()
                  if (confirm(`删除 agent「${a.name}」？\n\n历史消息将一并清除，不可恢复。`)) {
                    p.onDelete(a.id)
                  }
                }}
                title="删除"
                style={{ opacity: 0.35, width: 20, height: 20, fontSize: 14 }}
                onMouseEnter={(e) => (e.currentTarget.style.opacity = '1')}
                onMouseLeave={(e) => (e.currentTarget.style.opacity = '0.35')}
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
              background: 'var(--bg-surface)',
              border: '1px solid var(--border-default)',
              borderRadius: 'var(--radius-md)',
              padding: 'var(--space-2)',
              marginTop: 'var(--space-1)',
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
              placeholder="名字"
              style={{ width: '100%', marginBottom: 6, fontSize: 'var(--text-sm)' }}
            />
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              style={{ width: '100%', marginBottom: 7, fontSize: 'var(--text-xs)' }}
            >
              {ROLE_PRESETS.map((r) => (
                <option key={r.name} value={r.role}>
                  {r.name}
                </option>
              ))}
            </select>
            <select
              value={backend}
              onChange={(e) => setBackend(e.target.value)}
              style={{ width: '100%', marginBottom: 7, fontSize: 'var(--text-xs)' }}
            >
              <option value="mock">Mock（内置）</option>
              <option value="claude">Claude Code</option>
              <option value="codex">Codex</option>
            </select>
            <div style={{ display: 'flex', gap: 5 }}>
              <button className="c-btn c-btn--primary c-btn--sm" style={{ flex: 1 }} onClick={submit}>
                创建
              </button>
              <button
                className="c-btn c-btn--secondary c-btn--sm"
                style={{ flex: 1 }}
                onClick={() => setAdding(false)}
              >
                取消
              </button>
            </div>
          </div>
        )}
      </div>

      {/* 底部 */}
      <div style={{ padding: 'var(--space-2)', borderTop: '1px solid var(--border-subtle)', display: 'grid', gap: 6 }}>
        <button
          className="c-btn c-btn--secondary c-btn--full"
          onClick={() => setAdding(true)}
          disabled={adding}
        >
          <span style={{ fontSize: 13, lineHeight: 1 }}>+</span> 新建 Agent
        </button>
        <button
          className="c-btn c-btn--ghost c-btn--full"
          onClick={() => Sidecar.toggleMiniWindow(true)}
          title="缩小到悬浮条"
        >
          ─ 缩小到悬浮条
        </button>
      </div>
    </aside>
  )
}