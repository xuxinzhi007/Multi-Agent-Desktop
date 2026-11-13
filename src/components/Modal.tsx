import { Dialog } from 'radix-ui'
import type { ReactNode } from 'react'

/**
 * Modal —— 基于 radix-ui Dialog 的统一模态框。
 *
 * 手写版本缺的东西都在这里补上：
 *   · 焦点陷阱（Tab 不会跑到背景）
 *   · Esc 关闭
 *   · 打开时锁定背景滚动
 *   · aria-modal / aria-labelledby（屏幕阅读器）
 *   · 关闭后焦点还给触发元素
 */
export default function Modal({
  open,
  onClose,
  title,
  width = 470,
  children,
  footer,
}: {
  open: boolean
  onClose: () => void
  title: string
  /** 内容区宽度，默认 470 */
  width?: number
  children: ReactNode
  /** 底部按钮区，不传则不渲染 */
  footer?: ReactNode
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(v) => !v && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(0,0,0,0.5)',
            backdropFilter: 'blur(8px)',
            WebkitBackdropFilter: 'blur(8px)',
            zIndex: 100,
          }}
        />
        <Dialog.Content
          className="fade-up"
          style={{
            position: 'fixed',
            top: '50%',
            left: '50%',
            transform: 'translate(-50%, -50%)',
            width,
            maxWidth: '92vw',
            maxHeight: '86vh',
            display: 'flex',
            flexDirection: 'column',
            background: 'color-mix(in srgb, var(--bg-elevated) 88%, transparent)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-xl)',
            boxShadow: 'var(--shadow-lg)',
            backdropFilter: 'blur(20px)',
            WebkitBackdropFilter: 'blur(20px)',
            zIndex: 101,
            overflow: 'hidden',
          }}
        >
          {/* 标题栏 */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              padding: '16px 20px',
              borderBottom: '1px solid var(--border-subtle)',
              flexShrink: 0,
            }}
          >
            <Dialog.Title
              style={{ fontSize: 'var(--text-md)', fontWeight: 600, margin: 0 }}
            >
              {title}
            </Dialog.Title>
            <Dialog.Close asChild>
              <button
                className="c-icon-btn"
                style={{ marginLeft: 'auto' }}
                aria-label="关闭"
              >
                ×
              </button>
            </Dialog.Close>
          </div>

          {/* 内容 */}
          <div style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>{children}</div>

          {/* 底部 */}
          {footer && (
            <div
              style={{
                display: 'flex',
                gap: 10,
                padding: '16px 20px',
                borderTop: '1px solid var(--border-subtle)',
                flexShrink: 0,
              }}
            >
              {footer}
            </div>
          )}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
