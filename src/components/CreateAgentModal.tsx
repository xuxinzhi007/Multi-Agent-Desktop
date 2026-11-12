import { useEffect, useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'

type Props = {
  open: boolean
  onClose: () => void
  onCreate: (name: string, role: string, backend: string) => void
}

export default function CreateAgentModal({ open, onClose, onCreate }: Props) {
  const [name, setName] = useState('')
  const [role, setRole] = useState(ROLE_PRESETS[0].role)
  const [backend, setBackend] = useState('mock')

  useEffect(() => {
    if (open) {
      setName('')
      setRole(ROLE_PRESETS[0].role)
      setBackend('mock')
    }
  }, [open])

  if (!open) return null

  const submit = () => {
    const n = name.trim()
    if (!n) return
    onCreate(n, role, backend)
    onClose()
  }

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.6)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
        backdropFilter: 'blur(4px)',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 360,
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-lg)',
          boxShadow: 'var(--shadow-lg)',
          overflow: 'hidden',
        }}
      >
        {/* 标题 */}
        <div
          style={{
            padding: '16px 20px',
            borderBottom: '1px solid var(--border-subtle)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>新建 Agent</span>
          <button
            onClick={onClose}
            style={{
              width: 24,
              height: 24,
              borderRadius: 'var(--radius-sm)',
              border: 'none',
              background: 'transparent',
              color: 'var(--text-disabled)',
              cursor: 'pointer',
              fontSize: 16,
            }}
          >
            ×
          </button>
        </div>

        {/* 表单 */}
        <div style={{ padding: '20px', display: 'grid', gap: 14 }}>
          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>
              名称
            </label>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit()
                if (e.key === 'Escape') onClose()
              }}
              placeholder="例如：后端开发"
              style={{
                width: '100%',
                padding: '8px 12px',
                borderRadius: 'var(--radius-sm)',
                border: '1px solid var(--border-default)',
                background: 'var(--bg-surface)',
                color: 'var(--text-primary)',
                fontSize: 'var(--text-sm)',
              }}
            />
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>
              角色
            </label>
            <select
              value={role}
              onChange={(e) => setRole(e.target.value)}
              style={{
                width: '100%',
                padding: '8px 12px',
                borderRadius: 'var(--radius-sm)',
                border: '1px solid var(--border-default)',
                background: 'var(--bg-surface)',
                color: 'var(--text-primary)',
                fontSize: 'var(--text-sm)',
              }}
            >
              {ROLE_PRESETS.map((r) => (
                <option key={r.name} value={r.role}>
                  {r.name}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>
              后端
            </label>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8 }}>
              {[
                { id: 'mock', label: 'Mock', desc: '内置' },
                { id: 'claude', label: 'Claude', desc: 'Code' },
                { id: 'codex', label: 'Codex', desc: 'Exec' },
              ].map((b) => (
                <button
                  key={b.id}
                  onClick={() => setBackend(b.id)}
                  style={{
                    padding: '10px 8px',
                    borderRadius: 'var(--radius-sm)',
                    border: `1px solid ${backend === b.id ? 'var(--accent-bright)' : 'var(--border-default)'}`,
                    background: backend === b.id ? 'var(--accent-dim)' : 'var(--bg-surface)',
                    color: backend === b.id ? 'var(--accent-bright)' : 'var(--text-secondary)',
                    cursor: 'pointer',
                    textAlign: 'center',
                  }}
                >
                  <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{b.label}</div>
                  <div style={{ fontSize: 'var(--text-xs)', opacity: 0.7 }}>{b.desc}</div>
                </button>
              ))}
            </div>
          </div>
        </div>

        {/* 按钮 */}
        <div
          style={{
            padding: '16px 20px',
            borderTop: '1px solid var(--border-subtle)',
            display: 'flex',
            gap: 10,
          }}
        >
          <button
            onClick={onClose}
            style={{
              flex: 1,
              padding: '10px',
              borderRadius: 'var(--radius-sm)',
              border: '1px solid var(--border-default)',
              background: 'transparent',
              color: 'var(--text-secondary)',
              cursor: 'pointer',
              fontSize: 'var(--text-sm)',
            }}
          >
            取消
          </button>
          <button
            onClick={submit}
            disabled={!name.trim()}
            style={{
              flex: 1,
              padding: '10px',
              borderRadius: 'var(--radius-sm)',
              border: 'none',
              background: name.trim() ? 'var(--accent-bright)' : 'var(--bg-surface)',
              color: name.trim() ? 'var(--bg-base)' : 'var(--text-disabled)',
              cursor: name.trim() ? 'pointer' : 'not-allowed',
              fontSize: 'var(--text-sm)',
              fontWeight: 600,
            }}
          >
            创建
          </button>
        </div>
      </div>
    </div>
  )
}
