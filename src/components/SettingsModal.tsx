import { useCallback, useEffect, useMemo, useState } from 'react'
import { PROVIDERS, DEFAULT_BASE, DEFAULT_MODEL } from '../lib/presets'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
import Select from './Select'
import { API } from '../mygo'
import type { LLMConfig } from '../lib/useBackend'

type Props = {
  open: boolean
  llm: LLMConfig
  /** 运行版本号，显示在「关于」页 */
  version?: string
  onClose: () => void
  onSave: (cfg: LLMConfig) => void
}

type TabId = 'appearance' | 'model' | 'about'

const TABS: { id: TabId; icon: string; label: string }[] = [
  { id: 'appearance', icon: '◐', label: '外观' },
  { id: 'model', icon: '✦', label: '模型' },
  { id: 'about', icon: 'ⓘ', label: '关于' },
]

/** 每个分区的标题与一句话说明，让内容自解释 */
const TAB_HEAD: Record<TabId, { title: string; desc: string }> = {
  appearance: {
    title: '外观',
    desc: '选择界面的明暗风格。跟随系统时，会随 macOS / Windows 的深浅色设置自动切换。',
  },
  model: {
    title: '模型',
    desc: '配置对话背后的推理引擎。凭据只在这里填一次，其余地方只做选择。',
  },
  about: {
    title: '关于',
    desc: '配置存放位置与版本信息，方便备份、迁移和排查问题。',
  },
}

