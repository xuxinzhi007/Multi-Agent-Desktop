import { useState } from 'react'
import { Sidecar, type AgentView as Agent, type Channel } from '../mygo'
import CreateAgentModal from './CreateAgentModal'

type Props = {
  agents: Agent[]
  channels: Channel[]
  /** 当前选中：agent id / channel id / null */
  activeId: string | null
  activeKind: 'agent' | 'channel' | null
  connected: boolean
  version: string
  provider: string
  onSelect: (kind: 'agent' | 'channel', id: string) => void
  onCreate: (name: string, role: string, backend: string, workDir?: string) => void
  onDelete: (id: string) => void
  onCreateChannel: (name: string, members: string[]) => void
  onOpenSettings: () => void
}

export default function Sidebar(p: Props) {
  const [showCreateModal, setShowCreateModal] = useState(false)
  const [showChannelModal, setShowChannelModal] = useState(false)

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
        {/* 频道分组 */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            padding: '4px 2px 5px',
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
            fontWeight: 500,
            letterSpacing: '0.04em',
          }}
        >
          频道
          <button
            className="c-icon-btn"
            style={{ marginLeft: 'auto', width: 18, height: 18, fontSize: 12 }}
            onClick={() => setShowChannelModal(true)}
            title="新建频道"
          >
            +
          </button>
        </div>
        {p.channels.map((ch) => {
          const active = p.activeKind === 'channel' && ch.id === p.activeId
          return (
            <div
              key={ch.id}
              onClick={() => p.onSelect('channel', ch.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 'var(--space-2)',
                padding: '6px 10px',
                borderRadius: 'var(--radius-md)',
                cursor: 'pointer',
                marginBottom: 1,
                background: active ? 'var(--bg-surface-active)' : 'transparent',
              }}
              onMouseEnter={(e) => {
                if (!active) e.currentTarget.style.background = 'var(--bg-surface)'
              }}
              onMouseLeave={(e) => {
                if (!active) e.currentTarget.style.background = 'transparent'
              }}
            >
              <span
                style={{
                  width: 19,
                  height: 19,
                  borderRadius: 'var(--radius-sm)',
                  background: active ? 'var(--accent-dim)' : 'var(--bg-surface)',
                  color: active ? 'var(--accent-bright)' : 'var(--text-tertiary)',
                  display: 'grid',
                  placeItems: 'center',
                  fontSize: 11,
                  fontWeight: 700,
                  flexShrink: 0,
                }}
              >
                #
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 'var(--text-base)',
                    fontWeight: active ? 600 : 500,
                    color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {ch.name}
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                  {ch.members.length} 成员 · {ch.messages.length} 消息
                </div>
              </div>
            </div>
          )
        })}

        {p.channels.length > 0 && (
          <div
            style={{
              padding: '10px 2px 5px',
              fontSize: 'var(--text-xs)',
              color: 'var(--text-disabled)',
              fontWeight: 500,
              letterSpacing: '0.04em',
            }}
          >
            AGENTS
          </div>
        )}

        {p.agents.length === 0 && !showCreateModal && (
          <div className="c-empty" style={{ padding: 'var(--space-6) var(--space-3)' }}>
            还没有 agent
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 8, color: 'var(--text-disabled)' }}>
              点下面新建，可同时建多个并行跑
            </div>
          </div>
        )}

        {p.agents.map((a) => {
          const active = p.activeKind === 'agent' && a.id === p.activeId
          return (
            <div
              key={a.id}
              onClick={() => p.onSelect('agent', a.id)}
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
                className="agent-avatar"
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
                  // @ts-ignore CSS 变量
                  '--avatar-color': a.color,
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
                    className={`c-dot c-dot--${a.status} ${a.status === 'running' || a.status === 'queued' || a.status === 'blocked' ? 'blink' : ''}`}
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
                  {a.status === 'blocked'
                    ? `⚠ ${a.blockedReason || '等你处理'}`
                    : a.status === 'running'
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

      </div>

      {/* 底部 */}
      <div style={{ padding: 'var(--space-2)', borderTop: '1px solid var(--border-subtle)', display: 'grid', gap: 6 }}>
        <button
          className="c-btn c-btn--secondary c-btn--full"
          onClick={() => setShowCreateModal(true)}
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

      {/* 新建 Agent 弹窗 */}
      <CreateAgentModal
        open={showCreateModal}
        onClose={() => setShowCreateModal(false)}
        onCreate={p.onCreate}
      />

      <CreateChannelModal
        open={showChannelModal}
        agents={p.agents}
        onClose={() => setShowChannelModal(false)}
        onCreate={(name, members) => {
          p.onCreateChannel(name, members)
          setShowChannelModal(false)
        }}
      />
    </aside>
  )
}
function CreateChannelModal({
  open,
  agents,
  onClose,
  onCreate,
}: {
  open: boolean
  agents: Agent[]
  onClose: () => void
  onCreate: (name: string, members: string[]) => void
}) {
  const [name, setName] = useState('')
  const [members, setMembers] = useState<string[]>([])

  if (!open) return null

  const toggle = (id: string) =>
    setMembers((m) => (m.includes(id) ? m.filter((x) => x !== id) : [...m, id]))

  const submit = () => {
    const n = name.trim()
    if (!n) return
    onCreate(n, members)
    setName('')
    setMembers([])
  }

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.6)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
        backdropFilter: 'blur(4px)',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 380,
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-lg)',
          boxShadow: 'var(--shadow-lg)',
          overflow: 'hidden',
        }}
      >
        <div
          style={{
            padding: '16px 20px',
            borderBottom: '1px solid var(--border-subtle)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>新建频道</span>
          <button className="c-icon-btn" onClick={onClose}>
            ×
          </button>
        </div>

        <div style={{ padding: 20, display: 'grid', gap: 14 }}>
          <div>
            <label style={modalLabel}>频道名</label>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit()
                if (e.key === 'Escape') onClose()
              }}
              placeholder="例如：实现讨论"
              style={modalInput}
            />
          </div>
          <div>
            <label style={modalLabel}>成员（可多选）</label>
            {agents.length === 0 ? (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                还没有 agent，先创建一个
              </div>
            ) : (
              <div style={{ display: 'grid', gap: 4, maxHeight: 220, overflowY: 'auto' }}>
                {agents.map((a) => {
                  const on = members.includes(a.id)
                  return (
                    <button
                      key={a.id}
                      onClick={() => toggle(a.id)}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                        padding: '7px 10px',
                        borderRadius: 'var(--radius-sm)',
                        border: `1px solid ${on ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
                        background: on ? 'var(--accent-dim)' : 'var(--bg-surface)',
                        color: on ? 'var(--accent-bright)' : 'var(--text-secondary)',
                        fontSize: 'var(--text-sm)',
                        textAlign: 'left',
                        cursor: 'pointer',
                      }}
                    >
                      <span
                        style={{
                          width: 16,
                          height: 16,
                          borderRadius: 'var(--radius-sm)',
                          background: a.color,
                          color: 'var(--bg-base)',
                          display: 'grid',
                          placeItems: 'center',
                          fontSize: 9,
                          fontWeight: 700,
                        }}
                      >
                        {a.name.slice(0, 1).toUpperCase()}
                      </span>
                      {a.name}
                      {on && <span style={{ marginLeft: 'auto', fontSize: 11 }}>✓</span>}
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        </div>

        <div
          style={{
            padding: '16px 20px',
            borderTop: '1px solid var(--border-subtle)',
            display: 'flex',
            gap: 10,
          }}
        >
          <button className="c-btn c-btn--secondary" style={{ flex: 1 }} onClick={onClose}>
            取消
          </button>
          <button
            className="c-btn c-btn--primary"
            style={{ flex: 1 }}
            onClick={submit}
            disabled={!name.trim()}
          >
            创建
          </button>
        </div>
      </div>
    </div>
  )
}

const modalLabel: React.CSSProperties = {
  display: 'block',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-secondary)',
  marginBottom: 6,
}

const modalInput: React.CSSProperties = {
  width: '100%',
  padding: '8px 12px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-sm)',
}
