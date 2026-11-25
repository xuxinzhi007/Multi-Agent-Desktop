import { useEffect, useMemo, useState } from 'react'
import { ROLE_PRESETS } from '../lib/presets'
import { AGENT_COLORS } from '../lib/presets'
import Modal from './Modal'
import DirectoryPicker from './DirectoryPicker'
import type { AcpForm, RuntimeKind } from '../lib/runtime'
import { runtimeBadge, splitLines } from '../lib/runtime'

type Props = {
  open: boolean
  onClose: () => void
  onCreate: (
    name: string,
    role: string,
    backend: string,
    workDir?: string,
    runtime?: RuntimeKind,
    acp?: AcpForm,
  ) => void
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
  const [workDir, setWorkDir] = useState('')
  const [nameTouched, setNameTouched] = useState(false)
  const [colorIdx, setColorIdx] = useState(0)
  // 运行时二选一：native 进程内 loop / acp 外部 Agent（契约 §1）
  const [runtime, setRuntime] = useState<RuntimeKind>('native')
  const [acpName, setAcpName] = useState('')
  const [command, setCommand] = useState('')
  const [argsText, setArgsText] = useState('')
  const [envText, setEnvText] = useState('')

  useEffect(() => {
    if (open) {
      const preset = ROLE_PRESETS[0]
      setName(suggestName(preset.name, existingCount))
      setRole(preset.role)
      setWorkDir('')
      setNameTouched(false)
      setColorIdx(existingCount % AGENT_COLORS.length)
      setRuntime('native')
      setAcpName('')
      setCommand('')
      setArgsText('')
      setEnvText('')
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

  // 文件夹可以后补：先让人和 agent 说上话，比卡在选目录更重要。
  // 没选目录时文件与命令工具会明确报错，会话本身仍然能用。
  // 选了 ACP 则启动命令必填（契约 §4.2：空命令后端也会拒）。
  const acpCommandMissing = runtime === 'acp' && command.trim().length === 0
  const canSubmit = name.trim().length > 0 && !acpCommandMissing

  const submit = () => {
    if (!canSubmit) return
    const acp: AcpForm | undefined =
      runtime === 'acp'
        ? {
            name: acpName.trim(),
            command: command.trim(),
            args: splitLines(argsText),
            env: splitLines(envText),
          }
        : undefined
    onCreate(name.trim(), role, 'loop', workDir.trim(), runtime, acp)
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
            <label style={LABEL_STYLE}>运行时</label>
            <div
              style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6 }}
              role="group"
              aria-label="运行时"
            >
              <button
                type="button"
                onClick={() => setRuntime('native')}
                aria-pressed={runtime === 'native'}
                className={`c-role-card ${runtime === 'native' ? 'c-role-card--active' : ''}`}
              >
                <span className="c-role-card__icon" aria-hidden>
                  🖥
                </span>
                <span style={{ minWidth: 0, flex: 1, textAlign: 'left' }}>
                  <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600 }}>原生</span>
                  <span
                    style={{
                      display: 'block',
                      fontSize: 'var(--text-xs)',
                      color: 'var(--text-tertiary)',
                      marginTop: 1,
                    }}
                  >
                    进程内 loop
                  </span>
                </span>
                {runtime === 'native' && <span style={{ color: 'var(--accent-bright)' }}>✓</span>}
              </button>
              <button
                type="button"
                onClick={() => setRuntime('acp')}
                aria-pressed={runtime === 'acp'}
                className={`c-role-card ${runtime === 'acp' ? 'c-role-card--active' : ''}`}
              >
                <span className="c-role-card__icon" aria-hidden>
                  🔌
                </span>
                <span style={{ minWidth: 0, flex: 1, textAlign: 'left' }}>
                  <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600 }}>外部 ACP</span>
                  <span
                    style={{
                      display: 'block',
                      fontSize: 'var(--text-xs)',
                      color: 'var(--text-tertiary)',
                      marginTop: 1,
                    }}
                  >
                    拉起外部 Agent 子进程
                  </span>
                </span>
                {runtime === 'acp' && <span style={{ color: 'var(--accent-bright)' }}>✓</span>}
              </button>
            </div>
          </div>

          {runtime === 'acp' && (
            <div style={{ display: 'grid', gap: 10 }}>
              <div>
                <label style={LABEL_STYLE}>展示名（可选）</label>
                <input
                  value={acpName}
                  onChange={(e) => setAcpName(e.target.value)}
                  placeholder="例如：grok；留空用命令名"
                  style={FIELD_STYLE}
                />
              </div>
              <div>
                <label style={LABEL_STYLE}>启动命令（必填）</label>
                <input
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  placeholder="例如：grok / codex-acp"
                  aria-invalid={acpCommandMissing}
                  style={{ ...FIELD_STYLE, borderColor: acpCommandMissing ? 'var(--danger)' : undefined }}
                />
                {acpCommandMissing && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)', marginTop: 4 }}>
                    选了外部 ACP Agent，必须填写启动命令
                  </div>
                )}
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                <div>
                  <label style={LABEL_STYLE}>参数（每行一个）</label>
                  <textarea
                    rows={3}
                    value={argsText}
                    onChange={(e) => setArgsText(e.target.value)}
                    placeholder={'acp\n--xxx'}
                    style={{ ...FIELD_STYLE, resize: 'vertical', lineHeight: 1.6 }}
                  />
                </div>
                <div>
                  <label style={LABEL_STYLE}>环境变量（每行一个）</label>
                  <textarea
                    rows={3}
                    value={envText}
                    onChange={(e) => setEnvText(e.target.value)}
                    placeholder={'KEY=VALUE'}
                    style={{ ...FIELD_STYLE, resize: 'vertical', lineHeight: 1.6 }}
                  />
                </div>
              </div>
            </div>
          )}

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
            <label style={LABEL_STYLE}>文件夹</label>
            <DirectoryPicker value={workDir} onChange={setWorkDir} compact />
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
          <div style={{ fontSize: 'var(--text-xs)', letterSpacing: '0.08em', color: 'var(--text-disabled)', marginBottom: 10 }}>
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
                marginTop: 6,
                fontSize: 'var(--text-xs)',
                color: acpCommandMissing ? 'var(--danger)' : 'var(--text-secondary)',
              }}
            >
              {runtimeBadge({ runtime, acp: { name: acpName, command } }).label}
            </div>
            <div
              style={{
                marginTop: 8,
                display: 'inline-flex',
                alignItems: 'center',
                gap: 4,
                fontSize: 'var(--text-xs)',
                color: 'var(--success)',
              }}
            >
              <span className="c-live-dot" /> 就绪
            </div>
          </div>

          <div
            style={{
              marginTop: 12,
              fontSize: 'var(--text-xs)',
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
