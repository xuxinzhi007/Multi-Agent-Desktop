import { useEffect, useMemo, useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'
import { AGENT_COLORS } from '../lib/presets'
import { API } from '../mygo'
import Modal from './Modal'
import DropZone from './DropZone'

type Props = {
  open: boolean
  onClose: () => void
  onCreate: (name: string, role: string, backend: string, workDir?: string) => void
  /** 已有 agent 数量，用来预填一个合理的默认名，避免用户卡在起名 */
  existingCount?: number
}

const FIELD_STYLE = {
  width: '100%',
  padding: '8px 12px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-sm)',
  fontFamily: 'inherit',
} as const

const LABEL_STYLE = {
  display: 'block',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-secondary)',
  marginBottom: 6,
} as const

const BACKENDS = [
  { id: 'mock', label: '内置', desc: '无需配置' },
  { id: 'claude', label: 'Claude', desc: 'code -p' },
  { id: 'codex', label: 'Codex', desc: 'exec --json' },
] as const

/** suggestName 生成一个不重复的默认名：前端 / 前端 2 / 前端 3 … */
function suggestName(base: string, existingCount: number): string {
  return existingCount === 0 ? base : `${base} ${existingCount + 1}`
}

/**
 * CreateAgentModal —— 新建 agent。
 *
 * 「有 agent 的感觉」靠三件事，而不是再堆几个输入框：
 *   1. 角色是带图标和一句话描述的卡片，不是一排扁平文字
 *   2. 右边实时预览这个 agent 长什么样（头像色 + 名字 + 状态点），
 *      所见即所得，用户能想象出它加入舰队后的样子
 *   3. 创建时有一个入场动效，让「多了一个队友」这件事被感知到
 */
