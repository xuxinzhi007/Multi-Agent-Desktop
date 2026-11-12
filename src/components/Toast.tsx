import { useEffect, useState } from 'react'

export type Toast = {
  id: string
  message: string
  type: 'success' | 'error' | 'info'
  duration?: number
}

let toastId = 0
const listeners: ((toast: Toast) => void)[] = []

export function toast(message: string, type: Toast['type'] = 'info', duration = 3000) {
  const t: Toast = { id: `toast-${++toastId}`, message, type, duration }
  listeners.forEach((fn) => fn(t))
}

export function toastSuccess(message: string, duration?: number) {
  toast(message, 'success', duration)
}

export function toastError(message: string, duration?: number) {
  toast(message, 'error', duration)
}

export function toastInfo(message: string, duration?: number) {
  toast(message, 'info', duration)
}

export default function ToastContainer() {
  const [toasts, setToasts] = useState<Toast[]>([])

  useEffect(() => {
    const listener = (t: Toast) => {
      setToasts((prev) => [...prev, t])
      setTimeout(() => {
        setToasts((prev) => prev.filter((x) => x.id !== t.id))
      }, t.duration ?? 3000)
    }
    listeners.push(listener)
    return () => {
      const idx = listeners.indexOf(listener)
      if (idx > -1) listeners.splice(idx, 1)
    }
  }, [])

  const colors = {
    success: { bg: 'var(--success)', icon: '✓' },
    error: { bg: 'var(--error)', icon: '✕' },
    info: { bg: 'var(--accent-bright)', icon: 'ℹ' },
  }

  return (
    <div
      style={{
        position: 'fixed',
        top: 16,
        right: 16,
        zIndex: 9999,
        display: 'flex',
        flexDirection: 'column',
        gap: 8,
        pointerEvents: 'none',
      }}
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          className="fade-up"
          style={{
            background: 'var(--bg-elevated)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-md)',
            padding: '10px 14px',
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            boxShadow: 'var(--shadow-lg)',
            minWidth: 200,
            maxWidth: 320,
          }}
        >
          <span
            style={{
              width: 18,
              height: 18,
              borderRadius: '50%',
              background: colors[t.type].bg,
              color: 'var(--bg-base)',
              display: 'grid',
              placeItems: 'center',
              fontSize: 11,
              fontWeight: 700,
              flexShrink: 0,
            }}
          >
            {colors[t.type].icon}
          </span>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-primary)' }}>
            {t.message}
          </span>
        </div>
      ))}
    </div>
  )
}
