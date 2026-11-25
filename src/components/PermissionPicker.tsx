import { useEffect, useRef, useState } from 'react'
import { API } from '../mygo'

/**
 * PermissionPicker —— 这个会话能自己动手到什么程度。
 *
 * 三档，语义不重叠：
 *   逐次审批  每次写操作都问（默认）
 *   自动      工作目录内的写与命令直接执行
 *   只读      写操作直接拒绝，适合生产环境排查
 *
 * 刻意不做「完整权限」：工作目录是工具层的硬边界，
 * 审批模式不该能把它放开。危险命令的拦截也不受这里影响。
 */

const MODES = [
  { id: 'ask', label: '逐次审批', hint: '每次写操作都问你', icon: '◐' },
  { id: 'auto', label: '自动', hint: '工作目录内直接执行', icon: '▶' },
  { id: 'readonly', label: '只读', hint: '只查不改，写操作直接拒', icon: '◌' },
] as const

export default function PermissionPicker({ agentId }: { agentId: string }) {
  const [mode, setMode] = useState('ask')
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const box = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let dead = false
    API.agentPermission(agentId)
      .then((m) => {
        if (!dead) setMode(m || 'ask')
      })
      .catch(() => {})
    return () => {
      dead = true
    }
  }, [agentId])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (box.current && !box.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const pick = async (next: string) => {
    setBusy(true)
    try {
      const got = await API.setAgentPermission(agentId, next)
      setMode(got || next)
      setOpen(false)
    } catch {
      /* 改不动就保持原样，不假装成功 */
    } finally {
      setBusy(false)
    }
  }

  const cur = MODES.find((m) => m.id === mode) ?? MODES[0]

  return (
    <div className="c-perm" ref={box}>
      <button
        type="button"
        className={`c-perm__chip c-perm__chip--${cur.id}`}
        onClick={() => setOpen((v) => !v)}
        disabled={busy}
        aria-haspopup="menu"
        aria-expanded={open}
        title={`权限：${cur.label} — ${cur.hint}`}
      >
        <span aria-hidden>{cur.icon}</span>
        {cur.label}
      </button>
      {open && (
        <div className="c-perm__menu" role="menu">
          {MODES.map((m) => (
            <button
              key={m.id}
              type="button"
              role="menuitemradio"
              aria-checked={m.id === mode}
              className={`c-perm__item ${m.id === mode ? 'is-on' : ''}`}
              onClick={() => pick(m.id)}
            >
              <span className="c-perm__itemlabel">
                {m.label}
                {m.id === mode && <span className="c-perm__tick" aria-hidden>✓</span>}
              </span>
              <span className="c-perm__itemhint">{m.hint}</span>
            </button>
          ))}
          <div className="c-perm__note">
            工作目录是硬边界，任何一档都出不去；危险命令始终拦截。
          </div>
        </div>
      )}
    </div>
  )
}