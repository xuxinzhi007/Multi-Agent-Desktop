import { useEffect, useRef, useState, type CSSProperties, type MouseEvent, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import MentionText, { type MentionTarget } from './MentionText'
import { MessageShots } from './ImageDraft'
import type { Block } from '../mygo'

/**
 * MessageRow —— 会话（AgentView）与频道（ChannelView）共用的消息行渲染层。
 *
 * 两套历史实现曾各自维护一套正文解析 / 复制 / 转派 / 折叠逻辑，这里收成一处：
 *  - 纯展示层：不读存储、不做排序（排序只走 src/lib/message.ts）；
 *  - 会话与频道的身份差异（用户右气泡、频道发言人头像行）通过 props 表达，不 fork 组件；
 *  - 只在一边有意义的能力（会话的模型/耗时/peer 调用卡、频道的投递回执/回填）以可选
 *    prop 或外层 children 落点放在各自视图里。
 */

/** Unix 秒 → HH:MM（会话侧需要秒时传 withSeconds）。缺时间戳返回空串。 */
export function formatClock(sec?: number, withSeconds = false): string {
  if (!sec) return ''
  return new Date(sec * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: withSeconds ? '2-digit' : undefined,
    hour12: false,
  })
}

/** 22px 方形首字母头像。颜色由调用方给，用户/Agent 各自兜底。 */
export function Avatar({
  letter,
  color,
  textColor,
  onClick,
  title,
  style,
}: {
  letter: string
  color: string
  textColor?: string
  onClick?: () => void
  title?: string
  style?: CSSProperties
}) {
  return (
    <span
      onClick={onClick}
      title={title}
      style={{
        width: 22,
        height: 22,
        borderRadius: 'var(--radius-sm)',
        background: color,
        color: textColor ?? 'var(--bg-base)',
        display: 'grid',
        placeItems: 'center',
        fontSize: 'var(--text-xs)',
        fontWeight: 700,
        flexShrink: 0,
        marginTop: 1,
        cursor: onClick ? 'pointer' : undefined,
        ...style,
      }}
    >
      {letter}
    </span>
  )
}

/**
 * StreamingMarkdown 把「每来一个 token 就整段重新解析」降到约 8 次/秒。
 *
 * 长回答里 ReactMarkdown 是最大的单帧成本：文本越长，解析越慢，
 * 但人眼分辨不出 16ms 和 120ms 的排版更新。正文仍然立刻累积进状态，
 * 只有解析按间隔跑；流结束后的那一帧做一次全量解析，保证最终内容准确。
 */
export function StreamingMarkdown({ text, live }: { text: string; live: boolean }) {
  const latest = useRef(text)
  latest.current = text
  const [shown, setShown] = useState(text)

  useEffect(() => {
    if (!live) return
    const id = window.setInterval(() => setShown(latest.current), 120)
    return () => window.clearInterval(id)
  }, [live])

  useEffect(() => {
    if (!live) setShown(text)
  }, [text, live])

  return <ReactMarkdown remarkPlugins={[remarkGfm]}>{live ? shown : text}</ReactMarkdown>
}

/** Markdown 表格外层包一个横向滚动容器（频道宽表用；会话侧历史上未包，保持原样）。 */
function ScrollTableMarkdown({ text }: { text: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        table: ({ children }) => (
          <div style={{ overflowX: 'auto' }}>
            <table>{children}</table>
          </div>
        ),
      }}
    >
      {text}
    </ReactMarkdown>
  )
}

/**
 * MessageBody —— 消息正文的唯一渲染口径：图片块 → @提及纯文本 → Markdown。
 * 会话与频道两边都从这里走，不再各写一套。
 */
export function MessageBody({
  blocks,
  text,
  mentionTargets,
  selfColor,
  variant,
  live,
  tableScroll,
}: {
  blocks?: Block[]
  text: string
  mentionTargets?: MentionTarget[]
  selfColor: string
  variant: 'user' | 'agent'
  live?: boolean
  /** 表格外层包横向滚动容器（频道长表）；会话侧不传，保持历史排版 */
  tableScroll?: boolean
}) {
  if ((blocks?.length ?? 0) > 0) {
    return <MessageShots blocks={blocks} content={text} />
  }
  if (variant === 'user') {
    return <MentionText text={text} agents={mentionTargets} selfColor={selfColor} />
  }
  if (live) return <StreamingMarkdown text={text} live />
  return tableScroll ? <ScrollTableMarkdown text={text} /> : <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
}

/** 复制按钮：icon（会话悬停操作条）/ ghost（频道操作行）两种形态，copied 态内置。 */
export function CopyButton({
  text,
  variant = 'ghost',
  onCopy,
}: {
  text: string
  variant?: 'icon' | 'ghost'
  onCopy?: (text: string) => unknown
}) {
  const [copied, setCopied] = useState(false)
  const handle = () => {
    const done = () => {
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    }
    const ret = onCopy?.(text)
    if (ret && typeof (ret as Promise<void>).then === 'function') {
      ;(ret as Promise<void>).then(done).catch(() => {})
    } else {
      done()
    }
  }
  if (variant === 'icon') {
    return (
      <button type="button" className="c-icon-btn" title="复制 Markdown" onClick={handle}>
        复制
      </button>
    )
  }
  return (
    <button type="button" className="c-btn c-btn--ghost c-btn--sm" onClick={handle}>
      {copied ? '已复制' : '复制'}
    </button>
  )
}

export type ForwardItem = { id: string; label: string }

