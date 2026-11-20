import { useEffect, useState } from 'react'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
import { API, type AgentView, type FeishuView, type ProviderEndpoint } from '../mygo'
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

function FeishuBoard() {
  const [view, setView] = useState<FeishuView | null>(null)
  const [agents, setAgents] = useState<AgentView[]>([])
  const [enabled, setEnabled] = useState(false)
  const [joinAll, setJoinAll] = useState(true)
  const [appId, setAppId] = useState('')
  const [secret, setSecret] = useState('')
  const [agentId, setAgentId] = useState('')
  const [showSecret, setShowSecret] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const load = async () => {
    const [next, list] = await Promise.all([API.getFeishu(), API.listAgents()])
    setView(next)
    setAgents(list)
    setEnabled(next.enabled)
    setJoinAll(!next.mentionOnly)
    setAppId(next.appId)
    setAgentId(next.agentId)
  }

  useEffect(() => {
    void load().catch((e: unknown) => setErr(errText(e)))
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      API.getFeishu()
        .then(setView)
        .catch(() => {})
    }, 2000)
    return () => window.clearInterval(timer)
  }, [])

  const save = async () => {
    setBusy(true)
    setErr('')
    try {
      const next = await API.saveFeishu({
        enabled,
        appId,
        appSecret: secret,
        agentId,
        mentionOnly: !joinAll,
      })
      setView(next)
      setSecret('')
    } catch (e) {
      setErr(errText(e))
    } finally {
      setBusy(false)
    }
  }

  const statusText = (state: string | undefined) => {
    if (state === 'connected') return '已连接'
    if (state === 'connecting') return '连接中'
    if (state === 'error') return '连接失败'
    return '未连接'
  }

  return (
    <section className="c-settings__card">
      <div className="c-settings__card-head">
        <div>
          <div className="c-settings__card-title">飞书机器人</div>
          <div className="c-settings__card-sub">记下飞书会话。群里可以自己接话，也可以改成只有 @ 或回复才回答</div>
        </div>
        <span className={`c-providers__badge ${view?.status === 'connected' ? 'is-ok' : 'is-bad'}`}>
          {statusText(view?.status)}
        </span>
      </div>
      <div className="c-settings__card-body">
        <label className="c-settings__field">
          <span className="c-settings__field-label">启用</span>
          <input
            type="checkbox"
            aria-label="启用飞书机器人"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
        </label>
        <label className="c-settings__field">
          <span className="c-settings__field-label">群里自己接话</span>
          <input
            type="checkbox"
            aria-label="群里自己接话"
            checked={joinAll}
            onChange={(e) => setJoinAll(e.target.checked)}
          />
        </label>
        <label className="c-settings__field">
          <span className="c-settings__field-label">App ID</span>
          <input
            className="c-settings__input"
            aria-label="飞书 App ID"
            value={appId}
            spellCheck={false}
            onChange={(e) => setAppId(e.target.value)}
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
              value={secret}
              placeholder={view?.hasSecret ? '已保存，留空则不修改' : ''}
              onChange={(e) => setSecret(e.target.value)}
            />
            <button type="button" className="c-btn c-btn--ghost" onClick={() => setShowSecret((v) => !v)}>
              {showSecret ? '隐藏' : '显示'}
            </button>
          </span>
        </label>
        <label className="c-settings__field">
          <span className="c-settings__field-label">默认会话</span>
          <select
            className="c-settings__input"
            aria-label="默认会话"
            value={agentId}
            onChange={(e) => setAgentId(e.target.value)}
          >
            <option value="">选择一个会话</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </select>
        </label>
        <p className="c-settings__hint">
          飞书群和应用里的群不是同一个。打开「群里自己接话」后，不用 @ 也会回答，并回到原来那条消息。关掉后只在 @ 机器人或回复它时回答，其它消息仍记账。自己接话时，飞书后台要开通「获取群组中所有消息」并发布。图片优先用机器人身份下载；下不来且你已授权时，会先发确认卡片。连不上时再看：订阅方式选长连接，事件订 im.message.receive_v1、im.chat.member.bot.added_v1 和 card.action.trigger。重定向 URL 填 http://127.0.0.1:17321/callback。
        </p>
        <div className="c-settings__field">
          <button
            type="button"
            className="c-btn c-btn--ghost"
            disabled={busy || !view?.hasSecret}
            onClick={() => {
              setBusy(true)
              setErr('')
              API.authorizeFeishuUser()
                .then((next) => setView(next))
                .catch((e: unknown) => setErr(errText(e)))
                .finally(() => setBusy(false))
            }}
          >
            {view?.hasUser ? '已授权，重新授权' : '用我的飞书身份授权'}
          </button>
        </div>
        {view?.detail && (
          <p className="c-settings__hint" role="status">
            {view.detail}
          </p>
        )}
        <div className="c-settings__field">
          <button type="button" className="c-btn c-btn--primary" disabled={busy} onClick={() => void save()}>
            {busy ? '正在保存…' : '保存'}
          </button>
          {err && (
            <span className="c-settings__hint" role="status">
              {err}
            </span>
          )}
        </div>
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
    desc: '把一个企业自建机器人的私聊和群聊接到本机的一个会话上。',
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
