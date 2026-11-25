import { useEffect, useState } from 'react'
import {
  EmptyState,
  ErrorState,
  PageBody,
  PageHeader,
  SkeletonRows,
} from './pageKit'
import {
  decideGatewayApproval,
  fetchGatewayBindings,
  formatUptime,
  useGatewayStatus,
  type GatewayApprovalView,
  type GatewayBindingView,
} from '../../lib/gateway'

/**
 * 网关页（内嵌形态）。
 *
 * 数据全部来自 MyGo IPC（API.GatewayStatus / GatewayBindings / GatewayDecideApproval），
 * 不写死假数据：进程状态、wired 四端口、接管渠道、绑定数、Agent 列表、
 * 待审批、SSE 订阅数、运行时长。网关只接管 web；飞书等渠道仍走现有 Hub，
 * 本页不发起第二条飞书长连接。
 *
 * 四态齐全：加载 / 读取错误（可重试）/ 未启用（gateway:false 或运行时缺失）/ 已接入。
 */

const WIRED_PORTS = [
  { key: 'session', label: '会话' },
  { key: 'approval', label: '审批' },
  { key: 'sender', label: '发送' },
  { key: 'binder', label: '绑定' },
] as const

const PANEL_STYLE: React.CSSProperties = {
  border: '1px solid var(--border-subtle)',
  borderRadius: 'var(--radius-md)',
  padding: 'var(--space-3)',
  display: 'grid',
  gap: 10,
}

const SECTION_TITLE: React.CSSProperties = {
  fontSize: 'var(--text-sm)',
  fontWeight: 600,
}

const META_STYLE: React.CSSProperties = {
  fontSize: 'var(--text-xs)',
  color: 'var(--text-tertiary)',
}

function agentStatusLabel(status: string): string {
  switch (status) {
    case 'running':
      return '运行中'
    case 'queued':
      return '排队'
    case 'blocked':
    case 'waiting':
      return '等待审批'
    case 'failed':
    case 'error':
      return '错误'
    case 'idle':
      return '空闲'
    default:
      return status || '空闲'
  }
}

function formatTime(sec: number): string {
  if (!sec || sec <= 0) return ''
  return new Date(sec * 1000).toLocaleString()
}

