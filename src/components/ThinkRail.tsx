import { useEffect, useRef, useState, type ReactNode } from 'react'

/**
 * 思考链的唯一实现。
 *
 * 单聊（AgentView）和频道（ChannelView）共用这里，避免两套样式各写一遍：
 * 之前频道只有一个「正在思考中…」的静态提示，单聊有工具步骤 + 等待计时，
 * 同一个 agent 在两个视图里看起来像两个产品。
 */
export type ThinkStep = {
  tool: string
  target: string
  status: 'running' | 'ok' | 'error' | 'waiting' | 'cancelled'
  detail?: string
}

export type Pace = { since: number; first?: number; heard?: 'reason' | 'text' }

const TOOL_LABEL: Record<string, string> = {
  read_file: '读取',
  write_file: '写入',
  edit_file: '修改',
  bash: '执行',
}

export function stepText(tool: string, target: string) {
  const label = TOOL_LABEL[tool] ?? tool
  const prefix = tool + ' '
  const detail = target.startsWith(prefix) ? target.slice(prefix.length) : target
  const line = detail.replace(/\s+/g, ' ').trim()
  if (!line || line === tool) return label
  return line.length > 80 ? `${label} ${line.slice(0, 80)}…` : `${label} ${line}`
}

export function formatWait(secs: number): string {
  if (secs < 60) return `${secs}s`
  const m = Math.floor(secs / 60)
  const s = secs % 60
  return `${m}分${String(s).padStart(2, '0')}秒`
}

export function ThinkRail({
  live,
  title,
  meta,
  tone,
  children,
}: {
  live?: boolean
  title: string
  meta?: string
  tone?: 'wait' | 'think' | 'tool'
  children?: ReactNode
}) {
  const [open, setOpen] = useState(!!live)
  const bodyRef = useRef<HTMLDivElement>(null)
  const pin = useRef(true)
  const onBodyScroll = () => {
    const el = bodyRef.current
    if (!el) return
    pin.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
  }
  useEffect(() => {
    const el = bodyRef.current
    if (!el || !open || !pin.current) return
    el.scrollTop = el.scrollHeight
  }, [children, open])
  const hasBody = children != null && children !== false && children !== ''
  const pulse = live ? `c-think__pulse c-think__pulse--${tone ?? 'wait'}` : 'c-think__pulse c-think__pulse--done'
  return (
    <div className={live ? 'c-think c-think--live' : 'c-think'}>
      <button
        type="button"
        className="c-think__bar"
        onClick={() => hasBody && setOpen((v) => !v)}
        aria-expanded={hasBody ? open : undefined}
      >
        <span className={pulse} />
        <span className="c-think__title">{title}</span>
        {meta && <span className="c-think__meta">{meta}</span>}
        {hasBody && <span className="c-think__toggle">{open ? '收起' : '展开'}</span>}
      </button>
      {open && hasBody && (
        <div className="c-think__body" ref={bodyRef} onScroll={onBodyScroll}>
          {children}
        </div>
      )}
    </div>
  )
}

export function ReasoningCard({ text, live }: { text: string; live?: boolean }) {
  return (
    <ThinkRail live={live} title={live ? '思考中' : '思考'}>
      {text}
    </ThinkRail>
  )
}

export function Thinking({
  steps,
  reasoning,
  pace,
}: {
  steps: ThinkStep[]
  reasoning?: string
  pace?: Pace
}) {
  const current = [...steps].reverse().find((s) => s.status === 'running')
  const latest = [...steps].reverse().find((s) => s.status !== 'running' && s.detail)
  const thinking = !current && pace?.heard === 'reason'
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!pace) return
    const id = window.setInterval(() => setNow(Date.now()), 500)
    return () => window.clearInterval(id)
  }, [pace?.since])
  const waited = pace ? Math.max(0, Math.round((now - pace.since) / 1000)) : 0
  const label = current
    ? stepText(current.tool, current.target)
    : thinking
      ? '思考中'
      : latest?.detail
        ? latest.detail
        : waited >= 20
          ? '模型还没有返回'
          : '在等模型'
  const parts = [label]
  const showingResult = !current && !thinking && !!latest?.detail
  if (!showingResult && pace) parts.push(`已等 ${formatWait(waited)}`)
  if (!showingResult && pace?.first != null) parts.push(`首字 ${(pace.first / 1000).toFixed(1)}s`)
  return (
    <ThinkRail live title={parts.join(' · ')} tone={current ? 'tool' : thinking ? 'think' : 'wait'}>
      {reasoning || undefined}
    </ThinkRail>
  )
}
