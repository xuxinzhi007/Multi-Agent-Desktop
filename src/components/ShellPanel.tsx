import type { ShellLog } from '../lib/ws'
import { prettyTime } from '../lib/presets'

export default function ShellPanel({ logs, onClose }: { logs: ShellLog[]; onClose: () => void }) {
  return (
    <div
      style={{
        width: 330,
        flexShrink: 0,
        background: 'var(--panel)',
        borderLeft: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
      }}
    >
      <div
        style={{
          padding: '11px 14px',
          borderBottom: '1px solid var(--border)',
          display: 'flex',
          alignItems: 'center',
        }}
      >
        <span style={{ fontSize: 12, fontWeight: 600 }}>命令执行</span>
        <button
          onClick={onClose}
          style={{ marginLeft: 'auto', color: 'var(--dim)', fontSize: 15, lineHeight: 1 }}
          onMouseEnter={(e) => (e.currentTarget.style.color = 'var(--text)')}
          onMouseLeave={(e) => (e.currentTarget.style.color = 'var(--dim)')}
        >
          ×
        </button>
      </div>

      <div style={{ flex: 1, overflowY: 'auto', padding: 12 }}>
        {logs.length === 0 && (
          <div style={{ color: 'var(--dim)', fontSize: 11.5, lineHeight: 1.9, padding: '8px 2px' }}>
            还没有执行记录。
            <br />
            切到「终端」模式发命令。
          </div>
        )}

        {logs.map((l) => (
          <div
            key={l.id}
            className="fade-up mono"
            style={{
              marginBottom: 12,
              background: 'var(--panel-2)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              overflow: 'hidden',
              fontSize: 11,
            }}
          >
            <div
              style={{
                padding: '7px 9px',
                borderBottom: l.result ? '1px solid var(--border)' : 'none',
                display: 'flex',
                alignItems: 'center',
                gap: 6,
              }}
            >
              <span style={{ color: 'var(--green)' }}>$</span>
              <span style={{ flex: 1, wordBreak: 'break-all' }}>{l.command}</span>
              <span style={{ color: 'var(--dim)', fontSize: 9.5, flexShrink: 0 }}>
                {prettyTime(l.at)}
              </span>
            </div>

            {!l.result && (
              <div style={{ padding: '7px 9px', color: 'var(--amber)' }} className="blink">
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
                      color: '#c2c9d6',
                      maxHeight: 220,
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
                      color: '#ff9a9e',
                      borderTop: '1px solid var(--border)',
                      maxHeight: 160,
                      overflow: 'auto',
                    }}
                  >
                    {l.result.stderr}
                  </pre>
                )}
                <div
                  style={{
                    padding: '5px 9px',
                    borderTop: '1px solid var(--border)',
                    color: l.result.exitCode === 0 ? 'var(--green)' : 'var(--red)',
                    fontSize: 10,
                  }}
                >
                  exit {l.result.exitCode}
                </div>
              </>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}