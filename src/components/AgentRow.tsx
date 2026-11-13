/**
 * AgentRow —— agent 的一行展示，侧边栏与悬浮岛共用。
 *
 * 依据 Claude Code `agent view` 的行结构：名字 + 一句话活动 + 年龄，
 * 颜色编码「要不要我管」，形状编码「进程活没活」。
 *
 * 差异只在 variant：
 *   sidebar —— 大行高，带删除按钮，显示最后消息预览
 *   island  —— 紧凑行高，显示年龄，无删除按钮
 */

export type AgentRowData = {
  id: string
  name: string
  role: string
  color: string
  status: string
  blockedReason?: string
  queueLen?: number
  messages?: { role: string; content: string }[]
  /** 本轮开始时间（Unix 秒），显示年龄用 */
  startedAt?: number
  /** 本轮已生成字符数 */
  chars?: number
  /** 最近一次工具调用 */
  lastTool?: string
}

const GLYPH: Record<string, string> = {
  blocked: '✻', // 实心星：要你的输入
  running: '✽', // 星：进程在动
  queued: '◌', // 空圈：排队
  done: '∙', // 小点：进程退出
  error: '✕', // 叉：出错
  idle: '∙', // 小点：闲着
}

const COLOR: Record<string, string> = {
  blocked: 'var(--danger)',
  error: 'var(--danger)',
  running: 'var(--warning)',
  queued: 'var(--accent-bright)',
  done: 'var(--success)',
  idle: 'var(--text-disabled)',
}

/** 一句话活动：优先展示「它在等你什么」，其次「它在干什么」。 */
function activityText(a: AgentRowData): string {
  if (a.status === 'blocked') return `⚠ ${a.blockedReason || '等你处理'}`
  if (a.status === 'error') return '出错'
  if (a.status === 'running') {
    const parts: string[] = []
    if (a.lastTool) parts.push(`🔧 ${a.lastTool}`)
    if (a.chars && a.chars > 0) parts.push(`${a.chars} 字`)
    return parts.length > 0 ? parts.join(' · ') : '正在处理…'
  }
  if (a.status === 'queued') return `排队中 (${a.queueLen ?? 0})`
  const last = a.messages?.filter((m) => m.role !== 'system').at(-1)
  if (last) {
    const t = last.content.replace(/\s+/g, ' ').trim()
    return t.length > 40 ? t.slice(0, 40) + '…' : t
  }
  return a.role
}

/** 年龄：从本轮开始算，没有则不显示。 */
function formatAge(startedAt?: number): string {
  if (!startedAt) return ''
  const secs = Math.max(0, Math.floor(Date.now() / 1000 - startedAt))
  if (secs < 60) return `${secs}s`
  const mins = Math.floor(secs / 60)
  if (mins < 60) return `${mins}m`
  return `${Math.floor(mins / 60)}h${mins % 60}m`
}

export default function AgentRow({
  agent,
  active,
  variant,
  onClick,
  onDelete,
}: {
  agent: AgentRowData
  active?: boolean
  variant: 'sidebar' | 'island'
  onClick?: () => void
  onDelete?: () => void
}) {
  const isIsland = variant === 'island'
  const color = COLOR[agent.status] ?? COLOR.idle
  const glyph = GLYPH[agent.status] ?? GLYPH.idle
  const age = isIsland ? formatAge(agent.startedAt) : ''

  return (
    <div
      onClick={onClick}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: isIsland ? 8 : 9,
        padding: isIsland ? '5px 10px' : '8px 10px 8px 11px',
        borderRadius: 'var(--radius-md)',
        cursor: onClick ? 'pointer' : 'default',
        background: active ? 'var(--bg-surface-active)' : 'transparent',
        transition: 'background var(--dur-fast) var(--ease)',
      }}
      onMouseEnter={(e) => {
        if (!active && onClick) e.currentTarget.style.background = 'var(--bg-surface)'
      }}
      onMouseLeave={(e) => {
        if (!active && onClick) e.currentTarget.style.background = 'transparent'
      }}
    >
      {/* 状态图标：颜色表达「要不要我管」，形状表达「进程活没活」 */}
      <span
        style={{
          width: isIsland ? 14 : 16,
          height: isIsland ? 14 : 16,
          display: 'grid',
          placeItems: 'center',
          color,
          fontSize: isIsland ? 11 : 12,
          fontWeight: 700,
          flexShrink: 0,
          animation: agent.status === 'running' ? 'breathe 2.6s ease-in-out infinite' : 'none',
        }}
        aria-label={agent.status}
      >
        {glyph}
      </span>

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
          {/* 名字用 agent 自己的颜色染 —— 照抄 agent view 的 tint */}
          <span
            style={{
              fontSize: isIsland ? 'var(--text-sm)' : 'var(--text-base)',
              fontWeight: active ? 600 : 500,
              color: agent.color || 'var(--text-primary)',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {agent.name}
          </span>
        </div>
        <div
          style={{
            fontSize: 'var(--text-xs)',
            color: agent.status === 'blocked' ? 'var(--danger)' : 'var(--text-disabled)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            marginTop: isIsland ? 0 : 1,
          }}
        >
          {activityText(agent)}
        </div>
      </div>

      {/* 年龄：只在一行里显示，不占第二行 */}
      {age && (
        <span
          className="mono"
          style={{ fontSize: 10, color: 'var(--text-disabled)', flexShrink: 0 }}
        >
          {age}
        </span>
      )}

      {!isIsland && onDelete && (
        <button
          className="c-icon-btn c-icon-btn--danger"
          onClick={(e) => {
            e.stopPropagation()
            onDelete()
          }}
          title="删除"
          style={{ opacity: 0.35, width: 20, height: 20, fontSize: 14 }}
          onMouseEnter={(e) => (e.currentTarget.style.opacity = '1')}
          onMouseLeave={(e) => (e.currentTarget.style.opacity = '0.35')}
        >
          ×
        </button>
      )}
    </div>
  )
}
