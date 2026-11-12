import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { events, type MiniStatus } from './mygo'

function MiniApp() {
  const [status, setStatus] = useState<MiniStatus>({ agents: [] })

  useEffect(() => {
    // 订阅迷你窗状态事件
    const unsub = events.miniStatus.on((s) => setStatus(s))
    return unsub
  }, [])

  const running = status.agents.filter((a) => a.status === 'running').length
  const queued = status.agents.filter((a) => a.status === 'queued').length

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 8,
        padding: '8px 14px',
        background: 'rgba(20, 23, 32, 0.92)',
        borderRadius: 20,
        boxShadow: '0 4px 20px rgba(0,0,0,0.4)',
        backdropFilter: 'blur(12px)',
        border: '1px solid rgba(255,255,255,0.08)',
        height: '100%',
        boxSizing: 'border-box',
      }}
    >
      {/* Logo */}
      <span style={{ fontSize: 12, fontWeight: 700, color: '#4d8cf0' }}>A</span>

      {/* Agent 状态点 */}
      <div style={{ display: 'flex', gap: 5, alignItems: 'center' }}>
        {status.agents.length === 0 && (
          <span style={{ fontSize: 10, color: '#6e7789' }}>无 agent</span>
        )}
        {status.agents.slice(0, 6).map((a) => (
          <span
            key={a.id}
            title={`${a.name}: ${a.status}`}
            style={{
              width: 8,
              height: 8,
              borderRadius: '50%',
              background: a.color,
              opacity: a.status === 'idle' ? 0.4 : 1,
              boxShadow:
                a.status === 'running'
                  ? `0 0 6px ${a.color}`
                  : a.status === 'queued'
                    ? `0 0 4px ${a.color}`
                    : 'none',
              animation:
                a.status === 'running' || a.status === 'queued' ? 'pulse 1.2s infinite' : 'none',
            }}
          />
        ))}
        {status.agents.length > 6 && (
          <span style={{ fontSize: 9, color: '#6e7789' }}>+{status.agents.length - 6}</span>
        )}
      </div>

      {/* 统计 */}
      {(running > 0 || queued > 0) && (
        <span style={{ fontSize: 10, color: '#a4adbd' }}>
          {running > 0 && `${running} 运行`}
          {running > 0 && queued > 0 && ' · '}
          {queued > 0 && `${queued} 排队`}
        </span>
      )}

      {/* 点击展开主窗口（预留） */}
      <button
        onClick={() => {
          // 后续：调 Sidecar 聚焦主窗口
        }}
        style={{
          marginLeft: 'auto',
          width: 18,
          height: 18,
          borderRadius: '50%',
          border: 'none',
          background: 'rgba(255,255,255,0.1)',
          color: '#a4adbd',
          fontSize: 10,
          cursor: 'pointer',
          display: 'grid',
          placeItems: 'center',
        }}
        title="展开"
      >
        ↑
      </button>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
