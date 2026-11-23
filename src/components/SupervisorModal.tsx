import { useEffect, useState } from 'react'
import Modal from './Modal'
import DirectoryPicker from './DirectoryPicker'

type Props = {
  open: boolean
  onClose: () => void
  onCreate: (req: { name: string; role: string; goal: string; workDir: string }) => Promise<void>
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

/**
 * SupervisorModal —— 一句话拆任务。
 *
 * 只收三个真需要的东西：目标、文件夹、名字。
 * 角色和拆分方式交给模型，用户不需要先想好怎么拆。
 */
export default function SupervisorModal({ open, onClose, onCreate }: Props) {
  const [name, setName] = useState('')
  const [goal, setGoal] = useState('')
  const [workDir, setWorkDir] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (open) {
      setName('Supervisor')
      setGoal('')
      setWorkDir('')
      setBusy(false)
      setErr('')
    }
  }, [open])

  const canSubmit = goal.trim().length > 0 && workDir.trim().length > 0 && !busy

  const submit = async () => {
    if (!canSubmit) return
    setBusy(true)
    setErr('')
    try {
      await onCreate({
        name: name.trim() || 'Supervisor',
        role: '任务编排者：拆分子任务、并行派发、汇总结果',
        goal: goal.trim(),
        workDir: workDir.trim(),
      })
      onClose()
    } catch (e) {
      setErr(e instanceof Error && e.message ? e.message : '创建失败，请检查文件夹是不是 git 仓库')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="自动拆任务"
      width={560}
      footer={
        <>
          <button className="c-btn c-btn--ghost" style={{ flex: 1 }} onClick={onClose} disabled={busy}>
            取消
          </button>
          <button className="c-btn c-btn--primary" style={{ flex: 1 }} disabled={!canSubmit} onClick={() => void submit()}>
            {busy ? '正在派活…' : '拆开并开始'}
          </button>
        </>
      }
    >
      <div style={{ display: 'grid', gap: 14, padding: 18 }}>
        <p className="c-settings__prose" style={{ margin: 0 }}>
          写一句目标，模型会拆成 2-4 个子任务，每个子任务独立会话、独立分支并行做，最后收成一条汇总。
          文件夹需要是 git 仓库，子会话才能在各自的工作树里改代码。
        </p>

        <div>
          <label style={LABEL_STYLE}>目标</label>
          <textarea
            value={goal}
            onChange={(e) => setGoal(e.target.value)}
            placeholder="例如：给设置页加上模型供应商的分组搜索，并补一组测试"
            rows={4}
            style={{ ...FIELD_STYLE, resize: 'vertical', lineHeight: 1.6 }}
          />
        </div>

        <div>
          <label style={LABEL_STYLE}>文件夹</label>
          <DirectoryPicker value={workDir} onChange={setWorkDir} compact />
        </div>

        <div>
          <label style={LABEL_STYLE}>会话名</label>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Supervisor" style={FIELD_STYLE} />
        </div>

        {err && (
          <p className="c-settings__note is-bad" role="alert">
            {err}
          </p>
        )}
      </div>
    </Modal>
  )
}
