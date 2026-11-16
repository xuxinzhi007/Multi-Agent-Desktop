import { useEffect, useState } from 'react'
import { PROVIDERS, DEFAULT_BASE, DEFAULT_MODEL } from '../lib/presets'
import { useTheme, type Theme } from '../lib/useTheme'
import Modal from './Modal'
import Select from './Select'
import { API } from '../mygo'
import type { LLMConfig } from '../lib/useBackend'

type Props = {
  open: boolean
  llm: LLMConfig
  onClose: () => void
  onSave: (cfg: LLMConfig) => void
}

export default function SettingsModal(p: Props) {
  const [cfg, setCfg] = useState<LLMConfig>(p.llm)
  const { theme, setTheme } = useTheme()
  const [settingsPath, setSettingsPath] = useState('')
  const [models, setModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [modelError, setModelError] = useState('')

  useEffect(() => {
    if (!p.open) return
    API.settingsPath()
      .then(setSettingsPath)
      .catch(() => {})
  }, [p.open])

  useEffect(() => {
    if (p.open) {
      setCfg(p.llm)
    }
  }, [p.open, p.llm])

  if (!p.open) return null

  const loadModels = async () => {
    setLoadingModels(true)
    setModelError('')
    try {
      const list = await API.listModels(cfg)
      setModels(list)
      if (list.length === 0) {
        setModelError('未获取到模型列表')
      }
    } catch (e) {
      setModelError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoadingModels(false)
    }
  }

  const pick = (id: string) => {
    setCfg({
      provider: id,
      baseUrl: DEFAULT_BASE[id] ?? '',
      apiKey: id === cfg.provider ? cfg.apiKey : '',
      model: DEFAULT_MODEL[id] ?? '',
    })
    setModels([]) // 切换 provider 时清空模型列表
  }

  return (
    <Modal open={p.open} onClose={p.onClose} title="设置" width={470}>
      <div style={{ padding: 'var(--space-5)' }}>
        <Field label="外观">
          <div style={{ display: 'flex', gap: 4 }}>
            {(['dark', 'light', 'system'] as Theme[]).map((t) => (
              <button
                key={t}
                onClick={() => setTheme(t)}
                style={{
                  flex: 1,
                  padding: '7px 0',
                  borderRadius: 'var(--radius-md)',
                  fontSize: 'var(--text-sm)',
                  fontWeight: theme === t ? 600 : 400,
                  color: theme === t ? 'var(--accent-bright)' : 'var(--text-tertiary)',
                  background: theme === t ? 'var(--accent-dim)' : 'var(--bg-surface)',
                  border: `1px solid ${theme === t ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
                  transition: 'all var(--dur-fast) var(--ease)',
                }}
              >
                {t === 'dark' ? '暗色' : t === 'light' ? '亮色' : '跟随系统'}
              </button>
            ))}
          </div>
        </Field>

        <Field label="默认模型提供方">
          <div style={{ display: 'grid', gap: 4 }}>
            {PROVIDERS.map((x) => {
              const on = cfg.provider === x.id
              return (
                <button
                  key={x.id}
                  onClick={() => pick(x.id)}
                  style={{
                    textAlign: 'left',
                    padding: '8px 11px',
                    borderRadius: 'var(--radius-md)',
                    border: `1px solid ${on ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
                    background: on ? 'var(--accent-dim)' : 'var(--bg-surface)',
                    transition: 'all var(--dur-fast) var(--ease)',
                  }}
                >
                  <div
                    style={{
                      fontSize: 'var(--text-base)',
                      fontWeight: on ? 600 : 500,
                      color: on ? 'var(--accent-bright)' : 'var(--text-primary)',
                    }}
                  >
                    {x.label}
                  </div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginTop: 1 }}>
                    {x.hint}
                  </div>
                </button>
              )
            })}
          </div>
        </Field>

        {cfg.provider !== 'mock' && (
          <>
            <Field label="Base URL">
              <input
                value={cfg.baseUrl}
                onChange={(e) => setCfg({ ...cfg, baseUrl: e.target.value })}
                placeholder={DEFAULT_BASE[cfg.provider] ?? ''}
                className="mono"
                style={{ width: '100%' }}
              />
            </Field>
            <Field label="模型名">
              <div style={{ display: 'flex', gap: 6 }}>
                <input
                  value={cfg.model}
                  onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
                  placeholder={DEFAULT_MODEL[cfg.provider] ?? ''}
                  className="mono"
                  style={{ flex: 1 }}
                />
                <button
                  onClick={loadModels}
                  disabled={loadingModels}
                  style={{
                    padding: '6px 10px',
                    borderRadius: 'var(--radius-sm)',
                    border: '1px solid var(--border-default)',
                    background: 'var(--bg-surface)',
                    color: 'var(--text-primary)',
                    fontSize: 'var(--text-xs)',
                    cursor: loadingModels ? 'wait' : 'pointer',
                    whiteSpace: 'nowrap',
                  }}
                >
                  {loadingModels ? '加载中…' : '加载列表'}
                </button>
              </div>
              {models.length > 0 && (
                <div style={{ marginTop: 6 }}>
                  <Select
                    value={cfg.model}
                    placeholder="选择模型…"
                    options={models.map((m) => ({ value: m, label: m }))}
                    onChange={(m) => setCfg({ ...cfg, model: m })}
                  />
                </div>
              )}
              {modelError && (
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginTop: 4 }}>
                  {modelError}
                </div>
              )}
            </Field>
            <Field label="API Key">
              <input
                type="password"
                value={cfg.apiKey}
                onChange={(e) => setCfg({ ...cfg, apiKey: e.target.value })}
                placeholder="sk-..."
                className="mono"
                style={{ width: '100%' }}
              />
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginTop: 4 }}>
                {cfg.provider === 'ollama' ? 'Ollama 本地不需要，留空' : '仅存在进程内存，不落盘'}
              </div>
            </Field>
          </>
        )}

        {cfg.provider === 'mock' && (
          <div
            style={{
              background: 'var(--warning-dim)',
              border: '1px solid var(--warning-border)',
              borderRadius: 'var(--radius-md)',
              padding: '9px 12px',
              fontSize: 'var(--text-xs)',
              color: 'var(--warning)',
              lineHeight: 'var(--leading-normal)',
              marginBottom: 'var(--space-4)',
            }}
          >
            Mock 模式的回复是本地占位内容，不是真实分析。
          </div>
        )}

        {/* 明确告诉用户密钥存在哪：既是知情权，也方便备份与迁移 */}
        {settingsPath && (
          <div
            style={{
              fontSize: 'var(--text-xs)',
              color: 'var(--text-disabled)',
              lineHeight: 1.6,
              marginBottom: 'var(--space-4)',
            }}
          >
            配置保存在
            <code
              className="mono"
              style={{
                margin: '0 4px',
                padding: '1px 5px',
                background: 'var(--bg-inset)',
                borderRadius: 'var(--radius-sm)',
                color: 'var(--text-tertiary)',
              }}
            >
              {settingsPath}
            </code>
            （权限 600，重启后保留）
          </div>
        )}

        <div style={{ display: 'flex', gap: 'var(--space-2)' }}>
          <button
            className="c-btn c-btn--primary"
            style={{ flex: 1 }}
            onClick={() => {
              p.onSave(cfg)
              p.onClose()
            }}
          >
            保存
          </button>
          <button className="c-btn c-btn--secondary" style={{ flex: 1 }} onClick={p.onClose}>
            取消
          </button>
        </div>

        {cfg.provider === 'ollama' && (
          <div
            style={{
              marginTop: 'var(--space-3)',
              fontSize: 'var(--text-xs)',
              color: 'var(--text-disabled)',
              lineHeight: 'var(--leading-relaxed)',
            }}
          >
            本机未装 Ollama 时先执行：
            <br />
            <span className="mono">ollama serve</span> ·{' '}
            <span className="mono">ollama pull qwen2.5-coder:7b</span>
          </div>
        )}
      </div>
    </Modal>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 'var(--space-4)' }}>
      <div
        style={{
          fontSize: 'var(--text-xs)',
          color: 'var(--text-tertiary)',
          marginBottom: 6,
          fontWeight: 500,
        }}
      >
        {label}
      </div>
      {children}
    </div>
  )
}