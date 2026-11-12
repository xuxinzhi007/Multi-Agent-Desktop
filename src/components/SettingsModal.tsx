import { useEffect, useState } from 'react'
import { PROVIDERS } from '../lib/presets'
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

  useEffect(() => {
    if (p.open) {
      setCfg(p.llm)
      setDir(p.workDir)
    }
  }, [p.open, p.llm, p.workDir])

  if (!p.open) return null

  const pick = (id: string) => {
    setCfg({
      provider: id,
      baseUrl: DEFAULT_BASE[id] ?? '',
      apiKey: id === cfg.provider ? cfg.apiKey : '',
      model: DEFAULT_MODEL[id] ?? '',
    })
  }

  return (
    <div
      onClick={p.onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.62)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
        backdropFilter: 'blur(2px)',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 470,
          maxHeight: '86vh',
          overflowY: 'auto',
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-xl)',
          padding: 'var(--space-5)',
          boxShadow: 'var(--shadow-lg)',
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
              <input
                value={cfg.model}
                onChange={(e) => setCfg({ ...cfg, model: e.target.value })}
                placeholder={DEFAULT_MODEL[cfg.provider] ?? ''}
                className="mono"
                style={{ width: '100%' }}
              />
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
          <input
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            className="mono"
            style={{ width: '100%' }}
            placeholder="/path/to/project"
          />
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