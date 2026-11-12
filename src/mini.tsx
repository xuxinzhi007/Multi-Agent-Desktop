import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { events, Sidecar, type MiniStatus } from './mygo'

function MiniApp() {
  const [status, setStatus] = useState<MiniStatus>({ agents: [] })
  const [expanded, setExpanded] = useState(false)
  const [hovered, setHovered] = useState<string | null>(null)

  useEffect(() => {
    const unsub = events.miniStatus.on((s) => setStatus(s))
    Sidecar.listAgents().then((agents) => {
      setStatus({
        agents: agents.map((a) => ({
          id: a.id,
          name: a.name,
          color: a.color,
          status: a.status,
          task: '',
        })),
      })
    })
    return unsub
  }, [])

  const running = status.agents.filter((a) => a.status === 'running').length
  const queued = status.agents.filter((a) => a.status === 'queued').length
  const errors = status.agents.filter((a) => a.status === 'error').length

  const statusColor = (s: string) =>
    s === 'running' ? '#e8a33d' : s === 'queued' ? '#4d8cf0' : s === 'error' ? '#e5534b' : '#6e7789'

  const statusLabel = (s: string) =>
    s === 'running' ? '运行中' : s === 'queued' ? '排队' : s === 'error' ? '出错' : '空闲'

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        padding: '4px',
        // @ts-ignore CSS 变量
        '--app-region': 'drag',
      } as React.CSSProperties}
    >
      {/* 灵动岛主体 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 16px',
          background: 'rgba(15, 17, 26, 0.92)',
          borderRadius: expanded ? '20px 20px 0 0' : 24,
          boxShadow: '0 12px 40px rgba(0,0,0,0.6), 0 0 0 1px rgba(255,255,255,0.08)',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          border: '1px solid rgba(255,255,255,0.1)',
          borderBottom: expanded ? 'none' : undefined,
          height: 52,
          boxSizing: 'border-box',
          transition: 'border-radius 0.25s cubic-bezier(0.4, 0, 0.2, 1)',
          position: 'relative',
        }}
      >
        {/* 品牌 */}
        <span style={{ fontSize: 13, fontWeight: 700, color: '#4d8cf0', letterSpacing: '-0.02em' }}>
          舰队
        </span>

        {/* Agent 状态点 */}
        <div style={{ display: 'flex', gap: 6, alignItems: 'center', flex: 1 }}>
          {status.agents.length === 0 && (
            <span style={{ fontSize: 11, color: '#6e7789' }}>无 agent</span>
          )}
          {status.agents.slice(0, 6).map((a) => (
            <div
              key={a.id}
              style={{ position: 'relative' }}
              onMouseEnter={() => setHovered(a.id)}
              onMouseLeave={() => setHovered(null)}
            >
              <span
                style={{
                  width: a.status === 'running' ? 12 : 10,
                  height: a.status === 'running' ? 12 : 10,
                  borderRadius: '50%',
                  background: a.status === 'error' ? '#e5534b' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                  boxShadow:
                    a.status === 'running'
                      ? `0 0 12px ${a.color}, 0 0 4px ${a.color}`
                      : a.status === 'error'
                        ? '0 0 10px #e5534b'
                        : a.status === 'queued'
                          ? `0 0 6px ${a.color}`
                          : 'none',
                  animation:
                    a.status === 'running'
                      ? 'pulse 1s infinite'
                      : a.status === 'error'
                        ? 'errorPulse 0.8s infinite'
                        : a.status === 'queued'
                          ? 'pulse 1.5s infinite'
                          : 'none',
                  cursor: 'pointer',
                  display: 'block',
                  transition: 'all 0.2s',
                }}
              />
              {/* 错误角标 */}
              {a.status === 'error' && (
                <span
                  style={{
                    position: 'absolute',
                    top: -4,
                    right: -4,
                    width: 12,
                    height: 12,
                    borderRadius: '50%',
                    background: '#e5534b',
                    color: '#fff',
                    fontSize: 8,
                    fontWeight: 700,
                    display: 'grid',
                    placeItems: 'center',
                    border: '1.5px solid rgba(15,17,26,0.9)',
                  }}
                >
                  !
                </span>
              )}
              {/* Hover tooltip */}
              {hovered === a.id && (
                <div
                  style={{
                    position: 'absolute',
                    bottom: 'calc(100% + 8px)',
                    left: '50%',
                    transform: 'translateX(-50%)',
                    background: 'rgba(20, 23, 32, 0.95)',
                    border: '1px solid rgba(255,255,255,0.1)',
                    borderRadius: 8,
                    padding: '8px 12px',
                    fontSize: 11,
                    color: '#e8ebf1',
                    whiteSpace: 'nowrap',
                    zIndex: 100,
                    boxShadow: '0 4px 16px rgba(0,0,0,0.4)',
                    pointerEvents: 'none',
                  }}
                >
                  <div style={{ fontWeight: 600, marginBottom: 2 }}>{a.name}</div>
                  <div style={{ color: statusColor(a.status), fontSize: 10 }}>{statusLabel(a.status)}</div>
                  {a.task && (
                    <div style={{ color: '#a4adbd', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal', wordBreak: 'break-all' }}>
                      {a.task}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
          {status.agents.length > 6 && (
            <span style={{ fontSize: 10, color: '#6e7789' }}>+{status.agents.length - 6}</span>
          )}
        </div>

        {/* 统计 */}
        {(running > 0 || queued > 0 || errors > 0) && (
          <span style={{ fontSize: 11, color: '#a4adbd', display: 'flex', gap: 6 }}>
            {running > 0 && <span style={{ color: '#e8a33d' }}>{running} 运行</span>}
            {queued > 0 && <span style={{ color: '#4d8cf0' }}>{queued} 排队</span>}
            {errors > 0 && <span style={{ color: '#e5534b' }}>{errors} 出错</span>}
          </span>
        )}

        {/* 展开/收起 */}
        <button
          onClick={() => setExpanded(!expanded)}
          style={{
            width: 24,
            height: 24,
            borderRadius: '50%',
            border: 'none',
            background: 'rgba(255,255,255,0.1)',
            color: '#a4adbd',
            fontSize: 11,
            cursor: 'pointer',
            display: 'grid',
            placeItems: 'center',
            transition: 'all 0.2s',
            transform: expanded ? 'rotate(180deg)' : 'none',
          }}
          title={expanded ? '收起' : '展开详情'}
        >
          ↑
        </button>

        {/* 恢复主窗口 */}
        <button
          onClick={() => Sidecar.toggleMiniWindow(false)}
          style={{
            width: 24,
            height: 24,
            borderRadius: '50%',
            border: 'none',
            background: 'rgba(255,255,255,0.1)',
            color: '#a4adbd',
            fontSize: 11,
            cursor: 'pointer',
            display: 'grid',
            placeItems: 'center',
          }}
          title="展开主窗口"
        >
          ⤢
        </button>
      </div>

      {/* 展开详情面板（带动画） */}
      <div
        style={{
          background: 'rgba(15, 17, 26, 0.92)',
          borderRadius: '0 0 20px 20px',
          border: '1px solid rgba(255,255,255,0.1)',
          borderTop: '1px solid rgba(255,255,255,0.06)',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          maxHeight: expanded ? 220 : 0,
          overflow: 'hidden',
          transition: 'max-height 0.3s cubic-bezier(0.4, 0, 0.2, 1), opacity 0.25s',
          opacity: expanded ? 1 : 0,
        }}
      >
        <div style={{ padding: '12px 16px' }}>
          {status.agents.map((a) => (
            <div
              key={a.id}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                padding: '8px 0',
                borderBottom: '1px solid rgba(255,255,255,0.05)',
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  background: a.status === 'error' ? '#e5534b' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                  boxShadow: a.status === 'running' ? `0 0 8px ${a.color}` : 'none',
                  animation: a.status === 'running' ? 'pulse 1s infinite' : a.status === 'error' ? 'errorPulse 0.8s infinite' : 'none',
                }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 12, color: '#e8ebf1', fontWeight: 500 }}>{a.name}</div>
                {a.task && (
                  <div style={{ fontSize: 10, color: '#6e7789', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {a.task}
                  </div>
                )}
              </div>
              <span style={{ fontSize: 10, color: statusColor(a.status), fontWeight: 600 }}>
                {statusLabel(a.status)}
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* CSS 动画 */}
      <style>{`
        @keyframes pulse {
          0%, 100% { opacity: 1; }
          50% { opacity: 0.4; }
        }
        @keyframes errorPulse {
          0%, 100% { opacity: 1; box-shadow: 0 0 10px #e5534b; }
          50% { opacity: 0.6; box-shadow: 0 0 20px #e5534b; }
        }
      `}</style>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
