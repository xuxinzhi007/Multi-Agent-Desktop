import { useEffect, useState } from 'react'
import type { AgentView, ApprovalView } from '../mygo'

/**
 * 写操作审批卡。
 *
 * 为什么必须是模态：写文件、跑命令是唯一会让用户「看不懂结果」的操作。
 * 把它塞进某个侧栏，用户切走后会以为 agent 卡住了。模态至少保证
 * 有人看见这张卡，并且知道允许之后会发生什么。
 */
export default function ApprovalModal({
  items,
  agents,
  onDecide,
  open = true,
}: {
  items: ApprovalView[]
  agents?: AgentView[]
  onDecide: (id: string, allow: boolean, remember: boolean) => Promise<void>
  /** 顶部待审批数点击后打开队列；默认打开，保留「审批到达即弹窗」的现有行为 */
  open?: boolean
}) {
  const item = items[0]
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // 沿 parentId 上溯≤3 层判子会话并取根名。后端 ApprovalView 补 RootAgentID
  // 之前的显示近似：让用户看出是主还是子会话在申请（总纲 §3.5）。
  const requester = agents?.find((a) => a.id === item?.agentId)
  const isSub = !!requester?.parentId
  let rootName = requester?.name || item?.agentName || item?.agentId || ''
  if (requester) {
    let cur: AgentView | undefined = requester
    for (let i = 0; i < 3 && cur?.parentId; i++) {
      cur = agents?.find((a) => a.id === cur!.parentId)
    }
    if (cur && cur.id !== requester.id) rootName = cur.name
  }

  useEffect(() => {
    setBusy(false)
    setErr('')
  }, [item?.id])

  if (!open || !item) return null

  const decide = async (allow: boolean, remember: boolean) => {
    setBusy(true)
    setErr('')
    try {
      await onDecide(item.id, allow, remember)
    } catch (e) {
      setErr(e instanceof Error && e.message ? e.message : '审批没有送达后端，请重试')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        zIndex: 200,
        display: 'grid',
        placeItems: 'center',
        padding: 24,
        background: 'rgba(0,0,0,0.55)',
        backdropFilter: 'blur(6px)',
        WebkitBackdropFilter: 'blur(6px)',
      }}
      role="dialog"
      aria-modal="true"
      aria-label="写操作审批"
    >
      <div
        style={{
          width: 640,
          maxWidth: '94vw',
          maxHeight: '86vh',
          display: 'flex',
          flexDirection: 'column',
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-xl)',
          boxShadow: 'var(--shadow-lg)',
          overflow: 'hidden',
        }}
      >
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border-subtle)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>需要你的批准</span>
            <span className="c-badge c-badge--warning" style={{ fontSize: 'var(--text-xs)' }}>
              {item.tool}
            </span>
            {items.length > 1 && (
              <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                还有 {items.length - 1} 个在排队
              </span>
            )}
          </div>
          <div style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {isSub ? (
              <>
                <span className="c-badge c-badge--warning" style={{ fontSize: 'var(--text-xs)', marginRight: 6 }}>子会话</span>
                {item.agentName || item.agentId} 申请 · 属于主会话 {rootName}
              </>
            ) : (
              <>{item.agentName || item.agentId} 想执行一次写操作</>
            )}
          </div>
        </div>

        <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '16px 20px' }}>
          <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{item.title || item.tool}</div>
          {item.detail && (
            <pre
              className="mono"
              style={{
                margin: '8px 0 0',
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
              {item.detail}
            </pre>
          )}

          {item.diff ? (
            <div style={{ marginTop: 12 }}>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>
                改动预览
              </div>
              <DiffView diff={item.diff} />
            </div>
          ) : (
            <div style={{ marginTop: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {item.tool === 'bash' ? '这条命令没有文件 diff，执行结果会写进审计日志。' : '这次操作没有可显示的 diff。'}
            </div>
          )}

          {err && (
            <div role="alert" style={{ marginTop: 12, fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
              {err}
            </div>
          )}
        </div>

        <div
          style={{
            display: 'flex',
            gap: 10,
            padding: '14px 20px',
            borderTop: '1px solid var(--border-subtle)',
            alignItems: 'center',
          }}
        >
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flex: 1, lineHeight: 1.5 }}>
            允许一次只放行这一次；{isSub ? '本子会话允许' : '本会话允许'}只对「{item.tool}」生效，删除该会话后失效。
          </span>
          <button
            type="button"
            className="c-btn c-btn--ghost c-btn--sm"
            disabled={busy}
            onClick={() => void decide(false, false)}
          >
            拒绝
          </button>
          <button
            type="button"
            className="c-btn c-btn--secondary c-btn--sm"
            disabled={busy}
            onClick={() => void decide(true, false)}
          >
            允许一次
          </button>
          <button
            type="button"
            className="c-btn c-btn--primary c-btn--sm"
            disabled={busy}
            onClick={() => void decide(true, true)}
          >
            {busy ? '提交中…' : isSub ? '本子会话允许' : '本会话允许'}
          </button>
        </div>
      </div>
    </div>
  )
}

function DiffView({ diff }: { diff: string }) {
  const lines = diff.split('\n').slice(0, 400)
  return (
    <pre
      className="mono"
      style={{
        margin: 0,
        padding: 10,
        background: 'var(--bg-surface)',
        border: '1px solid var(--border-subtle)',
        borderRadius: 'var(--radius-sm)',
        fontSize: 'var(--text-xs)',
        lineHeight: 1.55,
        maxHeight: 320,
        overflow: 'auto',
        whiteSpace: 'pre',
      }}
    >
      {lines.map((line, i) => {
        const add = line.startsWith('+') && !line.startsWith('+++')
        const del = line.startsWith('-') && !line.startsWith('---')
        return (
          <div
            key={`${i}:${line.slice(0, 12)}`}
            style={{
              color: add ? 'var(--success)' : del ? 'var(--danger)' : 'var(--text-secondary)',
              background: add
                ? 'color-mix(in srgb, var(--success) 8%, transparent)'
                : del
                  ? 'color-mix(in srgb, var(--danger) 8%, transparent)'
                  : undefined,
            }}
          >
            {line || ' '}
          </div>
        )
      })}
    </pre>
  )
}