export default function GatewayPage() {
  const { status, loading, error, unavailable, refresh } = useGatewayStatus(5000)
  const [bindings, setBindings] = useState<GatewayBindingView[]>([])
  const [deciding, setDeciding] = useState<{ id: string; allow: boolean } | null>(null)
  const [cardError, setCardError] = useState<Record<string, string>>({})
  const [denying, setDenying] = useState<Record<string, boolean>>({})
  const [reasonDraft, setReasonDraft] = useState<Record<string, string>>({})

  // 绑定详情单独拉一次；gw 未启用时保持空列表，不发请求。
  useEffect(() => {
    if (!status.gateway) {
      setBindings([])
      return
    }
    let stop = false
    fetchGatewayBindings()
      .then((list) => {
        if (!stop) setBindings(list)
      })
      .catch(() => {
        if (!stop) setBindings([])
      })
    return () => {
      stop = true
    }
  }, [status.gateway, status.bindings])

  const decide = async (appr: GatewayApprovalView, allow: boolean, remember: boolean) => {
    const reason = allow ? '' : (reasonDraft[appr.id] ?? '').trim()
    setDeciding({ id: appr.id, allow })
    setCardError((prev) => ({ ...prev, [appr.id]: '' }))
    try {
      await decideGatewayApproval(appr.id, allow, remember, reason)
      await refresh()
    } catch (e) {
      setCardError((prev) => ({
        ...prev,
        [appr.id]: e instanceof Error && e.message ? e.message : String(e),
      }))
    } finally {
      setDeciding(null)
    }
  }

  if (loading) {
    return (
      <div className="c-col" data-testid="gateway-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="网关" />
        <PageBody>
          <SkeletonRows rows={5} />
        </PageBody>
      </div>
    )
  }

  if (error && !unavailable) {
    return (
      <div className="c-col" data-testid="gateway-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="网关" />
        <PageBody>
          <ErrorState message={error} onRetry={() => void refresh()} />
        </PageBody>
      </div>
    )
  }

  if (unavailable || !status.gateway) {
    return (
      <div className="c-col" data-testid="gateway-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader
          title="网关"
          badge={<span className="c-badge c-badge--disabled">未启用</span>}
        />
        <PageBody>
          <EmptyState
            icon="🌐"
            title="网关未启用"
            hint="网关作为库内嵌在桌面主进程中，随应用启动、退出即关闭。当前处于未启用状态（未装配、初始化失败，或不在桌面运行时内）。飞书等现有渠道仍由本地 Hub 处理，不受影响。"
          >
            <span style={META_STYLE}>独立常驻服务是后续形态；打包后网关随应用一起启动。</span>
          </EmptyState>
        </PageBody>
      </div>
    )
  }

  const badge = status.listenError ? (
    <span className="c-badge c-badge--warning">端口占用</span>
  ) : status.running ? (
    <span className="c-badge c-badge--success">运行中</span>
  ) : (
    <span className="c-badge c-badge--disabled">已停止</span>
  )

  return (
    <div className="c-col" data-testid="gateway-page" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader
        title="网关"
        badge={badge}
        right={
          <>
            <span style={META_STYLE}>运行时长 {formatUptime(status.uptimeSec)}</span>
            <span style={META_STYLE}>SSE 订阅 {status.sseSubscribers}</span>
          </>
        }
      />
      <PageBody>
        <div style={{ display: 'grid', gap: 'var(--space-3)' }}>
          {/* 进程与端口概览 */}
          <section style={PANEL_STYLE} aria-label="网关概览">
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, alignItems: 'center' }}>
              <span style={META_STYLE}>监听地址</span>
              <span className="mono" style={{ fontSize: 'var(--text-xs)' }}>
                {status.addr || '—'}
              </span>
              <span style={META_STYLE}>绑定 {status.bindings}</span>
              <span style={META_STYLE}>接管渠道</span>
              {status.channels.length === 0 ? (
                <span style={META_STYLE}>—</span>
              ) : (
                status.channels.map((ch) => (
                  <span key={ch} className="c-badge c-badge--accent">
                    {ch}
                  </span>
                ))
              )}
            </div>

            {status.listenError && (
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                监听失败：{status.listenError}（桌面已继续启动，网关 HTTP/SSE 未就绪）
              </div>
            )}

            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 12 }}>
              {WIRED_PORTS.map((p) => {
                const on = !!status.wired[p.key]
                return (
                  <span key={p.key} className="c-badge" style={{ gap: 6 }}>
                    <span className={`c-dot ${on ? 'c-dot--connected' : 'c-dot--idle'}`} aria-hidden />
                    {p.label}（{p.key}）
                    <span className={on ? '' : 'c-badge--disabled'} style={{ fontSize: 'var(--text-xs)' }}>
                      {on ? '已接线' : '未接线'}
                    </span>
                  </span>
                )
              })}
            </div>

            <div style={META_STYLE}>
              飞书等渠道仍走桌面现有 Hub；网关只接管上表列出的渠道，未接管渠道会被明确拒绝。
            </div>
          </section>

          {/* 渠道绑定 */}
          <section style={PANEL_STYLE} aria-label="渠道绑定">
            <div style={SECTION_TITLE}>渠道绑定</div>
            {bindings.length === 0 ? (
              <div style={META_STYLE}>暂无渠道绑定。外部会话绑定到本地 Agent 后会列在这里。</div>
            ) : (
              <div style={{ display: 'grid', gap: 8 }}>
                {bindings.map((b, i) => (
                  <div
                    key={`${b.channel}:${b.externalChatId}:${i}`}
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: 10,
                      alignItems: 'center',
                      fontSize: 'var(--text-xs)',
                    }}
                  >
                    <span className="c-badge c-badge--accent">{b.channel}</span>
                    <span className="mono">{b.externalChatId}</span>
                    <span style={{ color: 'var(--text-secondary)' }}>→ {b.agentId}</span>
                    {b.policy && <span style={META_STYLE}>策略 {b.policy}</span>}
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* Agent 列表 */}
          <section style={PANEL_STYLE} aria-label="会话列表">
            <div style={SECTION_TITLE}>会话（{status.agents.length}）</div>
            {status.agents.length === 0 ? (
              <div style={META_STYLE}>网关视角下暂时没有可见会话。</div>
            ) : (
              <div style={{ display: 'grid', gap: 8 }}>
                {status.agents.map((a) => (
                  <div
                    key={a.id}
                    style={{
                      display: 'flex',
                      flexWrap: 'wrap',
                      gap: 10,
                      alignItems: 'center',
                      fontSize: 'var(--text-xs)',
                    }}
                  >
                    <span style={{ fontWeight: 600 }}>{a.name || a.id}</span>
                    <span className="mono" style={{ color: 'var(--text-tertiary)' }}>
                      {a.id}
                    </span>
                    <span className="c-badge">{agentStatusLabel(a.status)}</span>
                    <span style={META_STYLE}>排队 {a.queueLen}</span>
                    {a.lastError && (
                      <span style={{ color: 'var(--danger)' }} title={a.lastError}>
                        {a.lastError}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </section>

          {/* 待审批 */}
          <section style={PANEL_STYLE} aria-label="待审批">
            <div style={SECTION_TITLE}>待审批（{status.pendingApprovals.length}）</div>
            {status.pendingApprovals.length === 0 ? (
              <div style={META_STYLE}>没有待审批的写操作。</div>
            ) : (
              <div style={{ display: 'grid', gap: 12 }}>
                {status.pendingApprovals.map((appr) => (
                  <ApprovalCard
                    key={appr.id}
                    approval={appr}
                    busy={deciding?.id === appr.id}
                    error={cardError[appr.id] ?? ''}
                    denying={!!denying[appr.id]}
                    reason={reasonDraft[appr.id] ?? ''}
                    onReasonChange={(v) => setReasonDraft((p) => ({ ...p, [appr.id]: v }))}
                    onToggleDeny={() => setDenying((p) => ({ ...p, [appr.id]: !p[appr.id] }))}
                    onDecide={(allow, remember) => void decide(appr, allow, remember)}
                  />
                ))}
              </div>
            )}
          </section>
        </div>
      </PageBody>
    </div>
  )
}

function ApprovalCard({
  approval,
  busy,
  error,
  denying,
  reason,
  onReasonChange,
  onToggleDeny,
  onDecide,
}: {
  approval: GatewayApprovalView
  busy: boolean
  error: string
  denying: boolean
  reason: string
  onReasonChange: (v: string) => void
  onToggleDeny: () => void
  onDecide: (allow: boolean, remember: boolean) => void
}) {
  const time = formatTime(approval.createdAt)
  return (
    <div
      style={{
        border: '1px solid var(--border-default)',
        borderRadius: 'var(--radius-sm)',
        padding: 12,
        display: 'grid',
        gap: 8,
      }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        <span className="c-badge c-badge--warning">{approval.tool}</span>
        <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>
          {approval.title || approval.tool}
        </span>
        <span style={META_STYLE}>
          {approval.agentName || approval.agentId}
          {time ? ` · ${time}` : ''}
        </span>
      </div>

      {approval.detail && (
        <pre
          className="mono"
          style={{
            margin: 0,
            padding: 10,
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-subtle)',
            borderRadius: 'var(--radius-sm)',
            fontSize: 'var(--text-xs)',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            maxHeight: 160,
            overflowY: 'auto',
          }}
        >
          {approval.detail}
        </pre>
      )}

      {approval.diff && (
        <pre
          className="mono"
          style={{
            margin: 0,
            padding: 10,
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-subtle)',
            borderRadius: 'var(--radius-sm)',
            fontSize: 'var(--text-xs)',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            maxHeight: 160,
            overflowY: 'auto',
          }}
        >
          {approval.diff}
        </pre>
      )}

      {error && (
        <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
          {error}
        </div>
      )}

      {denying && (
        <textarea
          rows={2}
          value={reason}
          onChange={(e) => onReasonChange(e.target.value)}
          placeholder="拒绝原因（可选，会带回后端）"
          style={{
            width: '100%',
            padding: '8px 12px',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border-default)',
            background: 'var(--bg-surface)',
            color: 'var(--text-primary)',
            fontSize: 'var(--text-sm)',
            fontFamily: 'inherit',
            resize: 'vertical',
          }}
        />
      )}

      <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
        {denying ? (
          <>
            <button
              type="button"
              className="c-btn c-btn--danger c-btn--sm"
              disabled={busy}
              onClick={() => onDecide(false, false)}
            >
              {busy ? '提交中…' : '确认拒绝'}
            </button>
            <button
              type="button"
              className="c-btn c-btn--ghost c-btn--sm"
              disabled={busy}
              onClick={onToggleDeny}
            >
              取消
            </button>
          </>
        ) : (
          <>
            <button
              type="button"
              className="c-btn c-btn--ghost c-btn--sm"
              disabled={busy}
              onClick={onToggleDeny}
            >
              拒绝
            </button>
            <button
              type="button"
              className="c-btn c-btn--secondary c-btn--sm"
              disabled={busy}
              onClick={() => onDecide(true, false)}
            >
              允许一次
            </button>
            <button
              type="button"
              className="c-btn c-btn--primary c-btn--sm"
              disabled={busy}
              onClick={() => onDecide(true, true)}
            >
              {busy ? '提交中…' : '本会话允许'}
            </button>
          </>
        )}
      </div>
    </div>
  )
}