export default function CreateAgentModal({ open, onClose, onCreate, existingCount = 0 }: Props) {
  const [name, setName] = useState('')
  const [role, setRole] = useState(ROLE_PRESETS[0].role)
  const [backend, setBackend] = useState<string>('mock')
  const [workDir, setWorkDir] = useState('')
  const [nameTouched, setNameTouched] = useState(false)
  const [colorIdx, setColorIdx] = useState(0)

  useEffect(() => {
    if (open) {
      const preset = ROLE_PRESETS[0]
      setName(suggestName(preset.name, existingCount))
      setRole(preset.role)
      setBackend('mock')
      setWorkDir('')
      setNameTouched(false)
      setColorIdx(existingCount % AGENT_COLORS.length)
    }
  }, [open, existingCount])

  const activePreset = useMemo(
    () => ROLE_PRESETS.find((r) => r.role === role) ?? ROLE_PRESETS[0],
    [role],
  )

  const pickPreset = (preset: (typeof ROLE_PRESETS)[number], idx: number) => {
    setRole(preset.role)
    setColorIdx(idx % AGENT_COLORS.length)
    if (!nameTouched) setName(suggestName(preset.name, existingCount))
  }

  const canSubmit = name.trim().length > 0

  const submit = () => {
    if (!canSubmit) return
    onCreate(name.trim(), role, backend, workDir.trim() || undefined)
    onClose()
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="新建 Agent"
      width={560}
      footer={
        <>
          <button className="c-btn c-btn--ghost" style={{ flex: 1 }} onClick={onClose}>
            取消
          </button>
          <button
            className="c-btn c-btn--primary"
            style={{ flex: 1 }}
            disabled={!canSubmit}
            onClick={submit}
          >
            创建
          </button>
        </>
      }
    >
      <div style={{ display: 'flex', gap: 16, padding: 18 }}>
        {/* 左：可配置项 */}
        <div style={{ flex: 1, minWidth: 0, display: 'grid', gap: 14 }}>
          <div>
            <label style={LABEL_STYLE}>选一个角色</label>
            <div
              style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6 }}
              role="group"
              aria-label="角色"
            >
              {ROLE_PRESETS.map((r, i) => {
                const active = r.role === role
                return (
                  <button
                    key={r.name}
                    type="button"
                    onClick={() => pickPreset(r, i)}
                    aria-pressed={active}
                    className={`c-role-card ${active ? 'c-role-card--active' : ''}`}
                  >
                    <span className="c-role-card__icon" aria-hidden>
                      {r.emoji}
                    </span>
                    <span style={{ minWidth: 0, flex: 1, textAlign: 'left' }}>
                      <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600 }}>
                        {r.name}
                      </span>
                      <span
                        style={{
                          display: 'block',
                          fontSize: 'var(--text-xs)',
                          color: 'var(--text-tertiary)',
                          marginTop: 1,
                        }}
                      >
                        {r.blurb}
                      </span>
                    </span>
                    {active && <span style={{ color: 'var(--accent-bright)' }}>✓</span>}
                  </button>
                )
              })}
            </div>
          </div>

          <div>
            <label style={LABEL_STYLE}>名称</label>
            <input
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                setNameTouched(true)
              }}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit()
              }}
              placeholder="例如：后端开发"
              style={FIELD_STYLE}
            />
          </div>

          <div>
            <label style={LABEL_STYLE}>工作目录（可选）</label>
            <DropZone onPath={setWorkDir} style={{ padding: '8px 10px' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <span
                  className="mono"
                  style={{
                    flex: 1,
                    minWidth: 0,
                    fontSize: 'var(--text-xs)',
                    color: workDir ? 'var(--text-secondary)' : 'var(--text-disabled)',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    textAlign: 'left',
                  }}
                  title={workDir}
                >
                  {workDir || '拖入文件夹，或'}
                </span>
                <button
                  type="button"
                  className="c-hint-chip"
                  onClick={async (e) => {
                    e.stopPropagation()
                    const picked = await API.pickDirectory()
                    if (picked) setWorkDir(picked)
                  }}
                >
                  浏览…
                </button>
              </div>
            </DropZone>
          </div>

          <div>
            <label style={LABEL_STYLE}>执行后端</label>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 6 }}>
              {BACKENDS.map((b) => (
                <button
                  key={b.id}
                  type="button"
                  onClick={() => setBackend(b.id)}
                  aria-pressed={backend === b.id}
                  className={`c-backend-card ${backend === b.id ? 'c-backend-card--active' : ''}`}
                >
                  <span style={{ display: 'block', fontSize: 'var(--text-xs)', fontWeight: 600 }}>
                    {b.label}
                  </span>
                  <span
                    style={{
                      display: 'block',
                      fontSize: 10,
                      color: 'var(--text-disabled)',
                      marginTop: 1,
                      whiteSpace: 'nowrap',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                    }}
                  >
                    {b.desc}
                  </span>
                </button>
              ))}
            </div>
          </div>
        </div>

        {/* 右：实时预览，让用户先「看到」这个 agent */}
        <div
          style={{
            width: 176,
            flexShrink: 0,
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'center',
            paddingTop: 8,
          }}
        >
          <div style={{ fontSize: 9, letterSpacing: '0.08em', color: 'var(--text-disabled)', marginBottom: 10 }}>
            预览
          </div>

          <div className="c-agent-preview">
            <div
              className="c-agent-preview__halo"
              style={{ background: AGENT_COLORS[colorIdx] }}
            />
            <div
              className="c-agent-preview__avatar"
              style={{ background: AGENT_COLORS[colorIdx] }}
            >
              {(name.trim() || '?').slice(0, 1).toUpperCase()}
            </div>
            <div
              style={{
                fontSize: 'var(--text-sm)',
                fontWeight: 600,
                marginTop: 8,
                maxWidth: '100%',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              {name.trim() || '未命名'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
              {activePreset.name}
            </div>
            <div
              style={{
                marginTop: 8,
                display: 'inline-flex',
                alignItems: 'center',
                gap: 4,
                fontSize: 10,
                color: 'var(--success)',
              }}
            >
              <span className="c-live-dot" /> 就绪
            </div>
          </div>

          <div
            style={{
              marginTop: 12,
              fontSize: 10,
              color: 'var(--text-disabled)',
              textAlign: 'center',
              lineHeight: 1.7,
            }}
          >
            创建后会出现在
            <br />
            舰队的并排监控里
          </div>
        </div>
      </div>
    </Modal>
  )
}
