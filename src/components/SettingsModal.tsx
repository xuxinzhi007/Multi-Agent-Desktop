import { useEffect, useState } from 'react'
import { PROVIDERS } from '../lib/presets'
import type { LLMConfig } from '../lib/ws'

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

  const pickProvider = (id: string) => {
    setCfg({
      provider: id,
      baseUrl: DEFAULT_BASE[id] ?? '',
      apiKey: cfg.apiKey,
      model: DEFAULT_MODEL[id] ?? '',
    })
  }

  const active = PROVIDERS.find((x) => x.id === cfg.provider)

  return (
    <div
      onClick={p.onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.6)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 460,
          maxHeight: '86vh',
          overflowY: 'auto',
          background: 'var(--panel)',
          border: '1px solid var(--border)',
          borderRadius: 10,
          padding: 22,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', marginBottom: 18 }}>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 600 }}>设置</h2>
          <button
            onClick={p.onClose}
            style={{ marginLeft: 'auto', color: 'var(--dim)', fontSize: 18, lineHeight: 1 }}
          >
            ×
          </button>
        </div>

        <Field label="模型提供方">
          <div style={{ display: 'grid', gap: 5 }}>
            {PROVIDERS.map((x) => (
              <button
                key={x.id}
                onClick={() => pickProvider(x.id)}
                style={{
                  textAlign: 'left',
                  padding: '8px 11px',
                  borderRadius: 6,
                  border: `1px solid ${cfg.provider === x.id ? 'var(--accent)' : 'var(--border)'}`,
                  background: cfg.provider === x.id ? 'rgba(91,157,255,0.09)' : 'var(--panel-2)',
                }}
              >
                <div style={{ fontSize: 12.5, fontWeight: 500 }}>{x.label}</div>
                <div style={{ fontSize: 10.5, color: 'var(--dim)', marginTop: 2 }}>{x.hint}</div>
              </button>
            ))}
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
              <div style={{ fontSize: 10.5, color: 'var(--dim)', marginTop: 5 }}>
                {cfg.provider === 'ollama' ? 'Ollama 本地不需要 key，留空即可' : '仅存在 sidecar 进程内存中'}
              </div>
            </Field>
          </>
        )}

        <Field label="工作目录（shell 命令在此执行）">
          <input
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            className="mono"
            style={{ width: '100%' }}
            placeholder="/path/to/project"
          />
        </Field>

        {cfg.provider === 'mock' && (
          <div
            style={{
              background: 'rgba(232,163,61,0.09)',
              border: '1px solid rgba(232,163,61,0.3)',
              borderRadius: 6,
              padding: '9px 12px',
              fontSize: 11,
              color: '#e8c07d',
              lineHeight: 1.7,
              marginBottom: 16,
            }}
          >
            当前是 mock 模式：回复由本地规则生成，用于验证多 agent 并行、流式渲染和会话隔离。
            切到真实模型后即可获得实际能力。
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, marginTop: 6 }}>
          <button
            onClick={() => {
              p.onSave(cfg)
              if (dir !== p.workDir) p.onWorkDir(dir)
              p.onClose()
            }}
            style={{
              flex: 1,
              background: 'var(--accent)',
              color: '#fff',
              padding: '9px 0',
              borderRadius: 6,
              fontWeight: 500,
              fontSize: 12.5,
            }}
          >
            保存
          </button>
          <button
            onClick={p.onClose}
            style={{
              flex: 1,
              background: 'var(--panel-2)',
              border: '1px solid var(--border)',
              padding: '9px 0',
              borderRadius: 6,
              fontSize: 12.5,
              color: 'var(--muted)',
            }}
          >
            取消
          </button>
        </div>

        {active && cfg.provider === 'ollama' && (
          <div style={{ marginTop: 12, fontSize: 10.5, color: 'var(--dim)', lineHeight: 1.8 }}>
            本机未检测到 Ollama。装好后先 <span className="mono">ollama serve</span>，再{' '}
            <span className="mono">ollama pull qwen2.5-coder:7b</span>。
          </div>
        )}
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 16 }}>
      <div style={{ fontSize: 11.5, color: 'var(--muted)', marginBottom: 7 }}>{label}</div>
      {children}
    </div>
  )
}