import { useEffect, useRef, useState } from 'react'

/**
 * Select —— 自定义下拉。
 *
 * 为什么不用原生 <select>：原生控件的展开列表由系统绘制，
 * 在深色界面上会弹出一个白底灰字的系统菜单，跟整体视觉完全脱节，
 * 而且没法做悬停反馈、图标、当前项高亮。
 *
 * 这里自己实现，键盘可达（↑↓ / Enter / Esc），点击外部关闭。
 */
export default function Select({
  value,
  options,
  onChange,
  placeholder = '请选择…',
  width,
}: {
  value: string
  options: { value: string; label: string; hint?: string }[]
  onChange: (v: string) => void
  placeholder?: string
  width?: number | string
}) {
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const root = useRef<HTMLDivElement>(null)

  const current = options.find((o) => o.value === value)

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        return
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setActive((i) => Math.min(options.length - 1, i + 1))
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setActive((i) => Math.max(0, i - 1))
      }
      if (e.key === 'Enter' && active >= 0 && options[active]) {
        e.preventDefault()
        onChange(options[active].value)
        setOpen(false)
      }
    }
    window.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open, active, options, onChange])

  return (
    <div ref={root} style={{ position: 'relative', width: width ?? '100%' }}>
      <button
        type="button"
        onClick={() => {
          setOpen((v) => !v)
          setActive(options.findIndex((o) => o.value === value))
        }}
        aria-haspopup="listbox"
        aria-expanded={open}
        style={{
          width: '100%',
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          padding: '6px 9px',
          borderRadius: 'var(--radius-sm)',
          border: `1px solid ${open ? 'var(--accent-border)' : 'var(--border-default)'}`,
          background: 'var(--bg-surface)',
          color: current ? 'var(--text-primary)' : 'var(--text-disabled)',
          fontSize: 'var(--text-xs)',
          fontFamily: 'inherit',
          cursor: 'pointer',
          textAlign: 'left',
        }}
      >
        <span
          style={{
            flex: 1,
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {current?.label ?? placeholder}
        </span>
        <span style={{ opacity: 0.5, fontSize: 'var(--text-xs)', flexShrink: 0 }}>▾</span>
      </button>

      {open && (
        <div
          role="listbox"
          className="c-select-menu"
          style={{
            position: 'absolute',
            bottom: 'calc(100% + 4px)',
            left: 0,
            right: 0,
            zIndex: 1000,
            maxHeight: 220,
            overflowY: 'auto',
            background: 'var(--bg-elevated)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-md)',
            boxShadow: 'var(--shadow-lg)',
            padding: 3,
          }}
        >
          {options.length === 0 && (
            <div style={{ padding: '7px 9px', fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
              没有可选项
            </div>
          )}
          {options.map((o, i) => {
            const on = o.value === value
            return (
              <button
                key={o.value}
                type="button"
                role="option"
                aria-selected={on}
                onClick={() => {
                  onChange(o.value)
                  setOpen(false)
                }}
                onMouseEnter={() => setActive(i)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 6,
                  width: '100%',
                  padding: '5px 8px',
                  borderRadius: 'var(--radius-sm)',
                  border: 'none',
                  background: i === active ? 'var(--bg-surface-hover)' : 'transparent',
                  color: on ? 'var(--accent-bright)' : 'var(--text-secondary)',
                  fontSize: 'var(--text-xs)',
                  fontFamily: 'inherit',
                  cursor: 'pointer',
                  textAlign: 'left',
                }}
              >
                <span style={{ width: 10, flexShrink: 0 }}>{on ? '✓' : ''}</span>
                <span
                  style={{
                    flex: 1,
                    minWidth: 0,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {o.label}
                </span>
                {o.hint && (
                  <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', flexShrink: 0 }}>
                    {o.hint}
                  </span>
                )}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