/**
 * 转派 / Route 下拉菜单。会话 Route 与频道转派共用同一交互，
 * 仅按钮文案、菜单位置与类名通过 props 区分。
 */
export function ForwardMenu({
  label,
  items,
  onPick,
  title,
  triggerClassName,
  wrapperClassName,
  menuClassName,
  itemClassName,
}: {
  label: string
  items: ForwardItem[]
  onPick: (id: string) => void
  title?: string
  triggerClassName?: string
  wrapperClassName?: string
  menuClassName?: string
  itemClassName?: string
}) {
  const [open, setOpen] = useState(false)
  if (items.length === 0) return null
  return (
    <span className={wrapperClassName} style={wrapperClassName ? undefined : { position: 'relative' }}>
      <button type="button" title={title} className={triggerClassName} onClick={() => setOpen((v) => !v)}>
        {label}
      </button>
      {open && (
        <div className={menuClassName}>
          {items.map((it) => (
            <button
              key={it.id}
              type="button"
              className={itemClassName}
              onClick={() => {
                onPick(it.id)
                setOpen(false)
              }}
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
    </span>
  )
}

/**
 * MessageRow —— 频道风格的整行消息（头像 + 姓名行 + 正文 + 操作行 + 尾部附加）。
 * 会话视图因左右分栏气泡与头部元信息布局不同，保留自己的外壳，但正文 / 复制 /
 * 转派 / 头像等原语都复用本文件导出的共享件。
 */
export function MessageRow({
  side,
  name,
  color,
  blocks,
  text,
  mentionTargets,
  timestamp,
  live,
  clamp,
  copyable,
  forwardItems,
  onForward,
  onProfile,
  onRefill,
  footer,
  onContextMenu,
  bodyClassName,
  tableScroll,
}: {
  side: 'user' | 'agent'
  name: string
  color: string
  blocks?: Block[]
  text: string
  mentionTargets?: MentionTarget[]
  timestamp?: number
  live?: boolean
  /** 长回复限高折叠（≥3 并发场景） */
  clamp?: boolean
  copyable?: boolean
  forwardItems?: ForwardItem[]
  onForward?: (id: string, text: string) => void
  onProfile?: () => void
  onRefill?: (text: string) => void
  /** 尾部附加（频道投递回执行等） */
  footer?: ReactNode
  onContextMenu?: (e: MouseEvent) => void
  bodyClassName?: string
  tableScroll?: boolean
}) {
  const [expanded, setExpanded] = useState(false)
  const isUser = side === 'user'
  const letter = name.slice(0, 1).toUpperCase()
  const clock = formatClock(timestamp)
  const bodyClass =
    !isUser && (blocks?.length ?? 0) === 0 ? ['markdown-body', 'c-channel-card', bodyClassName].filter(Boolean).join(' ') : bodyClassName

  return (
    <div
      data-msg-row={side}
      className="fade-up"
      style={{ display: 'flex', gap: 'var(--space-3)', marginBottom: 'var(--space-4)' }}
      onContextMenu={onContextMenu}
    >
      <Avatar
        letter={letter}
        color={isUser ? 'var(--bg-surface-active)' : color}
        textColor={isUser ? 'var(--text-secondary)' : undefined}
        onClick={onProfile}
        title={onProfile ? '看他的个人主页' : undefined}
      />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            marginBottom: 3,
            fontSize: 'var(--text-xs)',
            fontWeight: 600,
            color: isUser ? 'var(--text-disabled)' : color,
          }}
        >
          {onProfile ? (
            <span onClick={onProfile} style={{ cursor: 'pointer' }} title="看他的个人主页">
              {name}
            </span>
          ) : (
            name
          )}
          {clock && (
            <span style={{ marginLeft: 'auto', fontWeight: 400, color: 'var(--text-disabled)' }}>{clock}</span>
          )}
        </div>
        <div
          data-msg-body
          onClick={onRefill ? () => onRefill(text) : undefined}
          title={onRefill ? '点击回填到输入框，修改后重发' : undefined}
          className={bodyClass}
          style={{
            fontSize: 'var(--text-md)',
            lineHeight: 1.7,
            wordBreak: 'break-word',
            color: 'var(--text-secondary)',
            whiteSpace: isUser ? 'pre-wrap' : undefined,
            cursor: onRefill ? 'pointer' : undefined,
            maxHeight: clamp && !expanded ? '10.2em' : undefined,
            overflow: clamp && !expanded ? 'hidden' : undefined,
          }}
        >
          <MessageBody
            blocks={blocks}
            text={text}
            mentionTargets={mentionTargets}
            selfColor={color}
            variant={side}
            live={live}
            tableScroll={tableScroll}
          />
        </div>
        {clamp && (
          <button className="c-btn c-btn--ghost c-btn--sm" onClick={() => setExpanded((v) => !v)}>
            {expanded ? '收起' : '展开全文'}
          </button>
        )}
        {!isUser && ((copyable ?? forwardItems?.length) ? (
          <div className="c-channel-actions">
            {copyable && <CopyButton text={text} onCopy={(t) => navigator.clipboard.writeText(t)} />}
            {onForward && forwardItems && forwardItems.length > 0 && (
              <ForwardMenu
                label="转派"
                items={forwardItems}
                triggerClassName="c-btn c-btn--ghost c-btn--sm"
                menuClassName="c-channel-forward"
                onPick={(id) => onForward(id, text)}
              />
            )}
          </div>
        ) : null)}
        {footer}
      </div>
    </div>
  )
}
