import { useEffect, useState } from 'react'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
import { API, type AgentView, type FeishuScopeView, type FeishuView, type ProviderEndpoint, type KeepAwakeState } from '../mygo'
import type { LLMConfig } from '../lib/useBackend'
import DiagnosePanel from './DiagnosePanel'

type Props = {
  open: boolean
  llm: LLMConfig
  /** 运行版本号，显示在「关于」页 */
  version?: string
  onClose: () => void
  onSave: (cfg: LLMConfig) => void
}

const VENDORS: { id: string; label: string; local: boolean; baseUrl: string; login?: boolean }[] = [
  { id: 'llamacpp', label: '本地 llama.cpp', local: true, baseUrl: 'http://127.0.0.1:8080' },
  { id: 'ollama', label: '本地 Ollama', local: true, baseUrl: 'http://127.0.0.1:11434' },
  { id: 'qoder', label: 'Qoder', local: false, baseUrl: 'https://api3.qoder.sh', login: true },
  { id: 'doubao-cli', label: '豆包 CLI（本机）', local: true, baseUrl: '' },
  { id: 'doubao', label: '豆包（方舟 API）', local: false, baseUrl: 'https://ark.cn-beijing.volces.com/api/v3' },
  { id: 'deepseek', label: 'DeepSeek', local: false, baseUrl: 'https://api.deepseek.com' },
  { id: 'openai', label: 'OpenAI', local: false, baseUrl: 'https://api.openai.com' },
  { id: 'claude', label: 'Claude', local: false, baseUrl: '' },
]

function errText(e: unknown): string {
  if (e instanceof Error && e.message) return e.message
  if (typeof e === 'string' && e) return e
  if (e && typeof e === 'object' && 'message' in e) {
    const m = (e as { message?: unknown }).message
    if (typeof m === 'string' && m) return m
  }
  return '连接失败'
}

function vendorLabel(id: string): string {
  return VENDORS.find((v) => v.id === id)?.label || id
}

function savedText(ep: ProviderEndpoint, verb: '保存' | '登录'): { err: string; note: string } {
  const name = vendorLabel(ep.provider)
  if (!ep.ok) return { err: ep.reason || `${name}没有生效`, note: '' }
  const quota = ep.quota ? `${ep.quota}。` : ''
  if (ep.provider === 'doubao-cli') {
    return { err: '', note: `已保存。${name} 可以用。${quota}会话里选中它的模型即可，不用再填 Key。` }
  }
  const how = verb === '登录' ? '这次授权' : '这把 Key'
  return { err: '', note: `已${verb}。${name} 现在用${how}。${quota}之后的对话会用它。` }
}

