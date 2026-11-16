import { useEffect, useState } from 'react'
import type { AgentView as Agent } from '../mygo'

/**
 * FleetView —— 多 agent 并排监控台。
 *
 * 定位不是「第二个聊天工具」，而是「调度台」：一眼看到 N 个 agent
 * 各自在干什么、卡在哪、产出多少。所以每列展示的是
 * 状态 + 当前任务 + 最近动态 + 产出量，而不是完整对话。
 * 点任意一列进入该 agent 的详细对话。
 */
export default function FleetView({
  agents,
  streaming,
  onOpen,
  onStop,
  onQuickCreate,
  onCreateRequest,
}: {
  agents: Agent[]
  streaming: Record<string, string>
  onOpen: (id: string) => void
  onStop: (id: string) => void
  /** 空状态下的一键创建 */
  onQuickCreate?: (name: string, role: string) => void
  onCreateRequest?: () => void
}) {
  if (agents.length === 0) {
    return (
      <div className="c-col" style={{ flex: 1 }}>
        <div className="c-empty" style={{ flex: 1 }}>
          <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }}>▦</div>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>
            组建你的 agent 舰队
          </div>
          <div
            style={{
              fontSize: 'var(--text-sm)',
              color: 'var(--text-secondary)',
              marginBottom: 20,
            }}
          >
            选一个角色立刻开始。之后这里会并排显示每个 agent 的进度。
          </div>
          <div
            style={{
              display: 'flex',
              flexWrap: 'wrap',
              gap: 8,
              justifyContent: 'center',
              marginBottom: 16,
            }}
          >
            {QUICK_ROLES.map((r) => (
              <button
                key={r.name}
                type="button"
                className="c-empty-cta"
                onClick={() => onQuickCreate?.(r.name, r.role)}
              >
                <span aria-hidden>{r.emoji}</span>
                {r.label}
              </button>
            ))}
          </div>
          <button type="button" className="c-hint-chip" onClick={() => onCreateRequest?.()}>
            自定义名称与角色 →
          </button>
        </div>
      </div>
    )
  }

  return (
    <div className="c-col" style={{ flex: 1, minWidth: 0 }}>
      <header
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '0 var(--space-4)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>舰队</span>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
          {agents.length} 个 agent
        </span>
        <div style={{ marginLeft: 'auto' }}>
          <FleetSummary agents={agents} />
        </div>
      </header>

      <div
        style={{
          flex: 1,
          overflowX: 'auto',
          overflowY: 'hidden',
          display: 'flex',
          gap: 'var(--space-3)',
          padding: 'var(--space-3)',
          minHeight: 0,
        }}
      >
        {agents.map((a) => (
          <FleetColumn
            key={a.id}
            agent={a}
            live={streaming[a.id] ?? ''}
            onOpen={() => onOpen(a.id)}
            onStop={() => onStop(a.id)}
          />
        ))}
      </div>
    </div>
  )
}

function FleetSummary({ agents }: { agents: Agent[] }) {
  const running = agents.filter((a) => a.status === 'running').length
  const queued = agents.filter((a) => a.status === 'queued').length
  const blocked = agents.filter((a) => a.status === 'blocked').length

  return (
    <div style={{ display: 'flex', gap: 'var(--space-3)', fontSize: 'var(--text-xs)' }}>
      {blocked > 0 && (
        <span style={{ color: 'var(--danger)', fontWeight: 600 }}>⚠ {blocked} 个在等你</span>
      )}
      {running > 0 && <span style={{ color: 'var(--success)' }}>● {running} 运行中</span>}
      {queued > 0 && <span style={{ color: 'var(--warning)' }}>◷ {queued} 排队</span>}
      {running === 0 && queued === 0 && blocked === 0 && (
        <span style={{ color: 'var(--text-disabled)' }}>全部空闲</span>
      )}
    </div>
  )
}

