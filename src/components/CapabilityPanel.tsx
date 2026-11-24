import { useCallback, useEffect, useState } from 'react'
import { API, type Capability, type SkillView } from '../mygo'

/**
 * CapabilityPanel —— 这个会话此刻到底能干什么。
 *
 * 两件事必须看得见：
 *   1. 工具清单，以及不可用的那些「为什么不可用」（没文件夹 / 没模型 / 浏览器没开）
 *   2. 技能清单：可复用的做事步骤，能新建、能改
 *
 * 之前这些只存在于系统提示里，用户只能靠猜。
 */
export default function CapabilityPanel({ agentId }: { agentId: string | null }) {
  const [caps, setCaps] = useState<Capability[]>([])
  const [skills, setSkills] = useState<SkillView[]>([])
  const [dir, setDir] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  const [body, setBody] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState({ name: '', description: '', body: '' })
  const [err, setErr] = useState('')

  const reload = useCallback(() => {
    API.listSkills().then((s) => setSkills(s ?? [])).catch(() => {})
    API.skillDir().then(setDir).catch(() => {})
    if (agentId) API.agentCapabilities(agentId).then((c) => setCaps(c ?? [])).catch(() => {})
  }, [agentId])

  useEffect(reload, [reload])

  const openSkill = async (id: string) => {
    if (open === id) {
      setOpen(null)
      return
    }
    try {
      const sk = await API.readSkill(id)
      setBody(sk.body ?? '')
      setOpen(id)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }

  const save = async () => {
    setErr('')
    try {
      await API.saveSkill(draft.name, draft.description, draft.body)
      setEditing(false)
      setDraft({ name: '', description: '', body: '' })
      reload()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    }
  }

  const tools = caps.filter((c) => c.kind === '工具')
  const skillCaps = caps.filter((c) => c.kind === '技能')
  const blocked = tools.filter((c) => !c.ready)

  return (
    <div className="c-caps">
      {blocked.length > 0 && (
        <div className="c-caps__warn">
          {blocked.length} 项能力现在用不了：{blocked.map((c) => c.name).join('、')}
        </div>
      )}

      <div className="c-caps__label">工具</div>
      <div className="c-caps__list">
        {tools.map((c) => (
          <div key={c.name} className={`c-caps__row ${c.ready ? '' : 'is-off'}`}>
            <span className="c-caps__mark" aria-hidden>
              {c.ready ? '✓' : '✕'}
            </span>
            <div className="c-caps__main">
              <div className="c-caps__name mono">{c.name}</div>
              <div className="c-caps__detail">{c.ready ? c.detail : c.why || c.detail}</div>
            </div>
          </div>
        ))}
        {tools.length === 0 && <div className="c-caps__empty">选中一个会话后显示它的能力</div>}
      </div>

      <div className="c-caps__label">
        技能
        <span className="c-caps__count">{skillCaps.length}</span>
        <button
          type="button"
          className="c-btn c-btn--ghost c-btn--sm"
          style={{ marginLeft: 'auto' }}
          onClick={() => setEditing((v) => !v)}
        >
          {editing ? '取消' : '新建'}
        </button>
      </div>

      {editing && (
        <div className="c-caps__form">
          <input
            aria-label="技能名"
            placeholder="技能名，例如：排查生产慢SQL"
            value={draft.name}
            onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          />
          <input
            aria-label="什么时候用"
            placeholder="一句话说明什么时候用它"
            value={draft.description}
            onChange={(e) => setDraft({ ...draft, description: e.target.value })}
          />
          <textarea
            aria-label="步骤正文"
            placeholder="步骤正文。写清顺序、判据和坑，别写空话。"
            rows={6}
            value={draft.body}
            onChange={(e) => setDraft({ ...draft, body: e.target.value })}
          />
          <button
            type="button"
            className="c-btn c-btn--primary c-btn--sm"
            disabled={!draft.name.trim() || !draft.body.trim()}
            onClick={save}
          >
            保存技能
          </button>
        </div>
      )}

      <div className="c-caps__list">
        {skills.map((sk) => (
          <div key={sk.id}>
            <button type="button" className="c-caps__skill" onClick={() => openSkill(sk.id)}>
              <span className="c-caps__caret" aria-hidden>
                {open === sk.id ? '▾' : '▸'}
              </span>
              <span className="c-caps__skillname">{sk.name}</span>
              <span className="c-caps__skilldesc">{sk.description}</span>
            </button>
            {open === sk.id && <pre className="c-caps__body">{body}</pre>}
          </div>
        ))}
        {skills.length === 0 && !editing && (
          <div className="c-caps__empty">
            还没有技能。技能是「怎么做某类事」的步骤，跨项目复用；
            会话走通一套流程后也能用 save_skill 自己沉淀。
          </div>
        )}
      </div>

      {err && <div className="c-caps__err">{err}</div>}
      {dir && (
        <div className="c-caps__dir mono" title={dir}>
          文件在 {dir}
        </div>
      )}
    </div>
  )
}