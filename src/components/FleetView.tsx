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
  onQuickCreate?: (name: string, role: string) => void
  onCreateRequest?: () => void
}) {
  if (agents.length === 0) {
    return <FleetEmpty onQuickCreate={onQuickCreate} onCreateRequest={onCreateRequest} />
  }

  // 需要你处理的最前，其次在跑的，最后空闲/完成
  const sorted = [...agents].sort((a, b) => rank(a.status) - rank(b.status))

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
          // minWidth:0 必须写：flex 子项默认 min-width:auto，
          // 会被卡片内容撑开，导致溢出而不是出现横向滚动条
          minWidth: 0,
          minHeight: 0,
        }}
      >
        {sorted.map((a) => (
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

function FleetEmpty({
  onQuickCreate,
  onCreateRequest,
}: {
  onQuickCreate?: (name: string, role: string) => void
  onCreateRequest?: () => void
}) {
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

function FleetSummary({ agents }: { agents: Agent[] }) {
  const running = agents.filter((a) => a.status === 'running').length
  const queued = agents.filter((a) => a.status === 'queued').length
  const blocked = agents.filter((a) => a.status === 'blocked').length
  const totalChars = agents.reduce((n, a) => n + (a.chars ?? 0), 0)

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--space-3)', fontSize: 'var(--text-xs)' }}>
      {blocked > 0 && (
        <span className="c-fleet-pill c-fleet-pill--danger">⚠ {blocked} 个在等你</span>
      )}
      {running > 0 && (
        <span className="c-fleet-pill c-fleet-pill--live">
          <span className="c-live-dot" /> {running} 运行中
        </span>
      )}
      {queued > 0 && (
        <span className="c-fleet-pill c-fleet-pill--warning">◷ {queued} 排队</span>
      )}
      {totalChars > 0 && (
        <span className="mono" style={{ color: 'var(--text-disabled)' }}>
          {formatChars(totalChars)} 字
        </span>
      )}
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
  const active = agent.status === 'running' && live.length > 0
  const status = STATUS[agent.status] ?? STATUS.idle

  const msgs = agent.messages ?? []
  const task = [...msgs].reverse().find((m) => m.role === 'user')
  const lastReply = [...msgs].reverse().find((m) => m.role === 'assistant')
  const chars = agent.chars ?? 0

  return (
    <div
      className={`c-fleet-card ${active ? 'c-fleet-card--live' : ''} ${
        agent.status === 'blocked' ? 'c-fleet-card--blocked' : ''
      }`}
      style={{ ['--card-accent' as string]: agent.color }}
    >
      {/* 顶部身份条：颜色即身份 */}
      <div className="c-fleet-card__accent" />

      <div style={{ padding: '10px 12px', borderBottom: '1px solid var(--border-subtle)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 7, marginBottom: 7 }}>
          <span
            className="c-fleet-avatar"
            style={{ background: agent.color }}
            aria-hidden
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
            className={`c-fleet-status ${active ? 'c-fleet-status--live' : ''}`}
            style={{ color: status.color, marginLeft: 'auto' }}
            title={status.label}
          >
            <span>{status.icon}</span>
            {status.label}
          </span>
        </div>

        {/* 指标带：证明它在动，而不是卡住 */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            fontSize: 10,
            color: 'var(--text-disabled)',
            fontFamily: 'var(--font-mono)',
          }}
        >
          {agent.status === 'running' && agent.startedAt ? (
            <Elapsed from={agent.startedAt} />
          ) : (
            <span>—</span>
          )}
          <span title="本轮产出">{formatChars(chars)} 字</span>
          {(agent.queueLen ?? 0) > 0 && (
            <span style={{ color: 'var(--warning)' }}>队列 {agent.queueLen}</span>
          )}
          {agent.lastTool && (
            <span
              title="最近工具"
              style={{
                marginLeft: 'auto',
                color: 'var(--text-tertiary)',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
                maxWidth: 96,
              }}
            >
              ⚒ {agent.lastTool}
            </span>
          )}
        </div>

        {/* 产出强度条：文字在涨，条就在涨 */}
        <div className="c-fleet-meter" title={`本轮已生成 ${chars} 字`}>
          <div
            className="c-fleet-meter__fill"
            style={{
              width: `${Math.min(100, (chars / 800) * 100)}%`,
              background: agent.color,
            }}
          />
        </div>

        {agent.status === 'blocked' && agent.blockedReason && (
          <div className="c-fleet-blocked">{agent.blockedReason}</div>
        )}
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: '10px 12px', minHeight: 0 }}>
        <div style={labelStyle}>当前任务</div>
        {task ? (
          <div className="c-fleet-task">{stripPrefix(task.content)}</div>
        ) : (
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>还没有任务</div>
        )}

        {live ? (
          <div style={{ marginTop: 'var(--space-3)' }}>
            <div style={labelStyle}>实时输出</div>
            <div className="c-fleet-stream">
              {live.slice(-600)}
              <span className="c-caret" style={{ background: agent.color }} />
            </div>
          </div>
        ) : lastReply ? (
          <div style={{ marginTop: 'var(--space-3)' }}>
            <div style={labelStyle}>最近产出</div>
            <div className="c-fleet-reply">{lastReply.content}</div>
          </div>
        ) : null}
      </div>

      <div
        style={{
          padding: 8,
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
  return <span style={{ color: 'var(--success)' }}>⏱ {text}</span>
}

function formatChars(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

/** stripPrefix 去掉派发消息里的内部标记，只留正文。 */
function stripPrefix(text: string): string {
  return text
    .replace(/^\[(from|to)\s+[^\]]+\]\s*/, '')
    .replace(/^\[ch-\d+\]\s*/, '')
    .replace(/^\[(user|assistant|system)\]\s*/i, '')
}

function rank(status: string): number {
  switch (status) {
    case 'blocked':
      return 0
    case 'running':
      return 1
    case 'queued':
      return 2
    case 'error':
      return 3
    default:
      return 4
  }
}

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
  marginBottom: 4,
}