function FleetColumn({
  agent,
  live,
  onOpen,
  onStop,
}: {
  agent: Agent
  live: string
  onOpen: () => void
  onStop: () => void
}) {
  const running = agent.status === 'running' || agent.status === 'queued'
  const status = STATUS[agent.status] ?? STATUS.idle

  // 最近一条 user 消息 = 它此刻的任务
  const task = [...(agent.messages ?? [])].reverse().find((m) => m.role === 'user')
  // 最近一条 assistant 消息 = 它最新的产出
  const lastReply = [...(agent.messages ?? [])].reverse().find((m) => m.role === 'assistant')

  return (
    <div
      style={{
        width: 300,
        flexShrink: 0,
        display: 'flex',
        flexDirection: 'column',
        background: 'var(--bg-chrome)',
        border: '1px solid var(--border-subtle)',
        borderRadius: 'var(--radius-lg)',
        overflow: 'hidden',
        minHeight: 0,
      }}
    >
      {/* 头部：身份 + 状态 */}
      <div
        style={{
          padding: 'var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginBottom: 6 }}>
          <span
            style={{
              width: 20,
              height: 20,
              borderRadius: 'var(--radius-sm)',
              background: agent.color,
              color: 'var(--bg-base)',
              display: 'grid',
              placeItems: 'center',
              fontSize: 11,
              fontWeight: 700,
              flexShrink: 0,
            }}
          >
            {agent.name.slice(0, 1).toUpperCase()}
          </span>
          <span
            style={{
              fontSize: 'var(--text-sm)',
              fontWeight: 600,
              minWidth: 0,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {agent.name}
          </span>
          <span
            className={running ? 'blink' : undefined}
            style={{ marginLeft: 'auto', fontSize: 11, color: status.color, flexShrink: 0 }}
            title={status.label}
          >
            {status.icon} {status.label}
          </span>
        </div>

        {/* 运行时指标：证明它真的在动 */}
        <div
          style={{
            display: 'flex',
            gap: 'var(--space-3)',
            fontSize: 10,
            color: 'var(--text-disabled)',
            fontFamily: 'var(--font-mono)',
          }}
        >
          {agent.status === 'running' && agent.startedAt ? (
            <Elapsed from={agent.startedAt} />
          ) : null}
          {(agent.chars ?? 0) > 0 && <span>{agent.chars} 字</span>}
          {(agent.queueLen ?? 0) > 0 && (
            <span style={{ color: 'var(--warning)' }}>队列 {agent.queueLen}</span>
          )}
          {agent.lastTool && <span title="最近工具">{agent.lastTool}</span>}
        </div>

        {agent.status === 'blocked' && agent.blockedReason && (
          <div
            style={{
              marginTop: 6,
              fontSize: 10,
              color: 'var(--danger)',
              background: 'var(--danger-dim)',
              border: '1px solid var(--danger-border)',
              borderRadius: 'var(--radius-sm)',
              padding: '3px 6px',
            }}
          >
            {agent.blockedReason}
          </div>
        )}
      </div>

      {/* 当前任务 + 实时输出 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-3)', minHeight: 0 }}>
        {task ? (
          <div style={{ marginBottom: 'var(--space-3)' }}>
            <div style={labelStyle}>当前任务</div>
            <div
              style={{
                fontSize: 'var(--text-xs)',
                color: 'var(--text-secondary)',
                lineHeight: 1.6,
                display: '-webkit-box',
                WebkitLineClamp: 3,
                WebkitBoxOrient: 'vertical',
                overflow: 'hidden',
              }}
            >
              {task.content.replace(/^\[(from|to)\s+\S+\]\s*/, '')}
            </div>
          </div>
        ) : (
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginBottom: 'var(--space-3)' }}>
            还没有任务
          </div>
        )}

        {live ? (
          <div>
            <div style={labelStyle}>实时输出</div>
            <div
              className="mono"
              style={{
                fontSize: 11,
                lineHeight: 1.6,
                color: 'var(--text-secondary)',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                maxHeight: 240,
                overflow: 'hidden',
              }}
            >
              {live.slice(-600)}
              <span className="blink" style={{ color: agent.color }}>
                ▌
              </span>
            </div>
          </div>
        ) : lastReply ? (
          <div>
            <div style={labelStyle}>最近产出</div>
            <div
              style={{
                fontSize: 11,
                lineHeight: 1.6,
                color: 'var(--text-tertiary)',
                whiteSpace: 'pre-wrap',
                display: '-webkit-box',
                WebkitLineClamp: 8,
                WebkitBoxOrient: 'vertical',
                overflow: 'hidden',
              }}
            >
              {lastReply.content}
            </div>
          </div>
        ) : null}
      </div>

      {/* 操作 */}
      <div
        style={{
          padding: 'var(--space-2)',
          borderTop: '1px solid var(--border-subtle)',
          display: 'flex',
          gap: 6,
          flexShrink: 0,
        }}
      >
        <button className="c-btn c-btn--secondary c-btn--sm" style={{ flex: 1 }} onClick={onOpen}>
          打开对话
        </button>
        {running && (
          <button className="c-btn c-btn--danger c-btn--sm" onClick={onStop}>
            停止
          </button>
        )}
      </div>
    </div>
  )
}

/** Elapsed 每秒刷新「跑了多久」，让用户确信它没卡住。 */
function Elapsed({ from }: { from: number }) {
  const [now, setNow] = useState(() => Date.now() / 1000)
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now() / 1000), 1000)
    return () => clearInterval(t)
  }, [])
  const sec = Math.max(0, Math.floor(now - from))
  const text = sec < 60 ? `${sec}s` : `${Math.floor(sec / 60)}m${sec % 60}s`
  return <span style={{ color: 'var(--success)' }}>{text}</span>
}

// 空状态的一键角色。与 AgentView 保持同一组，避免两处漂移。
const QUICK_ROLES = [
  { emoji: '🧩', label: '前端', name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理与渲染性能' },
  { emoji: '⚙️', label: '后端', name: '后端', role: 'Go 专家，关注 API 设计、并发安全与查询效率' },
  { emoji: '🔍', label: '审查', name: 'Reviewer', role: '代码审查者，指出风险、代码异味与不符合规范的地方' },
] as const

const STATUS: Record<string, { icon: string; color: string; label: string }> = {
  running: { icon: '●', color: 'var(--success)', label: '运行中' },
  queued: { icon: '◷', color: 'var(--warning)', label: '排队' },
  blocked: { icon: '✻', color: 'var(--danger)', label: '等你' },
  done: { icon: '✓', color: 'var(--info)', label: '完成' },
  error: { icon: '✕', color: 'var(--danger)', label: '出错' },
  idle: { icon: '○', color: 'var(--text-disabled)', label: '空闲' },
}

const labelStyle: React.CSSProperties = {
  fontSize: 9,
  fontWeight: 600,
  letterSpacing: '0.06em',
  textTransform: 'uppercase',
  color: 'var(--text-disabled)',
  marginBottom: 3,
}
