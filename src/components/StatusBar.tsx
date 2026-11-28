/**
 * 顶部状态条（架构 §6.1）：网关 · 运行中 N · 待审批 M · 队列 K。
 *
 * 所有数字都来自现有前端 state 的实时计算，不编后端没有的数据：
 * - 运行中：agents 里 status === 'running' 的会话数
 * - 待审批：approvals 队列长度，点击打开审批队列（复用 ApprovalModal）
 * - 队列：各会话 queueLen 之和
 * - 网关：来自 GatewayStatus IPC 的轻量摘要。三态：运行中 / 端口占用 / 未启用；
 *   不再固定写「未接入」。摘要由 App 按挂载与低速轮询刷新，这里不自己发请求。
 */

export type GatewaySummary = {
  /** gateway:false（gw 未装配 / 运行时缺失）→ 未启用 */
  enabled: boolean
  running: boolean
  listenError: string
}

type Props = {
  running: number
  approvals: number
  queue: number
  gateway?: GatewaySummary
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

function gatewayBadge(g?: GatewaySummary): { cls: string; dot: string; text: string; title: string } {
  if (!g || !g.enabled) {
    return {
      cls: 'c-badge c-badge--disabled',
      dot: 'c-dot--idle',
      text: '网关 未启用',
      title: '网关未启用：未装配、初始化失败，或不在桌面运行时内',
    }
  }
  if (g.listenError) {
    return {
      cls: 'c-badge c-badge--warning',
      dot: 'c-dot--error',
      text: '网关 端口占用',
      title: g.listenError,
    }
  }
  if (g.running) {
    return {
      cls: 'c-badge c-badge--success',
      dot: 'c-dot--connected',
      text: '网关 运行中',
      title: '网关 HTTP/SSE 已在本地监听',
    }
  }
  return {
    cls: 'c-badge c-badge--disabled',
    dot: 'c-dot--idle',
    text: '网关 已停止',
    title: '网关已装配但当前未在监听',
  }
}

export default function StatusBar({ running, approvals, queue, gateway, onOpenApprovals }: Props) {
  const gw = gatewayBadge(gateway)
  return (
    <header style={barStyle}>
      <span className={gw.cls} title={gw.title}>
        <span className={gw.dot} aria-hidden />
        {gw.text}
      </span>

      {running > 0 || queue > 0 || approvals > 0 ? (
        <span style={statStyle} className={`c-status-stat ${running > 0 ? 'is-live' : ''}`} title="正在执行任务的会话数">
          <span className="c-status-stat__pulse" aria-hidden />
          运行中 <b style={numStyle}>{running}</b>
        </span>
      ) : (
        <span style={statStyle} className="c-status-stat" title="所有任务已完成">
          全部完成 <b style={numStyle}>✓</b>
        </span>
      )}

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

      <span style={statStyle} className={`c-status-stat ${queue > 0 ? 'is-live' : ''}`} title="所有会话排队中的消息总数">
        队列 <b style={numStyle}>{queue}</b>
      </span>
    </header>
  )
}