function ProviderBoard() {
  const [rows, setRows] = useState<ProviderEndpoint[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [formErr, setFormErr] = useState('')
  const [formNote, setFormNote] = useState('')

  const load = async () => {
    setLoading(true)
    try {
      setRows(await API.listProviderEndpoints())
    } catch (e) {
      setFormErr(errText(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const startEdit = (id: string) => {
    const row = rows.find((r) => r.provider === id)
    const preset = VENDORS.find((v) => v.id === id)
    setEditing(id)
    setBaseUrl(row?.baseUrl || preset?.baseUrl || '')
    setApiKey('')
    setFormErr('')
    setFormNote('')
  }

  const applyEndpoint = (ep: ProviderEndpoint, verb: '保存' | '登录') => {
    setRows((cur) => {
      const next = cur.some((r) => r.provider === ep.provider)
        ? cur.map((r) => (r.provider === ep.provider ? ep : r))
        : [...cur, ep]
      return next
    })
    const msg = savedText(ep, verb)
    setFormErr(msg.err)
    setFormNote(msg.note)
    if (!msg.err) setApiKey('')
  }

  const loginQoder = async () => {
    setBusy(true)
    setFormErr('')
    setFormNote('')
    try {
      applyEndpoint(await API.loginQoder(), '登录')
    } catch (e) {
      setFormErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    if (!editing) return
    setBusy(true)
    setFormErr('')
    setFormNote('')
    try {
      applyEndpoint(
        await API.saveProvider({
          provider: editing,
          baseUrl,
          apiKey,
          model: '',
        }),
        '保存',
      )
    } catch (e) {
      setFormErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const saved = rows.filter((r) => r.saved)
  const current = VENDORS.find((v) => v.id === editing)
  const editingRow = rows.find((r) => r.provider === editing)

  return (
    <>
      <section className="c-settings__card">
        <div className="c-settings__card-head">
          <div>
            <div className="c-settings__card-title">已保存</div>
            <div className="c-settings__card-sub">生效表示现在的对话会用这把 Key。Qoder 还会写出剩余额度</div>
          </div>
        </div>
        <div className="c-settings__card-body">
          {loading && <p className="c-settings__prose">正在检查已保存的服务…</p>}
          {!loading && saved.length === 0 && <p className="c-settings__prose">还没有保存的服务。</p>}
          <div role="list" aria-label="已保存的服务" className="c-providers">
            {saved.map((row) => (
              <div key={row.provider} role="listitem" className="c-providers__row">
                <div className="c-providers__main">
                  <span className="c-providers__name">{vendorLabel(row.provider)}</span>
                  <span className="c-providers__url">{row.baseUrl}</span>
                  <span className={`c-providers__badge ${row.ok ? 'is-ok' : 'is-bad'}`}>
                    {row.ok ? '生效' : '未生效'}
                  </span>
                </div>
                {row.quota && <p className="c-settings__note is-ok">{row.quota}</p>}
                {row.ok && row.models && row.models.length > 0 && (
                  <ul className="c-providers__models" aria-label={`${vendorLabel(row.provider)} 的模型`}>
                    {row.models.map((m) => (
                      <li key={m}>{m}</li>
                    ))}
                  </ul>
                )}
                {!row.ok && row.reason && <p className="c-settings__note is-bad">{row.reason}</p>}
              </div>
            ))}
          </div>
        </div>
      </section>

      <section className="c-settings__card">
        <div className="c-settings__card-head">
          <div>
            <div className="c-settings__card-title">服务厂商</div>
            <div className="c-settings__card-sub">选一个厂商，填地址和 Key，保存后立刻检查</div>
          </div>
        </div>
        <div className="c-settings__card-body">
          <div role="list" aria-label="服务厂商" className="c-providers">
            {VENDORS.map((v) => {
              const row = rows.find((r) => r.provider === v.id)
              const on = editing === v.id
              return (
                <div key={v.id} role="listitem" className={`c-providers__row ${on ? 'is-on' : ''}`}>
                  <button type="button" className="c-providers__pick" onClick={() => startEdit(v.id)}>
                    <span className="c-providers__name">{v.label}</span>
                    <span className="c-providers__url">
                      {row?.saved ? '已保存' : '未保存'}
                      {row?.ok ? ' · 生效' : ''}
                      {row?.quota ? ` · ${row.quota}` : ''}
                    </span>
                  </button>
                </div>
              )
            })}
          </div>

          {editing && (
            <>
              {editing === 'doubao-cli' ? (
                <div className="c-settings__field">
                  <p className="c-settings__hint">
                    这是本机命令，复用你已经登录的豆包账号，不需要地址和 Key。模型列表直接读 `doubao model list`；
                    对话时用 `doubao -p` 在会话工作目录里执行。
                  </p>
                </div>
              ) : (
                <>
                  <label className="c-settings__field">
                    <span className="c-settings__field-label">服务地址</span>
                <input
                  className="c-settings__input"
                  aria-label="服务地址"
                  value={baseUrl}
                  placeholder={current?.baseUrl || 'https://'}
                  onChange={(e) => {
                    setBaseUrl(e.target.value)
                    setFormNote('')
                  }}
                />
              </label>
              <label className="c-settings__field">
                <span className="c-settings__field-label">API Key</span>
                <span className="c-settings__input-row">
                  <input
                    className="c-settings__input"
                    aria-label="API Key"
                    type={showKey ? 'text' : 'password'}
                    autoComplete="off"
                    spellCheck={false}
                    value={apiKey}
                    placeholder={
                      current?.local
                        ? '本地服务可以留空'
                        : editing === 'qoder'
                          ? editingRow?.hasKey
                            ? '已登录，留空则不修改'
                            : 'pt-…'
                          : editingRow?.hasKey
                            ? '已保存，留空则不修改'
                            : 'sk-…'
                    }
                    onChange={(e) => {
                      setApiKey(e.target.value)
                      setFormNote('')
                    }}
                  />
                  <button type="button" className="c-btn c-btn--ghost" onClick={() => setShowKey((v) => !v)}>
                    {showKey ? '隐藏' : '显示'}
                  </button>
                </span>
              </label>
                </>
              )}
              {editing === 'qoder' && (
                <div className="c-settings__field">
                  <p className="c-settings__hint">
                    可以在浏览器里授权，也可以填写 Personal Access Token（pt- 开头）或访问令牌。
                  </p>
                  <button type="button" className="c-btn" disabled={busy} onClick={() => void loginQoder()}>
                    {busy ? '正在等待浏览器授权…' : '授权登录'}
                  </button>
                </div>
              )}
              <div className="c-settings__field">
                <button type="button" className="c-btn c-btn--primary" disabled={busy} onClick={() => void save()}>
                  {busy ? '正在检查…' : '保存并检查'}
                </button>
                {formNote && (
                  <p className="c-settings__note is-ok" role="status">
                    {formNote}
                  </p>
                )}
                {formErr && (
                  <p className="c-settings__note is-bad" role="alert">
                    {formErr}
                  </p>
                )}
              </div>
            </>
          )}
        </div>
      </section>
    </>
  )
}

const PERMS: { kind: string; title: string; detail: string; grant: string }[] = [
  {
    kind: 'screen',
    title: '屏幕录制',
    detail: '协助要看当前屏幕上的字，才能给出和这一屏有关的建议。',
    grant: '系统设置 → 隐私与安全性 → 屏幕录制',
  },
  {
    kind: 'accessibility',
    title: '辅助功能',
    detail: '要操作 IDE 或其他应用的界面，需要这项。现在还没接上，授权先记在这里。',
    grant: '系统设置 → 隐私与安全性 → 辅助功能',
  },
  {
    kind: 'automation',
    title: '自动化',
    detail: '读取前台是哪个应用时会用到。网页的打开、读取和点击只在应用自己的浏览器窗口里。',
    grant: '系统设置 → 隐私与安全性 → 自动化',
  },
]

function AccessBoard() {
  const [err, setErr] = useState('')
  const [grants, setGrants] = useState<Record<string, string>>({})
  useEffect(() => {
    API.privacyStatus()
      .then((rows) => {
        const next: Record<string, string> = {}
        for (const row of rows) next[row.kind] = row.state
        setGrants(next)
      })
      .catch((e: unknown) => setErr(errText(e)))
  }, [])
  const open = (kind: string) => {
    setErr('')
    API.openPrivacy(kind).catch((e: unknown) => setErr(errText(e)))
  }
  const mark = (state: string | undefined) => {
    if (state === 'yes') return '已授权'
    if (state === 'no') return '未授权'
    return '未能读取'
  }
  return (
    <>
      <section className="c-settings__card">
        <div className="c-settings__card-head">
          <div>
            <div className="c-settings__card-title">系统权限</div>
            <div className="c-settings__card-sub">已开通的标成已授权。未授权时再去系统设置打开。</div>
          </div>
        </div>
        <div className="c-settings__card-body">
          <div className="c-providers" role="list">
            {PERMS.map((item) => {
              const state = grants[item.kind]
              const on = state === 'yes'
              return (
                <div key={item.kind} className="c-providers__row" role="listitem">
                  <div className="c-providers__main">
                    <span className="c-providers__name">{item.title}</span>
                    <span className={`c-providers__badge ${on ? 'is-ok' : 'is-bad'}`}>{mark(state)}</span>
                    {!on && (
                      <button type="button" className="c-btn c-btn--ghost c-btn--sm" onClick={() => open(item.kind)}>
                        去授权
                      </button>
                    )}
                  </div>
                  <p className="c-settings__hint">{item.detail}</p>
                  <p className="c-settings__hint">{item.grant}</p>
                </div>
              )
            })}
          </div>
          {err && (
            <p className="c-settings__hint" role="status">
              {err}
            </p>
          )}
        </div>
      </section>
      <section className="c-settings__card">
        <div className="c-settings__card-head">
          <div>
            <div className="c-settings__card-title">这些不是权限</div>
            <div className="c-settings__card-sub">系统设置里没有对应开关</div>
          </div>
        </div>
        <div className="c-settings__card-body">
          <p className="c-settings__prose">
            读写代码、跑测试、问其他会话、写笔记、看图片、删除会话，要等产品自己接上。选好这个会话的文件夹和模型之后，读文件和跑命令才会落在那个目录里。
          </p>
        </div>
      </section>
    </>
  )
}

type FeishuDraft = {
  id: string
  name: string
  enabled: boolean
  joinAll: boolean
  appId: string
  secret: string
  agentId: string
  /** 审批卡会话 owner 的 open_id，只读，来自飞书身份授权；不在草稿里编辑。 */
  ownerOpenId?: string
  /** 审批白名单 open_id，界面上是「每行一个」的文本，保存时解析成数组。 */
  approverText?: string
  dirty: boolean
}

function botLabel(name: string, appId: string): string {
  const n = name.trim()
  if (n) return n
  if (appId.trim()) return appId.trim()
  return '未命名'
}

function sessionLabel(agentId: string, agents: AgentView[]): string {
  if (!agentId) return '未选会话'
  const found = agents.find((a) => a.id === agentId)
  return found ? found.name : '会话不存在'
}

function statusText(state: string | undefined): string {
  if (state === 'connected') return '已连接'
  if (state === 'connecting') return '连接中'
  if (state === 'error') return '连接失败'
  return '未连接'
}

function draftFromBot(bot: FeishuView): FeishuDraft {
  return {
    id: bot.id,
    name: bot.name,
    enabled: bot.enabled,
    joinAll: !bot.mentionOnly,
    appId: bot.appId,
    secret: '',
    agentId: bot.agentId,
    ownerOpenId: bot.ownerOpenId ?? '',
    approverText: (bot.approverOpenIds ?? []).join('\n'),
    dirty: false,
  }
}

function FeishuBoard() {
  const [bots, setBots] = useState<FeishuView[]>([])
  const [pending, setPending] = useState<FeishuDraft[]>([])
  const [drafts, setDrafts] = useState<Record<string, FeishuDraft>>({})
  const [agents, setAgents] = useState<AgentView[]>([])
  const [selected, setSelected] = useState('')
  const [showSecret, setShowSecret] = useState(false)
  const [editing, setEditing] = useState<{ id: string; field: 'name' | 'agent' } | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [scopes, setScopes] = useState<Record<string, FeishuScopeView>>({})
  // 设备授权是两段式的：先拿验证码给用户看，他确认后再取令牌。
  const [auth, setAuth] = useState<{ id: string; url: string; code: string } | null>(null)
  // 机器人不在的群，用 chat_id 主动读一次。
  const [chatID, setChatID] = useState('')
  const [chatBusy, setChatBusy] = useState(false)
  const [chatNote, setChatNote] = useState('')

  const loadScopes = (list: FeishuView[]) => {
    for (const bot of list) {
      if (!bot.hasSecret || !bot.appId) continue
      API.checkFeishuScopes(bot.id)
        .then((view) => setScopes((cur) => ({ ...cur, [bot.id]: view })))
        .catch(() => {})
    }
  }

  const load = async () => {
    const [next, list] = await Promise.all([API.listFeishu(), API.listAgents()])
    const bots = next ?? []
    setBots(bots)
    setAgents(list)
    loadScopes(bots)
  }

  useEffect(() => {
    void load().catch((e: unknown) => setErr(errText(e)))
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      API.listFeishu()
        .then((next) => setBots(next ?? []))
        .catch(() => {})
    }, 2000)
    return () => window.clearInterval(timer)
  }, [])

  const saved = bots.find((b) => b.id === selected)
  const fresh = pending.find((b) => b.id === selected)
  const active = saved || fresh ? selected : ''
  const base = saved ? draftFromBot(saved) : fresh
  const draft = active && drafts[active] ? drafts[active] : base

  const patchDraft = (id: string, patch: Partial<FeishuDraft>) => {
    setDrafts((cur) => {
      const bot = bots.find((b) => b.id === id)
      const prev = cur[id] ?? (bot ? draftFromBot(bot) : pending.find((b) => b.id === id))
      if (!prev) return cur
      return { ...cur, [id]: { ...prev, ...patch, dirty: true } }
    })
  }

  const beginEdit = (id: string, field: 'name' | 'agent') => {
    setSelected(id)
    setErr('')
    setEditing({ id, field })
  }

  const putBot = (view: FeishuView) => {
    setBots((cur) => {
      const has = cur.some((b) => b.id === view.id)
      return has ? cur.map((b) => (b.id === view.id ? view : b)) : [...cur, view]
    })
  }

  const add = () => {
    const id = `new-${Date.now()}`
    const row: FeishuDraft = {
      id,
      name: '',
      enabled: false,
      joinAll: true,
      appId: '',
      secret: '',
      agentId: '',
      ownerOpenId: '',
      approverText: '',
      dirty: true,
    }
    setPending((cur) => [...cur, row])
    setDrafts((cur) => ({ ...cur, [id]: row }))
    setSelected(id)
    setErr('')
    setShowSecret(false)
  }

  const toggleEnabled = (id: string, enabled: boolean) => {
    if (id.startsWith('new-')) {
      patchDraft(id, { enabled })
      return
    }
    setErr('')
    API.setFeishuEnabled(id, enabled)
      .then((view) => {
        putBot(view)
        setDrafts((cur) => {
          const d = cur[id]
          if (!d) return cur
          return { ...cur, [id]: { ...d, enabled: view.enabled } }
        })
      })
      .catch((e: unknown) => setErr(errText(e)))
  }

  const remove = (id: string) => {
    if (!id.startsWith('new-') && !window.confirm('删掉这个机器人？它的长连接会停掉。')) return
    if (id.startsWith('new-')) {
      setPending((cur) => cur.filter((b) => b.id !== id))
      setDrafts((cur) => {
        const next = { ...cur }
        delete next[id]
        return next
      })
      if (selected === id) setSelected('')
      return
    }
    setBusy(true)
    setErr('')
    API.deleteFeishu(id)
      .then(() => {
        setBots((cur) => cur.filter((b) => b.id !== id))
        setDrafts((cur) => {
          const next = { ...cur }
          delete next[id]
          return next
        })
        if (selected === id) setSelected('')
      })
      .catch((e: unknown) => setErr(errText(e)))
      .finally(() => setBusy(false))
  }

  const saveBot = async (id: string) => {
    const source = drafts[id] ?? pending.find((b) => b.id === id)
    if (!source) return
    setSelected(id)
    setBusy(true)
    setErr('')
    // 白名单：每行一个 open_id，去空行、去重；留空则不传，沿用已保存的值（契约 FeishuSave）。
    const approvers = Array.from(
      new Set(
        (source.approverText ?? '')
          .split('\n')
          .map((s) => s.trim())
          .filter(Boolean),
      ),
    )
    try {
      const view = await API.saveFeishu({
        id: source.id.startsWith('new-') ? '' : source.id,
        name: source.name,
        enabled: source.enabled,
        appId: source.appId,
        appSecret: source.secret,
        agentId: source.agentId,
        mentionOnly: !source.joinAll,
        ownerOpenId: source.ownerOpenId || undefined,
        approverOpenIds: approvers.length > 0 ? approvers : undefined,
      })
      putBot(view)
      setPending((cur) => cur.filter((b) => b.id !== source.id))
      setDrafts((cur) => {
        const next = { ...cur }
        delete next[source.id]
        return next
      })
      setSelected(view.id)
      setShowSecret(false)
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const rows = [
    ...bots.map((b) => ({
      id: b.id,
      name: drafts[b.id]?.name ?? b.name,
      appId: b.appId,
      agentId: drafts[b.id]?.agentId ?? b.agentId,
      enabled: drafts[b.id]?.enabled ?? b.enabled,
      status: b.status,
      hasSecret: b.hasSecret,
      hasUser: b.hasUser,
      userName: b.userName,
      userExpire: b.userExpire,
      userRead: b.userRead,
      saved: true,
    })),
    ...pending.map((b) => ({
      id: b.id,
      name: drafts[b.id]?.name ?? b.name,
      appId: drafts[b.id]?.appId ?? b.appId,
      agentId: drafts[b.id]?.agentId ?? b.agentId,
      enabled: drafts[b.id]?.enabled ?? b.enabled,
      status: 'stopped',
      hasSecret: false,
      hasUser: false,
      userName: undefined,
      userExpire: undefined,
      userRead: undefined,
      saved: false,
    })),
  ]

  return (
    <section className="c-settings__card">
      <div className="c-settings__card-head">
        <div>
          <div className="c-settings__card-title">飞书机器人</div>
          <div className="c-settings__card-sub">每个机器人单独保存</div>
        </div>
        <button type="button" className="c-btn c-btn--ghost c-feishu__add" onClick={add}>
          添加
        </button>
      </div>
      <div className="c-settings__card-body">
        {rows.length === 0 && <p className="c-settings__prose">还没有机器人</p>}
        <div role="list" aria-label="飞书机器人" className="c-providers">
          {rows.map((row) => {
            const on = row.id === active
            const label = botLabel(row.name, row.appId)
            const state = row.status
            const badge = state === 'connected' ? 'is-ok' : state === 'error' ? 'is-bad' : ''
            return (
              <div key={row.id} role="listitem" className={`c-providers__row c-feishu__row ${on ? 'is-on' : ''}`}>
                <div className="c-feishu__identity">
                  {editing?.id === row.id && editing.field === 'name' ? (
                    <input
                      className="c-settings__input c-feishu__inline"
                      aria-label="机器人名字"
                      value={drafts[row.id]?.name ?? row.name}
                      autoFocus
                      onChange={(e) => patchDraft(row.id, { name: e.target.value })}
                      onBlur={() => setEditing((cur) => (cur?.id === row.id && cur.field === 'name' ? null : cur))}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === 'Escape') e.currentTarget.blur()
                      }}
                    />
                  ) : (
                    <button
                      type="button"
                      className="c-feishu__edit"
                      onMouseDown={(e) => {
                        e.preventDefault()
                        beginEdit(row.id, 'name')
                      }}
                      onClick={() => beginEdit(row.id, 'name')}
                    >
                      {label}
                    </button>
                  )}
                  {editing?.id === row.id && editing.field === 'agent' ? (
                    <select
                      className="c-settings__input c-feishu__inline"
                      aria-label="默认会话"
                      value={row.agentId}
                      autoFocus
                      onChange={(e) => {
                        patchDraft(row.id, { agentId: e.target.value })
                        setEditing(null)
                      }}
                      onKeyDown={(e) => {
                        if (e.key === 'Escape') setEditing(null)
                      }}
                      onBlur={() => {
                        window.setTimeout(() => {
                          setEditing((cur) => (cur?.id === row.id && cur.field === 'agent' ? null : cur))
                        }, 180)
                      }}
                    >
                      <option value="">选择一个会话</option>
                      {agents.map((a) => (
                        <option key={a.id} value={a.id}>
                          {a.name}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <button
                      type="button"
                      className="c-feishu__edit c-feishu__edit--muted"
                      onMouseDown={(e) => {
                        e.preventDefault()
                        beginEdit(row.id, 'agent')
                      }}
                      onClick={() => beginEdit(row.id, 'agent')}
                    >
                      {sessionLabel(row.agentId, agents)}
                    </button>
                  )}
                </div>
                {row.saved && (
                  <button
                    type="button"
                    className="c-btn c-btn--ghost c-feishu__auth"
                    disabled={busy || !row.hasSecret || !row.appId}
                    onClick={() => {
                      setSelected(row.id)
                      setErr('')
                      setAuth(null)
                      setBusy(true)
                      API.beginFeishuUserAuth(row.id)
                        .then((v) => setAuth({ id: row.id, url: v.url, code: v.code }))
                        .catch((e: unknown) => setErr(errText(e)))
                        .finally(() => setBusy(false))
                    }}
                  >
                    {row.hasUser ? '已授权，重新授权' : '用我的飞书身份授权'}
                  </button>
                )}
                {auth?.id === row.id && (
                  <div className="c-settings__note" role="status">
                    <p>
                      浏览器已经打开确认页。确认验证码 <b>{auth.code}</b>，然后点右边的按钮。
                    </p>
                    <p style={{ wordBreak: 'break-all', color: 'var(--text-tertiary)' }}>{auth.url}</p>
                    <button
                      type="button"
                      className="c-btn c-btn--ghost"
                      disabled={busy}
                      onClick={() => {
                        setErr('')
                        setBusy(true)
                        API.finishFeishuUserAuth(row.id)
                          .then((view) => {
                            putBot(view)
                            setAuth(null)
                          })
                          .catch((e: unknown) => setErr(errText(e)))
                          .finally(() => setBusy(false))
                      }}
                    >
                      {busy ? '正在等授权结果…' : '我已同意，取回身份'}
                    </button>
                  </div>
                )}
                {row.saved && row.hasUser && (
                  <span className="c-feishu__user" role="status">
                    用户身份
                    {row.userName ? `：${row.userName}` : ''}
                    {row.userExpire
                      ? `（${new Date(row.userExpire * 1000).toLocaleString()} 到期，过期会自动续）`
                      : ''}
                  </span>
                )}
                {row.userRead && (
                  <p
                    className={
                      row.userRead.includes('失败') || row.userRead.includes('缺权限')
                        ? 'c-settings__note is-bad'
                        : 'c-settings__note is-ok'
                    }
                    role="status"
                  >
                    {row.userRead}
                  </p>
                )}
                <span className={`c-providers__badge ${badge}`}>{statusText(state)}</span>
                <input
                  type="checkbox"
                  aria-label={`${label} 启用`}
                  checked={row.enabled}
                  onChange={(e) => toggleEnabled(row.id, e.target.checked)}
                />
                <button type="button" className="c-btn c-btn--ghost" onClick={() => remove(row.id)}>
                  删除
                </button>
                {(drafts[row.id]?.dirty || !row.saved) && (
                  <button
                    type="button"
                    className="c-btn c-btn--primary c-feishu__save"
                    disabled={busy}
                    onClick={() => void saveBot(row.id)}
                  >
                    {busy && row.id === active ? '正在保存…' : '保存'}
                  </button>
                )}
                {!drafts[row.id]?.dirty && row.saved && (state === 'connected' || !row.enabled) && (
                  <span className="c-feishu__saved">已保存</span>
                )}
              </div>
            )
          })}
        </div>
        {bots.some((b) => b.hasUser) && (
          <div className="c-settings__card-body" style={{ display: 'grid', gap: 8 }}>
            <div className="c-settings__card-sub">
              按 chat_id 手动读一个群。默认只同步机器人在的群，机器人不在的群不会自动读；
              这里读一次算显式放行，之后只补历史、不自动回答。
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <input
                className="c-settings__input"
                aria-label="群 chat_id"
                placeholder="oc_..."
                value={chatID}
                onChange={(e) => setChatID(e.target.value)}
              />
              <button
                type="button"
                className="c-btn c-btn--ghost"
                disabled={chatBusy || !chatID.trim()}
                onClick={() => {
                  const id = chatID.trim()
                  setChatBusy(true)
                  setChatNote('')
                  API.syncFeishuChat(id)
                    .then((n) => {
                      setChatNote(
                        n > 0
                          ? `读到 ${n} 条新消息，会话列表里已经加上了（只读，不会自动回答）。`
                          : '没有新消息（这个群已放行，只读同步）。',
                      )
                      setChatID('')
                    })
                    .catch((e: unknown) => setChatNote(`读取失败：${errText(e)}`))
                    .finally(() => setChatBusy(false))
                }}
              >
                {chatBusy ? '读取中…' : '读取'}
              </button>
            </div>
            {chatNote && <span className="c-settings__note">{chatNote}</span>}
          </div>
        )}
        {bots.map((bot) => {
          const view = scopes[bot.id]
          if (!view) return null
          const need = (view.items ?? []).filter((it) => !it.ok && !it.optional)
          const opt = (view.items ?? []).filter((it) => !it.ok && it.optional)
          const has = (view.items ?? []).filter((it) => it.ok)
          return (
            <div key={bot.id} className="c-scope" role="status">
              <div className="c-scope__head">
                <b>{botLabel(bot.name, bot.appId)}</b>
                <span className={view.ok ? 'c-scope__badge is-ok' : 'c-scope__badge is-bad'}>
                  {view.ok ? '必需权限齐全' : `缺 ${need.length} 项必需权限`}
                </span>
                <button
                  type="button"
                  className="c-btn c-btn--ghost c-scope__again"
                  disabled={busy}
                  onClick={() => {
                    setBusy(true)
                    setErr('')
                    API.checkFeishuScopes(bot.id)
                      .then((next) => setScopes((cur) => ({ ...cur, [bot.id]: next })))
                      .catch((e: unknown) => setErr(errText(e)))
                      .finally(() => setBusy(false))
                  }}
                >
                  重新检查
                </button>
                {view.url && !view.ok && (
                  <button
                    type="button"
                    className="c-btn c-btn--primary c-scope__open"
                    disabled={busy}
                    onClick={() => {
                      setBusy(true)
                      setErr('')
                      API.openFeishuScopes(bot.id)
                        .then((next) => setScopes((cur) => ({ ...cur, [bot.id]: next })))
                        .catch((e: unknown) => setErr(errText(e)))
                        .finally(() => setBusy(false))
                    }}
                  >
                    开通缺少的权限
                  </button>
                )}
              </div>
              {need.length > 0 && (
                <ul className="c-scope__list">
                  {need.map((it) => (
                    <li key={it.ask} className="c-scope__item is-bad">
                      <span className="c-scope__mark">缺</span>
                      <span className="c-scope__name">{it.name}</span>
                      <code className="c-scope__ask">{it.ask}</code>
                      {it.why && <span className="c-scope__why">{it.why}</span>}
                    </li>
                  ))}
                </ul>
              )}
              {opt.length > 0 && (
                <ul className="c-scope__list">
                  {opt.map((it) => (
                    <li key={it.ask} className="c-scope__item is-opt">
                      <span className="c-scope__mark">可选</span>
                      <span className="c-scope__name">{it.name}</span>
                      <code className="c-scope__ask">{it.ask}</code>
                      {it.why && <span className="c-scope__why">{it.why}</span>}
                    </li>
                  ))}
                </ul>
              )}
              {has.length > 0 && (
                <details className="c-scope__have">
                  <summary>已有 {has.length} 项能力</summary>
                  <ul className="c-scope__list">
                    {has.map((it) => (
                      <li key={it.ask} className="c-scope__item is-ok">
                        <span className="c-scope__mark">有</span>
                        <span className="c-scope__name">{it.name}</span>
                        <code className="c-scope__ask">{it.ask}</code>
                      </li>
                    ))}
                  </ul>
                </details>
              )}
              {view.granted && view.granted.length > 0 && (
                <details className="c-scope__have">
                  <summary>租户已授予的 {view.granted.length} 个 scope</summary>
                  <p className="c-scope__raw">{view.granted.join('  ')}</p>
                </details>
              )}
            </div>
          )
        })}
        {draft && (
          <>
            <div className="c-feishu__switches">
              <label className="c-feishu__switch">
                <input
                  type="checkbox"
                  aria-label="启用飞书机器人"
                  checked={draft.enabled}
                  onChange={(e) => toggleEnabled(draft.id, e.target.checked)}
                />
                启用
              </label>
              <label className="c-feishu__switch">
                <input
                  type="checkbox"
                  aria-label="群里自己接话"
                  checked={draft.joinAll}
                  onChange={(e) => patchDraft(draft.id, { joinAll: e.target.checked })}
                />
                群里自己接话
              </label>
            </div>
            <label className="c-settings__field">
              <span className="c-settings__field-label">App ID</span>
              <input
                className="c-settings__input"
                aria-label="飞书 App ID"
                value={draft.appId}
                spellCheck={false}
                onChange={(e) => patchDraft(draft.id, { appId: e.target.value })}
              />
            </label>
            <label className="c-settings__field">
              <span className="c-settings__field-label">App Secret</span>
              <span className="c-settings__input-row">
                <input
                  className="c-settings__input"
                  aria-label="飞书 App Secret"
                  type={showSecret ? 'text' : 'password'}
                  autoComplete="off"
                  spellCheck={false}
                  value={draft.secret}
                  placeholder={saved?.hasSecret ? '已保存，留空则不修改' : ''}
                  onChange={(e) => patchDraft(draft.id, { secret: e.target.value })}
                />
                <button type="button" className="c-btn c-btn--ghost" onClick={() => setShowSecret((v) => !v)}>
                  {showSecret ? '隐藏' : '显示'}
                </button>
              </span>
            </label>
            <label className="c-settings__field">
              <span className="c-settings__field-label">审批 Owner OpenID</span>
              <input
                className="c-settings__input"
                aria-label="审批 Owner OpenID"
                readOnly
                disabled
                spellCheck={false}
                value={draft.ownerOpenId ?? ''}
                placeholder="未获取到；完成「用我的飞书身份授权」后自动填入"
              />
            </label>
            <label className="c-settings__field">
              <span className="c-settings__field-label">审批白名单 open_id（每行一个）</span>
              <textarea
                className="c-settings__input c-settings__textarea"
                aria-label="审批白名单 open_id"
                spellCheck={false}
                rows={4}
                disabled={busy}
                value={draft.approverText ?? ''}
                placeholder={'ou_xxx\nou_yyy'}
                onChange={(e) => patchDraft(draft.id, { approverText: e.target.value })}
              />
              <span className="c-settings__hint">
                Owner 与白名单都为空时，审批一律拒绝；留空保存表示沿用已保存的值。
              </span>
            </label>
            <details className="c-settings__fold">
              <summary>连不上时</summary>
              <p className="c-settings__hint">
                飞书群和应用里的群不是同一个。打开「群里自己接话」后，不用 @ 也会回答，并回到原来那条消息。关掉后只在 @ 机器人或回复它时回答，其它消息仍记账。自己接话时，飞书后台要开通「获取群组中所有消息」并发布。图片优先用机器人身份下载；下不来且你已授权时，会先发确认卡片。连不上时再看：订阅方式选长连接，事件订 im.message.receive_v1、im.chat.member.bot.added_v1 和 card.action.trigger。重定向 URL 填 http://127.0.0.1:17321/callback。
              </p>
            </details>
            {saved?.detail && (
              <p className="c-settings__note is-bad" role="status">
                {saved.detail}
              </p>
            )}
            {err && (
              <p className="c-settings__note is-bad" role="status">
                {err}
              </p>
            )}
          </>
        )}
        {!draft && err && (
          <p className="c-settings__note is-bad" role="status">
            {err}
          </p>
        )}
      </div>
    </section>
  )
}

type TabId = 'appearance' | 'model' | 'feishu' | 'access' | 'diagnose' | 'about'

const TABS: { id: TabId; icon: string; label: string }[] = [
  { id: 'appearance', icon: '◐', label: '外观' },
  { id: 'model', icon: '✦', label: '连接' },
  { id: 'feishu', icon: '✉', label: '飞书' },
  { id: 'access', icon: '⏻', label: '权限' },
  { id: 'diagnose', icon: '⚕', label: '诊断' },
  { id: 'about', icon: 'ⓘ', label: '关于' },
]

/** 每个分区的标题与一句话说明，让内容自解释 */
const TAB_HEAD: Record<TabId, { title: string; desc: string }> = {
  diagnose: {
    title: '诊断',
    desc: '发消息没反应时先看这里。会真的调一次模型，并列出每个 agent 的真实状态。',
  },
  appearance: {
    title: '外观',
    desc: '选择界面的明暗风格。跟随系统时，会随 macOS / Windows 的深浅色设置自动切换。',
  },
  model: {
    title: '连接',
    desc: '这里只保存厂商的地址和 Key。用哪个模型、哪个文件夹，在每个会话里单独选。',
  },
  feishu: {
    title: '飞书',
    desc: '把企业自建机器人接到本机会话。每个机器人单独保存。',
  },
  access: {
    title: '权限',
    desc: '这些是系统或浏览器里的开关。打开对应页面授权之后，相关能力才能用。',
  },
  about: {
    title: '关于',
    desc: '配置存放位置与版本信息，方便备份、迁移和排查问题。',
  },
}

/** 每个 provider 的卡片图标，让「选谁」比读文字更快 */
const THEME_OPTIONS: { id: Theme; icon: string; label: string; desc: string }[] = [
  { id: 'dark', icon: '🌙', label: '暗色', desc: '深色背景，长时间阅读更省眼' },
  { id: 'light', icon: '☀️', label: '亮色', desc: '明亮背景，适合白天或强光环境' },
  { id: 'system', icon: '🖥', label: '跟随系统', desc: '与操作系统的深浅色设置保持一致' },
]

/**
 * SettingsModal —— 设置。
 *
 * 从上到下的长表单信息密度低、找不到东西，所以改成
 * 「左侧分类导航 + 右侧内容」：每一类都有标题和一句话说明，
 * 内容装进分组卡片里，靠边框和间距分层而不是靠底色深浅。
 */
export default function SettingsModal(p: Props) {
  const { open, version, onClose } = p

  const { theme, setTheme } = useTheme()
  const [tab, setTab] = useState<TabId>('appearance')
  const [settingsPath, setSettingsPath] = useState('')
  const [keepAwakeState, setKeepAwakeState] = useState<KeepAwakeState | null>(null)

  const head = TAB_HEAD[tab]

  useEffect(() => {
    if (!open) return
    API.settingsPath()
      .then(setSettingsPath)
      .catch(() => {})
  }, [open])

  useEffect(() => {
    if (!open) return
    setTab('appearance')
  }, [open])

  useEffect(() => {
    if (!open) return
    API.keepAwake()
      .then(setKeepAwakeState)
      .catch((e) => console.error('Failed to load keepAwake status:', e))
  }, [open])

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="设置"
      width={760}
      footer={
        <>
          <span className="c-settings__footer-state" role="status">
            厂商在列表里单独保存
          </span>
          <button className="c-btn c-btn--primary" onClick={onClose}>
            关闭
          </button>
        </>
      }
    >
      <div className="c-settings">
        {/* 左：分类导航 */}
        <nav className="c-settings__nav" aria-label="设置分类">
          <div className="c-settings__nav-label" aria-hidden>
            设置
          </div>
          {TABS.map((t) => {
            const active = tab === t.id
            return (
              <button
                key={t.id}
                type="button"
                className={`c-settings__nav-item ${active ? 'c-settings__nav-item--active' : ''}`}
                aria-current={active ? 'page' : undefined}
                onClick={() => setTab(t.id)}
              >
                <span className="c-settings__nav-icon" aria-hidden>
                  {t.icon}
                </span>
                <span>{t.label}</span>
              </button>
            )
          })}
        </nav>

        {/* 右：内容 */}
        <div className="c-settings__body">
          <div className="c-settings__body-inner" key={tab}>
            <div className="c-settings__head">
              <h3 className="c-settings__title">{head.title}</h3>
              <p className="c-settings__desc">{head.desc}</p>
            </div>

            {tab === 'appearance' && (
              <section className="c-settings__card">
                <div className="c-settings__card-head">
                  <div>
                    <div className="c-settings__card-title">主题</div>
                    <div className="c-settings__card-sub">立即生效，无需保存</div>
                  </div>
                </div>
                <div className="c-settings__card-body" role="radiogroup" aria-label="主题">
                  <div className="c-settings__grid c-settings__grid--3">
                    {THEME_OPTIONS.map((o) => {
                      const on = theme === o.id
                      return (
                        <button
                          key={o.id}
                          type="button"
                          role="radio"
                          aria-checked={on}
                          className={`c-settings__option c-settings__option--tile ${on ? 'c-settings__option--active' : ''}`}
                          onClick={() => setTheme(o.id)}
                        >
                          <span className="c-settings__option-icon" aria-hidden>
                            {o.icon}
                          </span>
                          <span className="c-settings__option-name">{o.label}</span>
                          <span className="c-settings__option-desc">{o.desc}</span>
                        </button>
                      )
                    })}
                  </div>
                </div>
              </section>
            )}

            {tab === 'appearance' && keepAwakeState && (
              <section className="c-settings__card">
                <div className="c-settings__card-head">
                  <div>
                    <div className="c-settings__card-title">保持唤醒</div>
                    <div className="c-settings__card-sub">防止电脑进入休眠状态（macOS）</div>
                  </div>
                </div>
                <div className="c-settings__card-body">
                  <label className="c-settings__toggle">
                    <input
                      type="checkbox"
                      aria-label="保持唤醒开关"
                      checked={keepAwakeState.on}
                      onChange={(e) => {
                        if (e.target.checked) {
                          API.keepAwake()
                            .then(() => setKeepAwakeState({ on: true }))
                            .catch((err) => console.error('Failed to enable keepAwake:', err))
                        } else {
                          API.stopAwake()
                            .then(() => setKeepAwakeState({ on: false }))
                            .catch((err) => console.error('Failed to disable keepAwake:', err))
                        }
                      }}
                    />
                    <span className="c-settings__toggle-track"></span>
                    <span className="c-settings__toggle-thumb"></span>
                  </label>
                </div>
              </section>
            )}

            {tab === 'model' && <ProviderBoard />}

            {tab === 'feishu' && <FeishuBoard />}

            {tab === 'access' && <AccessBoard />}

            {tab === 'diagnose' && <DiagnosePanel />}

            {tab === 'about' && (
              <>
                <section className="c-settings__card">
                  <div className="c-settings__card-head">
                    <div>
                      <div className="c-settings__card-title">配置存放位置</div>
                      <div className="c-settings__card-sub">权限 600，重启后保留</div>
                    </div>
                  </div>
                  <div className="c-settings__card-body">
                    <p className="c-settings__prose">
                      配置保存在
                      <code className="c-settings__code c-settings__code--block">
                        {settingsPath || '正在读取…'}
                      </code>
                      文件权限 600，只有当前用户可读写；重启应用后仍然保留。
                    </p>
                  </div>
                </section>

                <section className="c-settings__card">
                  <div className="c-settings__card-head">
                    <div>
                      <div className="c-settings__card-title">当前配置</div>
                      <div className="c-settings__card-sub">本次运行实际生效的值</div>
                    </div>
                  </div>
                  <div className="c-settings__card-body">
                    <dl className="c-settings__kv">
                      <div className="c-settings__kv-row">
                        <dt className="c-settings__kv-key">版本</dt>
                        <dd className="c-settings__kv-val mono">{version || '—'}</dd>
                      </div>
                      <div className="c-settings__kv-row">
                        <dt className="c-settings__kv-key">模型和文件夹</dt>
                        <dd className="c-settings__kv-val">每个会话自己一份</dd>
                      </div>
                    </dl>
                  </div>
                </section>
              </>
            )}
          </div>
        </div>
      </div>
    </Modal>
  )
}
