import { useEffect, useMemo, useState } from 'react'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
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

import DiagnosePanel from './DiagnosePanel'
import ModelList from './ModelList'

type TabId = 'appearance' | 'model' | 'diagnose' | 'about'

const TABS: { id: TabId; icon: string; label: string }[] = [
  { id: 'appearance', icon: '◐', label: '外观' },
  { id: 'model', icon: '✦', label: '模型' },
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
    title: '模型',
    desc: '配置对话背后的推理引擎。凭据只在这里填一次，其余地方只做选择。',
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
  const { open, llm, version, onClose, onSave } = p

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
    setCfg(llm)
    setTab('appearance')
  }, [open, llm])

  const dirty = useMemo(
    () =>
      cfg.provider !== llm.provider ||
      cfg.baseUrl !== llm.baseUrl ||
      cfg.apiKey !== llm.apiKey ||
      cfg.model !== llm.model,
    [cfg, llm],
  )

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
                {t.id === 'model' && llm.model && (
                  // 显示「当前实际在用」的模型，而不是正在编辑的草稿。
                  // 编辑中的值还没保存，显示它会让用户以为已经切过去了。
                  <span className="c-settings__nav-hint" title={llm.model}>
                    {llm.model.length > 14 ? `${llm.model.slice(0, 13)}…` : llm.model}
                  </span>
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
              <ModelList
                current={llm}
                // 点一下立刻生效并落盘：用户不需要再按「保存」。
                // 多一次确认只会让人担心「我到底选上没有」。
                onPick={(next) => {
                  setCfg(next)
                  onSave(next)
                }}
              />
            )}

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
