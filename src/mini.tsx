import { StrictMode, useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { events, Sidecar, type MiniStatus } from './mygo'

function MiniApp() {
  const [status, setStatus] = useState<MiniStatus>({ agents: [] })
  const [expanded, setExpanded] = useState(false)

  useEffect(() => {
    const unsub = events.miniStatus.on((s) => setStatus(s))
    // 请求初始状态
    Sidecar.listAgents().then((agents) => {
      setStatus({
        agents: agents.map((a) => ({
          id: a.id,
          name: a.name,
          color: a.color,
          status: a.status,
        })),
      })
    })
    return unsub
  }, [])

  const running = status.agents.filter((a) => a.status === 'running').length
  const queued = status.agents.filter((a) => a.status === 'queued').length

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        height: '100%',
        // @ts-ignore CSS 变量
        '--app-region': 'drag',
        // 整个窗口可拖动
      } as React.CSSProperties}
    >
      {/* 悬浮条主体 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 16px',
          background: 'rgba(20, 23, 32, 0.95)',
          borderRadius: expanded ? '16px 16px 0 0' : 24,
          boxShadow: '0 8px 32px rgba(0,0,0,0.5)',
          backdropFilter: 'blur(16px)',
          border: '1px solid rgba(255,255,255,0.1)',
          borderBottom: expanded ? 'none' : undefined,
          height: 48,
          boxSizing: 'border-box',
          transition: 'border-radius 0.2s',
        }}
      >
        {/* 品牌 */}
        <span style={{ fontSize: 13, fontWeight: 700, color: '#4d8cf0' }}>
          舰队
        </span>

        {/* Agent 状态点 */}
        <div style={{ display: 'flex', gap: 6, alignItems: 'center', flex: 1 }}>
          {status.agents.length === 0 && (
            <span style={{ fontSize: 11, color: '#6e7789' }}>无 agent</span>
          )}
          {status.agents.slice(0, 8).map((a) => (
            <span
              key={a.id}
              title={`${a.name}: ${a.status}`}
              style={{
                width: 10,
                height: 10,
                borderRadius: '50%',
                background: a.color,
                opacity: a.status === 'idle' ? 0.35 : 1,
                boxShadow:
                  a.status === 'running'
                    ? `0 0 8px ${a.color}`
                    : a.status === 'queued'
                      ? `0 0 5px ${a.color}`
                      : 'none',
                animation:
                  a.status === 'running' || a.status === 'queued'
                    ? 'pulse 1.2s infinite'
                    : 'none',
                cursor: 'pointer',
              }}
              onClick={() => {
                // 点击状态点，后续：聚焦主窗口该 agent
              }}
            />
          ))}
          {status.agents.length > 8 && (
            <span style={{ fontSize: 10, color: '#6e7789' }}>
              +{status.agents.length - 8}
            </span>
          )}
        </div>

        {/* 统计 */}
        {(running > 0 || queued > 0) && (
          <span style={{ fontSize: 11, color: '#a4adbd' }}>
            {running > 0 && `${running} 运行`}
            {running > 0 && queued > 0 && ' · '}
            {queued > 0 && `${queued} 排队`}
          </span>
        )}

        {/* 展开/收起 */}
        <button
          onClick={() => setExpanded(!expanded)}
          style={{
            width: 22,
            height: 22,
            borderRadius: '50%',
            border: 'none',
            background: 'rgba(255,255,255,0.12)',
            color: '#a4adbd',
            fontSize: 11,
            cursor: 'pointer',
            display: 'grid',
            placeItems: 'center',
            transition: 'transform 0.2s',
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
            width: 22,
            height: 22,
            borderRadius: '50%',
            border: 'none',
            background: 'rgba(255,255,255,0.12)',
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

      {/* 展开详情面板 */}
      {expanded && (
        <div
          style={{
            background: 'rgba(20, 23, 32, 0.95)',
            borderRadius: '0 0 16px 16px',
            border: '1px solid rgba(255,255,255,0.1)',
            borderTop: '1px solid rgba(255,255,255,0.06)',
            padding: '12px 16px',
            maxHeight: 200,
            overflowY: 'auto',
          }}
        >
          {status.agents.map((a) => (
            <div
              key={a.id}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '6px 0',
                borderBottom: '1px solid rgba(255,255,255,0.05)',
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  background: a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                }}
              />
              <span style={{ fontSize: 12, color: '#e8ebf1', flex: 1 }}>
                {a.name}
              </span>
              <span
                style={{
                  fontSize: 10,
                  color:
                    a.status === 'running'
                      ? '#e8a33d'
                      : a.status === 'queued'
                        ? '#4d8cf0'
                        : '#6e7789',
                }}
              >
                {a.status === 'running'
                  ? '运行中'
                  : a.status === 'queued'
                    ? '排队'
                    : a.status === 'error'
                      ? '出错'
                      : '空闲'}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
