import type { ShellLog } from '../lib/useSidecar'
import { prettyTime } from '../lib/presets'

export default function ShellPanel({ logs, onClose }: { logs: ShellLog[]; onClose: () => void }) {
  return (
    <aside
      className="c-col slide-in"
      style={{
        width: 360,
        flexShrink: 0,
        background: 'var(--bg-chrome)',
        borderLeft: '1px solid var(--border-subtle)',
      }}
    >
      <div
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '0 var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span className="c-section-label">命令执行</span>
        <span
          className="mono"
          style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}
        >
          {logs.length}
        </span>
        <button className="c-icon-btn" style={{ marginLeft: 'auto' }} onClick={onClose}>
          ×
        </button>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-2)' }}>
        {logs.length === 0 && (
          <div className="c-empty">
            还没有执行记录
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 6 }}>
              底部切到「终端」发命令
            </div>
          </div>
        )}

        {logs.map((l) => (
          <div
            key={l.id}
            className="fade-up mono"
            style={{
              marginBottom: 'var(--space-2)',
              background: 'var(--bg-surface)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)',
              overflow: 'hidden',
              fontSize: 'var(--text-xs)',
            }}
          >
            <div
              style={{
                padding: '7px 9px',
                borderBottom: l.result ? '1px solid var(--border-subtle)' : 'none',
                display: 'flex',
                alignItems: 'center',
                gap: 6,
              }}
            >
              <span style={{ color: 'var(--success)', flexShrink: 0 }}>$</span>
              <span style={{ flex: 1, wordBreak: 'break-all', color: 'var(--text-primary)' }}>
                {l.command}
              </span>
              <span style={{ color: 'var(--text-disabled)', fontSize: 9, flexShrink: 0 }}>
                {prettyTime(l.at)}
              </span>
            </div>

            {!l.result && (
              <div style={{ padding: '7px 9px', color: 'var(--warning)' }} className="blink">
                执行中…
              </div>
            )}

            {l.result && (
              <>
                {l.result.stdout && (
                  <pre
                    style={{
                      margin: 0,
                      padding: '7px 9px',
                      whiteSpace: 'pre-wrap',
                      wordBreak: 'break-all',
                      color: 'var(--text-secondary)',
                      maxHeight: 240,
                      overflow: 'auto',
                    }}
                  >
                    {l.result.stdout}
                  </pre>
                )}
                {l.result.stderr && (
                  <pre
                    style={{
                      margin: 0,
                      padding: '7px 9px',
                      whiteSpace: 'pre-wrap',
                      wordBreak: 'break-all',
                      color: 'var(--danger)',
                      borderTop: '1px solid var(--border-subtle)',
                      maxHeight: 160,
                      overflow: 'auto',
                    }}
                  >
                    {l.result.stderr}
                  </pre>
                )}
                <div
                  style={{
                    padding: '4px 9px',
                    borderTop: '1px solid var(--border-subtle)',
                    color: l.result.exitCode === 0 ? 'var(--success)' : 'var(--danger)',
                    fontSize: 9.5,
                  }}
                >
                  exit {l.result.exitCode}
                </div>
              </>
            )}
          </div>
        ))}
      </div>
    </aside>
  )
}