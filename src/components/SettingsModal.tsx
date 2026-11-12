import { useEffect, useState } from 'react'
import { PROVIDERS } from '../lib/presets'
import { useTheme, type Theme } from '../lib/useTheme'
import { Sidecar } from '../mygo'
import type { LLMConfig } from '../lib/useSidecar'

type Props = {
  open: boolean
  llm: LLMConfig
  workDir: string
  onClose: () => void
  onSave: (cfg: LLMConfig) => void
  onWorkDir: (dir: string) => void
}

const DEFAULT_BASE: Record<string, string> = {
  ollama: 'http://127.0.0.1:11434',
  openai: 'https://api.openai.com',
  deepseek: 'https://api.deepseek.com',
  claude: 'https://api.anthropic.com',
}

const DEFAULT_MODEL: Record<string, string> = {
  ollama: 'qwen2.5-coder:7b',
  openai: 'gpt-4o-mini',
  deepseek: 'deepseek-chat',
  claude: 'claude-3-5-sonnet-20241022',
}

export default function SettingsModal(p: Props) {
  const [cfg, setCfg] = useState<LLMConfig>(p.llm)
  const [dir, setDir] = useState(p.workDir)
  const { theme, setTheme } = useTheme()
  const [alwaysOnTop, setAlwaysOnTop] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [modelError, setModelError] = useState('')

  useEffect(() => {
    if (p.open) {
      setCfg(p.llm)
      setDir(p.workDir)
    }
  }, [p.open, p.llm, p.workDir])

  if (!p.open) return null

  const loadModels = async () => {
    setLoadingModels(true)
    setModelError('')
    try {
      const list = await Sidecar.listModels(cfg)
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
    <div
      onClick={p.onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.5)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
        backdropFilter: 'blur(8px)',
        WebkitBackdropFilter: 'blur(8px)',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 470,
          maxHeight: '86vh',
          overflowY: 'auto',
          background: 'color-mix(in srgb, var(--bg-elevated) 85%, transparent)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-xl)',
          padding: 'var(--space-5)',
          boxShadow: 'var(--shadow-lg)',
          backdropFilter: 'blur(20px)',
          WebkitBackdropFilter: 'blur(20px)',
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            marginBottom: 'var(--space-5)',
          }}
        >
          <h2 style={{ margin: 0, fontSize: 'var(--text-lg)', fontWeight: 600 }}>设置</h2>
          <button className="c-icon-btn" style={{ marginLeft: 'auto' }} onClick={p.onClose}>
            ×
          </button>
        </div>

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

        <Field label="窗口">
          <button
            onClick={() => {
              const next = !alwaysOnTop
              setAlwaysOnTop(next)
              Sidecar.setAlwaysOnTop(next)
            }}
            style={{
              width: '100%',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              padding: '9px 12px',
              borderRadius: 'var(--radius-md)',
              background: 'var(--bg-surface)',
              border: `1px solid ${alwaysOnTop ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
              transition: 'all var(--dur-fast) var(--ease)',
            }}
          >
            <span
              style={{
                fontSize: 'var(--text-base)',
                color: alwaysOnTop ? 'var(--accent-bright)' : 'var(--text-primary)',
                fontWeight: alwaysOnTop ? 600 : 400,
              }}
            >
              悬浮窗口（置顶）
            </span>
            <span
              style={{
                width: 32,
                height: 18,
                borderRadius: 'var(--radius-full)',
                background: alwaysOnTop ? 'var(--accent-primary)' : 'var(--border-default)',
                position: 'relative',
                transition: 'background var(--dur-fast) var(--ease)',
              }}
            >
              <span
                style={{
                  position: 'absolute',
                  top: 2,
                  left: alwaysOnTop ? 16 : 2,
                  width: 14,
                  height: 14,
                  borderRadius: 'var(--radius-full)',
                  background: '#fff',
                  transition: 'left var(--dur-fast) var(--ease)',
                }}
              />
            </span>
          </button>
        </Field>

        <Field label="模型提供方">
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
                <select
                  value={cfg.model}
                  onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
                  style={{
                    width: '100%',
                    marginTop: 6,
                    padding: '6px 8px',
                    borderRadius: 'var(--radius-sm)',
                    border: '1px solid var(--border-default)',
                    background: 'var(--bg-surface)',
                    color: 'var(--text-primary)',
                    fontSize: 'var(--text-xs)',
                  }}
                >
                  <option value="">选择模型…</option>
                  {models.map((m) => (
                    <option key={m} value={m}>{m}</option>
                  ))}
                </select>
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
                {cfg.provider === 'ollama' ? 'Ollama 本地不需要，留空' : '仅存在 sidecar 内存，不落盘'}
              </div>
            </Field>
          </>
        )}

        <Field label="工作目录">
          <div style={{ display: 'flex', gap: 6 }}>
            <input
              value={dir}
              onChange={(e) => setDir(e.target.value)}
              className="mono"
              style={{ flex: 1 }}
              placeholder="/path/to/project"
            />
            <button
              className="c-btn c-btn--secondary c-btn--sm"
              onClick={async () => {
                const picked = await Sidecar.pickDirectory()
                if (picked) setDir(picked)
              }}
            >
              浏览…
            </button>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginTop: 4 }}>
            shell 命令在此执行
          </div>
        </Field>

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
            <br />
            检查器里的改动 / 任务 / 工具是演示数据，用来展示界面结构。
          </div>
        )}

        <div style={{ display: 'flex', gap: 'var(--space-2)' }}>
          <button
            className="c-btn c-btn--primary"
            style={{ flex: 1 }}
            onClick={() => {
              p.onSave(cfg)
              if (dir !== p.workDir) p.onWorkDir(dir)
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
    </div>
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