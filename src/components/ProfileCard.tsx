import Modal from './Modal'
import PermissionPicker from './PermissionPicker'
import type { AgentView as Agent, Channel } from '../mygo'
import { bodyOf } from '../lib/message'

const STATUS_TEXT: Record<string, { label: string; color: string }> = {
  idle: { label: '空闲', color: 'var(--text-tertiary)' },
  running: { label: '干活中', color: 'var(--success)' },
  queued: { label: '排队中', color: 'var(--warning)' },
  blocked: { label: '等你处理', color: 'var(--danger)' },
  error: { label: '出错', color: 'var(--danger)' },
}

/**
 * ProfileCard 是 agent 的个人主页：他是谁、在干什么、在哪些频道、最近说过什么。
 * 数据全部来自现有快照（AgentView + Channel[]），不依赖新后端接口。
 */
export default function ProfileCard({
  agent,
  channels,
  onClose,
  onOpenChat,
  onOpenChannel,
}: {
  agent: Agent | null
  channels: Channel[]
  onClose: () => void
  onOpenChat?: (id: string) => void
  onOpenChannel?: (id: string) => void
}) {
  if (!agent) return null

  const st = STATUS_TEXT[agent.status] ?? STATUS_TEXT.idle
  const joined = channels.filter((c) => (c.members ?? []).includes(agent.id))
  const recent = channels
    .flatMap((c) =>
      (c.messages ?? [])
        .filter((m) => m.fromId === agent.id)
        .map((m) => ({ ...m, channelName: c.name })),
    )
    .sort((a, b) => b.at - a.at)
    .slice(0, 3)

  const row = (label: string, value: string) => (
    <div style={{ display: 'flex', gap: 10, fontSize: 'var(--text-sm)', lineHeight: 1.8 }}>
      <span style={{ width: 64, flexShrink: 0, color: 'var(--text-tertiary)' }}>{label}</span>
      <span style={{ color: 'var(--text-secondary)', wordBreak: 'break-all' }}>{value}</span>
    </div>
  )

  return (
    <Modal open onClose={onClose} title="个人主页" width={440}>
      <div style={{ padding: '18px 20px' }}>
        <div style={{ display: 'flex', gap: 14, alignItems: 'center', marginBottom: 14 }}>
          <span
            style={{
              width: 48,
              height: 48,
              borderRadius: 'var(--radius-lg)',
              background: agent.color,
              color: 'var(--bg-base)',
              display: 'grid',
              placeItems: 'center',
              fontSize: 20,
              fontWeight: 700,
              flexShrink: 0,
            }}
          >
            {agent.name.slice(0, 1).toUpperCase()}
          </span>
          <div style={{ minWidth: 0 }}>
            <div style={{ fontSize: 'var(--text-lg)', fontWeight: 700, lineHeight: 1.3 }}>
              {agent.name}
            </div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              {agent.role || '没写职责'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: st.color, marginTop: 2 }}>
              ● {st.label}
              {agent.status === 'blocked' && agent.blockedReason ? ` · ${agent.blockedReason}` : ''}
              {agent.busy ? ` · 正在 #${agent.busy} 回答` : ''}
            </div>
          </div>
        </div>

        {row('模型', agent.llm?.model || agent.backend || '默认')}
        {agent.workDir ? row('工作目录', agent.workDir) : null}
        {agent.createdAt ? row('入职时间', new Date(agent.createdAt * 1000).toLocaleString('zh-CN')) : null}
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', fontSize: 'var(--text-sm)', lineHeight: 1.8 }}>
          <span style={{ width: 64, flexShrink: 0, color: 'var(--text-tertiary)' }}>干活权限</span>
          <PermissionPicker agentId={agent.id} />
        </div>

        <div style={{ marginTop: 14 }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>
            所在频道（{joined.length}）
          </div>
          {joined.length === 0 && (
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-disabled)' }}>还没进任何频道</div>
          )}
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {joined.map((c) => (
              <button
                key={c.id}
                className="c-btn c-btn--sm"
                onClick={() => onOpenChannel?.(c.id)}
                title={`打开 #${c.name}`}
              >
                # {c.name}
              </button>
            ))}
          </div>
        </div>

        <div style={{ marginTop: 14 }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>
            最近发言
          </div>
          {recent.length === 0 && (
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-disabled)' }}>还没在频道里说过话</div>
          )}
          {recent.map((m) => (
            <div
              key={m.id}
              style={{
                padding: '7px 10px',
                marginBottom: 6,
                background: 'var(--bg-inset)',
                borderRadius: 'var(--radius-md)',
                fontSize: 'var(--text-sm)',
              }}
            >
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 2 }}>
                #{m.channelName} ·{' '}
                {new Date(m.at * 1000).toLocaleString('zh-CN', {
                  month: 'numeric',
                  day: 'numeric',
                  hour: '2-digit',
                  minute: '2-digit',
                })}
              </div>
              <div
                style={{
                  color: 'var(--text-secondary)',
                  overflow: 'hidden',
                  display: '-webkit-box',
                  WebkitLineClamp: 2,
                  WebkitBoxOrient: 'vertical',
                }}
              >
                {bodyOf(m.text)}
              </div>
            </div>
          ))}
        </div>
      </div>

      <div style={{ padding: '0 20px 18px', display: 'flex', gap: 10 }}>
        <button
          className="c-btn c-btn--primary"
          style={{ flex: 1 }}
          onClick={() => {
            onOpenChat?.(agent.id)
            onClose()
          }}
        >
          去单聊
        </button>
        <button className="c-btn" onClick={onClose}>
          关闭
        </button>
      </div>
    </Modal>
  )
}