/** 每个 provider 的卡片图标，让「选谁」比读文字更快 */
const PROVIDER_ICON: Record<string, string> = {
  mock: '◈',
  ollama: '🦙',
  openai: '◎',
  deepseek: '🐋',
  claude: '✳',
}

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
  const { open, llm, version, onClose, onSave } = p

  const [cfg, setCfg] = useState<LLMConfig>(llm)
  const { theme, setTheme } = useTheme()
  const [tab, setTab] = useState<TabId>('appearance')
  const [settingsPath, setSettingsPath] = useState('')
  const [showKey, setShowKey] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [modelError, setModelError] = useState('')
  /** 模型列表加载完毕但结果为空 —— 与「还没加载过」是两种状态 */
  const [modelsLoaded, setModelsLoaded] = useState(false)

  useEffect(() => {
    if (!open) return
    API.settingsPath()
      .then(setSettingsPath)
      .catch(() => {})
  }, [open])

  /** 回到初始状态：换 provider / 关闭再打开都要把临时 UI 状态清干净 */
  const resetTransient = useCallback(() => {
    setShowKey(false)
    setModels([])
    setModelsLoaded(false)
    setModelError('')
  }, [])

  useEffect(() => {
    if (!open) return
    setCfg(llm)
    setTab('appearance')
    resetTransient()
  }, [open, llm, resetTransient])

  const provider = useMemo(
    () => PROVIDERS.find((x) => x.id === cfg.provider) ?? PROVIDERS[0],
    [cfg.provider],
  )

  const needsCredentials = cfg.provider !== 'mock'
  const keyOptional = cfg.provider === 'ollama'
  const dirty = useMemo(
    () =>
      cfg.provider !== llm.provider ||
      cfg.baseUrl !== llm.baseUrl ||
      cfg.apiKey !== llm.apiKey ||
      cfg.model !== llm.model,
    [cfg, llm],
  )

  const loadModels = useCallback(async () => {
    setLoadingModels(true)
    setModelError('')
    try {
      const list = await API.listModels(cfg)
      setModels(list)
      setModelsLoaded(true)
      if (list.length === 0) setModelError('未获取到模型列表')
    } catch (e) {
      setModels([])
      setModelsLoaded(true)
      setModelError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoadingModels(false)
    }
  }, [cfg])

  const pick = (id: string) => {
    if (id === cfg.provider) return
    setCfg({
      provider: id,
      baseUrl: DEFAULT_BASE[id] ?? '',
      apiKey: '',
      model: DEFAULT_MODEL[id] ?? '',
    })
    resetTransient()
  }

  if (!open) return null

  const head = TAB_HEAD[tab]

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="设置"
      width={760}
      footer={
        <>
          <span className="c-settings__footer-state" role="status">
            {dirty ? '有未保存的修改' : '所有修改已保存'}
          </span>
          <button className="c-btn c-btn--ghost" onClick={onClose}>
            取消
          </button>
          <button
            className="c-btn c-btn--primary"
            disabled={!dirty}
            onClick={() => {
              onSave(cfg)
              onClose()
            }}
          >
            保存
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
                {t.id === 'model' && (
                  <span className="c-settings__nav-hint">{provider.label.split('（')[0]}</span>
                )}
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

            {tab === 'model' && (
              <>
                <section className="c-settings__card">
                  <div className="c-settings__card-head">
                    <div>
                      <div className="c-settings__card-title">提供方</div>
                      <div className="c-settings__card-sub">
                        当前：{provider.label} · 切换后需要重新填写凭据
                      </div>
                    </div>
                    <span className="c-settings__pill">{provider.label.split('（')[0]}</span>
                  </div>
                  <div className="c-settings__card-body" role="radiogroup" aria-label="模型提供方">
                    {PROVIDERS.map((x) => {
                      const on = cfg.provider === x.id
                      return (
                        <button
                          key={x.id}
                          type="button"
                          role="radio"
                          aria-checked={on}
                          className={`c-settings__option ${on ? 'c-settings__option--active' : ''}`}
                          onClick={() => pick(x.id)}
                        >
                          <span className="c-settings__option-icon" aria-hidden>
                            {PROVIDER_ICON[x.id] ?? '◆'}
                          </span>
                          <span className="c-settings__option-body">
                            <span className="c-settings__option-name">
                              {x.label}
                              {x.id === 'mock' && <span className="c-badge">无需配置</span>}
                            </span>
                            <span className="c-settings__option-desc">{x.hint}</span>
                          </span>
                          <span className={`c-settings__check ${on ? 'c-settings__check--on' : ''}`}>
                            {on ? '✓' : ''}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                </section>

                {needsCredentials && (
                  <section className="c-settings__card">
                    <div className="c-settings__card-head">
                      <div>
                        <div className="c-settings__card-title">连接与凭据</div>
                        <div className="c-settings__card-sub">
                          填好 Base URL 与 API Key 后即可加载可用模型
                        </div>
                      </div>
                    </div>
                    <div className="c-settings__card-body">
                      <div className="c-settings__field">
                        <label className="c-settings__field-label" htmlFor="settings-base-url">
                          Base URL
                        </label>
                        <input
                          id="settings-base-url"
                          value={cfg.baseUrl}
                          onChange={(e) => setCfg({ ...cfg, baseUrl: e.target.value })}
                          placeholder={DEFAULT_BASE[cfg.provider] ?? ''}
                          className="mono c-settings__input"
                          spellCheck={false}
                        />
                        <div className="c-settings__hint">
                          留空则使用默认地址{' '}
                          <code className="c-settings__code">
                            {DEFAULT_BASE[cfg.provider] ?? '—'}
                          </code>
                        </div>
                      </div>

                      <div className="c-settings__field">
                        <label className="c-settings__field-label" htmlFor="settings-api-key">
                          API Key
                        </label>
                        <div className="c-settings__input-row">
                          <input
                            id="settings-api-key"
                            type={showKey ? 'text' : 'password'}
                            value={cfg.apiKey}
                            onChange={(e) => setCfg({ ...cfg, apiKey: e.target.value })}
                            placeholder={keyOptional ? '本地服务可留空' : 'sk-...'}
                            className="mono c-settings__input"
                            autoComplete="off"
                            spellCheck={false}
                          />
                          <button
                            type="button"
                            className="c-btn c-btn--secondary c-btn--sm"
                            aria-pressed={showKey}
                            aria-label={showKey ? '隐藏 API Key' : '显示 API Key'}
                            onClick={() => setShowKey((v) => !v)}
                          >
                            {showKey ? '隐藏' : '显示'}
                          </button>
                        </div>
                        <div className="c-settings__hint">
                          {keyOptional
                            ? 'Ollama 跑在本机，通常不需要 Key，留空即可。'
                            : '仅存于进程内存，不写入磁盘；重启后需重新填写。'}
                        </div>
                      </div>
                    </div>
                  </section>
                )}

                {needsCredentials && (
                  <section className="c-settings__card">
                    <div className="c-settings__card-head">
                      <div>
                        <div className="c-settings__card-title">模型</div>
                        <div className="c-settings__card-sub">
                          可以直接手填，也可以从服务端拉取可用列表
                        </div>
                      </div>
                    </div>
                    <div className="c-settings__card-body">
                      <div className="c-settings__field">
                        <label className="c-settings__field-label" htmlFor="settings-model">
                          模型名
                        </label>
                        <div className="c-settings__input-row">
                          <input
                            id="settings-model"
                            value={cfg.model}
                            onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
                            placeholder={DEFAULT_MODEL[cfg.provider] ?? ''}
                            className="mono c-settings__input"
                            spellCheck={false}
                          />
                          <button
                            type="button"
                            className="c-btn c-btn--secondary c-btn--sm c-settings__load-btn"
                            onClick={loadModels}
                            disabled={loadingModels}
                          >
                            {loadingModels
                              ? '加载中…'
                              : models.length > 0
                                ? '刷新列表'
                                : '加载列表'}
                          </button>
                        </div>
                      </div>

                      {/* 加载中 / 错误 / 空 / 成功，四种状态各自说人话 */}
                      {loadingModels && (
                        <div className="c-settings__status" role="status">
                          <span className="c-settings__spinner" aria-hidden />
                          正在加载 {provider.label} 的模型列表…
                        </div>
                      )}

                      {!loadingModels && modelError && (
                        <div className="c-settings__status c-settings__status--error" role="alert">
                          <span aria-hidden>⚠</span>
                          <span className="c-settings__status-text">{modelError}</span>
                          <button
                            type="button"
                            className="c-btn c-btn--ghost c-btn--sm"
                            onClick={loadModels}
                          >
                            重试
                          </button>
                        </div>
                      )}

                      {!loadingModels && !modelError && modelsLoaded && models.length === 0 && (
                        <div className="c-settings__status">
                          <span aria-hidden>∅</span>
                          <span className="c-settings__status-text">
                            这个提供方没有返回任何模型，请确认 Base URL 与 API Key 后重试。
                          </span>
                        </div>
                      )}

                      {!loadingModels && models.length > 0 && (
                        <div className="c-settings__field">
                          <div className="c-settings__status c-settings__status--success">
                            <span aria-hidden>✓</span>
                            <span className="c-settings__status-text">
                              已获取 {models.length} 个模型，点下面选择
                            </span>
                          </div>
                          <Select
                            value={cfg.model}
                            placeholder="选择模型…"
                            options={models.map((m) => ({ value: m, label: m }))}
                            onChange={(m) => setCfg({ ...cfg, model: m })}
                          />
                        </div>
                      )}
                    </div>
                  </section>
                )}

                {cfg.provider === 'mock' && (
                  <section className="c-settings__card">
                    <div className="c-settings__card-head">
                      <div>
                        <div className="c-settings__card-title">Mock 模式</div>
                        <div className="c-settings__card-sub">无需网络与凭据</div>
                      </div>
                    </div>
                    <div className="c-settings__card-body">
                      <div className="c-settings__status c-settings__status--warning">
                        <span aria-hidden>⚠</span>
                        <span className="c-settings__status-text">
                          Mock 模式的回复是本地占位内容，不是真实分析。想接入真实模型请在「提供方」里切换。
                        </span>
                      </div>
                    </div>
                  </section>
                )}

                {cfg.provider === 'ollama' && (
                  <section className="c-settings__card">
                    <div className="c-settings__card-head">
                      <div>
                        <div className="c-settings__card-title">本地 Ollama</div>
                        <div className="c-settings__card-sub">还没装？先跑这两条命令</div>
                      </div>
                    </div>
                    <div className="c-settings__card-body">
                      <div className="c-settings__cmds">
                        <code className="c-settings__cmd">ollama serve</code>
                        <code className="c-settings__cmd">ollama pull qwen2.5-coder:7b</code>
                      </div>
                    </div>
                  </section>
                )}
              </>
            )}

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
                        <dd className="c-settings__kv-val">{provider.label}</dd>
                      </div>
                      <div className="c-settings__kv-row">
                        <dt className="c-settings__kv-key">当前模型</dt>
                        <dd className="c-settings__kv-val mono">{cfg.model || '—'}</dd>
                      </div>
                      <div className="c-settings__kv-row">
                        <dt className="c-settings__kv-key">API Key</dt>
                        <dd className="c-settings__kv-val">
                          {cfg.apiKey ? '已填写（仅本次运行有效）' : '未填写'}
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
