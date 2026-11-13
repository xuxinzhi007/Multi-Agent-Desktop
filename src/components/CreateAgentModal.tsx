import { useEffect, useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'
import { Sidecar } from '../mygo'
import Modal from './Modal'

type Props = {
  open: boolean
  onClose: () => void
  onCreate: (name: string, role: string, backend: string, workDir?: string) => void
}

const FIELD_STYLE = {
  width: '100%',
  padding: '8px 12px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-sm)',
} as const

const LABEL_STYLE = {
  display: 'block',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-secondary)',
  marginBottom: 6,
} as const

const BACKENDS = [
  { id: 'mock', label: 'Mock', desc: '内置' },
  { id: 'claude', label: 'Claude', desc: 'Code' },
  { id: 'codex', label: 'Codex', desc: 'Exec' },
] as const

export default function CreateAgentModal({ open, onClose, onCreate }: Props) {
  const [name, setName] = useState('')
  const [role, setRole] = useState(ROLE_PRESETS[0].role)
  const [backend, setBackend] = useState('mock')
  const [workDir, setWorkDir] = useState('')

  useEffect(() => {
    if (open) {
      setName('')
      setRole(ROLE_PRESETS[0].role)
      setBackend('mock')
      setWorkDir('')
    }
  }, [open])

  const submit = () => {
    const n = name.trim()
    if (!n) return
    onCreate(n, role, backend, workDir.trim() || undefined)
    onClose()
  }

  const canSubmit = name.trim().length > 0

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="新建 Agent"
      width={400}
      footer={
        <>
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
            disabled={!canSubmit}
            style={{
              flex: 1,
              padding: '10px',
              borderRadius: 'var(--radius-sm)',
              border: 'none',
              background: canSubmit ? 'var(--accent-bright)' : 'var(--bg-surface)',
              color: canSubmit ? 'var(--bg-base)' : 'var(--text-disabled)',
              cursor: canSubmit ? 'pointer' : 'not-allowed',
              fontSize: 'var(--text-sm)',
              fontWeight: 600,
            }}
          >
            创建
          </button>
        </>
      }
    >
      <div style={{ padding: '20px', display: 'grid', gap: 14 }}>
        <div>
          <label style={LABEL_STYLE}>名称</label>
          <input
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') submit()
            }}
            placeholder="例如：后端开发"
            style={FIELD_STYLE}
          />
        </div>

        <div>
          <label style={LABEL_STYLE}>角色</label>
          <select value={role} onChange={(e) => setRole(e.target.value)} style={FIELD_STYLE}>
            {ROLE_PRESETS.map((r) => (
              <option key={r.name} value={r.role}>
                {r.name}
              </option>
            ))}
          </select>
        </div>

        <div>
          <label style={LABEL_STYLE}>工作目录（可选，仅本会话）</label>
          <div style={{ display: 'flex', gap: 6 }}>
            <input
              value={workDir}
              onChange={(e) => setWorkDir(e.target.value)}
              placeholder="留空则跟随全局默认"
              className="mono"
              style={{ ...FIELD_STYLE, flex: 1, minWidth: 0, fontSize: 'var(--text-xs)' }}
            />
            <button
              className="c-btn c-btn--secondary c-btn--sm"
              onClick={async () => {
                const picked = await Sidecar.pickDirectory()
                if (picked) setWorkDir(picked)
              }}
            >
              浏览…
            </button>
          </div>
        </div>

        <div>
          <label style={LABEL_STYLE}>后端</label>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8 }}>
            {BACKENDS.map((b) => (
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
    </Modal>
  )
}
