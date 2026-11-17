import { useEffect, useState } from 'react'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
import { API, type ProviderEndpoint } from '../mygo'
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

const VENDORS: { id: string; label: string; local: boolean; baseUrl: string }[] = [
  { id: 'llamacpp', label: '本地 llama.cpp', local: true, baseUrl: 'http://127.0.0.1:8080' },
  { id: 'ollama', label: 'Ollama', local: true, baseUrl: 'http://127.0.0.1:11434' },
  { id: 'deepseek', label: 'DeepSeek', local: false, baseUrl: 'https://api.deepseek.com' },
  { id: 'openai', label: 'OpenAI', local: false, baseUrl: 'https://api.openai.com' },
  { id: 'claude', label: 'Claude', local: false, baseUrl: 'https://api.anthropic.com' },
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

function ProviderBoard() {
  const [rows, setRows] = useState<ProviderEndpoint[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState('')
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [busy, setBusy] = useState(false)
  const [formErr, setFormErr] = useState('')

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
  }

  const save = async () => {
    if (!editing) return
    setBusy(true)
    setFormErr('')
    try {
      const ep = await API.saveProvider({
        provider: editing,
        baseUrl,
        apiKey,
        model: '',
      })
      setRows((cur) => {
        const next = cur.some((r) => r.provider === ep.provider)
          ? cur.map((r) => (r.provider === ep.provider ? ep : r))
          : [...cur, ep]
        return next
      })
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
            <div className="c-settings__card-sub">检查通过的标成生效，并列出这个厂商加载到的模型</div>
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
                {row.ok && row.models && row.models.length > 0 && (
                  <ul className="c-providers__models" aria-label={`${vendorLabel(row.provider)} 的模型`}>
                    {row.models.map((m) => (
                      <li key={m}>{m}</li>
                    ))}
                  </ul>
                )}
                {!row.ok && row.reason && <p className="c-settings__hint">{row.reason}</p>}
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
                  onChange={(e) => setBaseUrl(e.target.value)}
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
                        : editingRow?.hasKey
                          ? '已保存，留空则不修改'
                          : 'sk-…'
                    }
                    onChange={(e) => setApiKey(e.target.value)}
                  />
                  <button type="button" className="c-btn c-btn--ghost" onClick={() => setShowKey((v) => !v)}>
                    {showKey ? '隐藏' : '显示'}
                  </button>
                </span>
              </label>
              <div className="c-settings__field">
                <button type="button" className="c-btn c-btn--primary" disabled={busy} onClick={() => void save()}>
                  {busy ? '正在检查…' : '保存并检查'}
                </button>
                {formErr && (
                  <span className="c-settings__hint" role="status">
                    {formErr}
                  </span>
                )}
              </div>
            </>
          )}
        </div>
      </section>
    </>
  )
}

type TabId = 'appearance' | 'model' | 'diagnose' | 'about'

const TABS: { id: TabId; icon: string; label: string }[] = [
  { id: 'appearance', icon: '◐', label: '外观' },
  { id: 'model', icon: '✦', label: '连接' },
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
    desc: '每个厂商单独保存。保存后检查是否生效，生效的会标出来，并列出它加载到的模型。',
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
  const { open, llm, version, onClose } = p

  const [cfg, setCfg] = useState<LLMConfig>(llm)
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

  useEffect(() => {
    if (!open) return
    setCfg(llm)
  }, [open, llm])

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
                        <dt className="c-settings__kv-key">当前提供方</dt>
                        <dd className="c-settings__kv-val">{cfg.provider || '未设置'}</dd>
                      </div>
                      <div className="c-settings__kv-row">
                        <dt className="c-settings__kv-key">API Key</dt>
                        <dd className="c-settings__kv-val">
                          {cfg.apiKey ? '已填写' : '未填写'}
                        </dd>
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
