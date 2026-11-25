import type { ReactNode } from 'react'

/**
 * 三个「助手」页面（网关 / 记忆 / 计划）共用的状态骨架。
 *
 * 契约 §6.2 要求每个页面空态 / 加载态 / 错误态 / 禁用态齐全。
 * M1 阶段后端尚未接入，页面固定渲染禁用态；其余三态的分支先落好，
 * 后端 API 落地后只需把 phase 换成真实状态，不必重做布局。
 *
 * 原则：没有后端就明确写「未接入」并禁用按钮，不允许出现能点但没反应的控件。
 */
export type PagePhase = 'empty' | 'loading' | 'error' | 'disabled'

const headerStyle: React.CSSProperties = {
  height: 'var(--header-h)',
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: '0 var(--space-4)',
  borderBottom: '1px solid var(--border-subtle)',
  flexShrink: 0,
}

export function PageHeader({
  title,
  badge,
  right,
}: {
  title: string
  badge?: ReactNode
  right?: ReactNode
}) {
  return (
    <header style={headerStyle}>
      <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>{title}</span>
      {badge}
      {right && <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>{right}</span>}
    </header>
  )
}

export function PageBody({ children }: { children: ReactNode }) {
  return (
    <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: 'var(--space-4)' }}>
      {children}
    </div>
  )
}

/** 加载态骨架行：纯 CSS 占位，不编数据。 */
export function SkeletonRows({ rows = 3 }: { rows?: number }) {
  return (
    <div style={{ display: 'grid', gap: 10 }} role="status" aria-label="正在加载">
      {Array.from({ length: rows }, (_, i) => (
        <div
          key={i}
          className="c-skeleton"
          style={{ height: 44, width: `${100 - i * 14}%`, borderRadius: 'var(--radius-md)' }}
        />
      ))}
    </div>
  )
}

export function EmptyState({
  icon,
  title,
  hint,
  children,
}: {
  icon: string
  title: string
  hint?: string
  children?: ReactNode
}) {
  return (
    <div className="c-empty" style={{ flex: 1 }}>
      <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }} aria-hidden>
        {icon}
      </div>
      <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>{title}</div>
      {hint && (
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 20, maxWidth: 360 }}>
          {hint}
        </div>
      )}
      {children && <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, justifyContent: 'center' }}>{children}</div>}
    </div>
  )
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="c-empty" style={{ flex: 1 }}>
      <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }} aria-hidden>
        ⚠
      </div>
      <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>读取失败</div>
      <div role="alert" style={{ fontSize: 'var(--text-sm)', color: 'var(--danger)', marginBottom: 16, maxWidth: 360 }}>
        {message}
      </div>
      {/* 没有 onRetry 时禁用，不留死按钮 */}
      <button type="button" className="c-btn c-btn--secondary c-btn--sm" disabled={!onRetry} onClick={onRetry}>
        重试
      </button>
    </div>
  )
}

export function DisabledState({
  icon,
  title,
  description,
  note,
  children,
}: {
  icon: string
  title: string
  description: string
  note?: string
  children?: ReactNode
}) {
  return (
    <div className="c-empty" style={{ flex: 1 }}>
      <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }} aria-hidden>
        {icon}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>
        <span className="c-badge c-badge--disabled">未接入</span>
        {title}
      </div>
      <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 20, maxWidth: 420, lineHeight: 'var(--leading-relaxed)' }}>
        {description}
      </div>
      {children && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, justifyContent: 'center' }}>{children}</div>
      )}
      {note && (
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginTop: 12 }}>{note}</div>
      )}
    </div>
  )
}
