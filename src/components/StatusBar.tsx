/**
 * 顶部状态条（架构 §6.1）：网关 · 运行中 N · 待审批 M · 队列 K。
 *
 * 所有数字都来自现有前端 state 的实时计算，不编后端没有的数据：
 * - 运行中：agents 里 status === 'running' 的会话数
 * - 待审批：approvals 队列长度，点击打开审批队列（复用 ApprovalModal）
 * - 队列：各会话 queueLen 之和
 * - 网关：mad-gateway 后端尚未接入，固定「未接入」禁用态，不伪造在线 / 延迟
 */
type Props = {
  running: number
  approvals: number
  queue: number
  onOpenApprovals: () => void
}

const barStyle: React.CSSProperties = {
  height: 'var(--header-h)',
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-3)',
  padding: '0 var(--space-4)',
  borderBottom: '1px solid var(--border-subtle)',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-tertiary)',
  flexShrink: 0,
}

const statStyle: React.CSSProperties = {
  display: 'inline-flex',
  alignItems: 'center',
  gap: 4,
}

const numStyle: React.CSSProperties = {
  color: 'var(--text-secondary)',
  fontWeight: 600,
  fontVariantNumeric: 'tabular-nums',
}

export default function StatusBar({ running, approvals, queue, onOpenApprovals }: Props) {
  return (
    <header style={barStyle}>
      <span className="c-badge c-badge--disabled" title="网关后端尚未接入，暂不显示在线状态与延迟">
        <span className="c-dot c-dot--idle" aria-hidden />
        网关 未接入
      </span>

      <span style={statStyle}>
        运行中 <b style={numStyle}>{running}</b>
      </span>

      <button
        type="button"
        className={approvals > 0 ? 'c-badge c-badge--warning' : 'c-badge c-badge--disabled'}
        disabled={approvals === 0}
        onClick={onOpenApprovals}
        title={approvals > 0 ? '打开审批队列' : '当前没有待审批的写操作'}
        style={{ fontFamily: 'inherit', cursor: approvals > 0 ? 'pointer' : 'not-allowed' }}
      >
        待审批 <b style={numStyle}>{approvals}</b>
      </button>

      <span style={statStyle} title="所有会话排队中的消息总数">
        队列 <b style={numStyle}>{queue}</b>
      </span>
    </header>
  )
}
