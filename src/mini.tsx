import { StrictMode, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { events, Sidecar, type MiniStatus, type MiniAgentStatus } from './mygo'

// 扩展 MiniAgentStatus 类型
type AgentStatus = MiniAgentStatus & {
  startedAt?: number
  errorMsg?: string
  chars?: number   // 本轮已生成字符数
  lastTool?: string // 最近一次工具调用名
}

type ExtendedStatus = {
  agents: AgentStatus[]
}

function MiniApp() {
  const [status, setStatus] = useState<ExtendedStatus>({ agents: [] })
  const [expanded, setExpanded] = useState(false)
  const [hovered, setHovered] = useState<string | null>(null)
  const [selectedAgent, setSelectedAgent] = useState<string | null>(null)
  const expandedRef = useRef<HTMLDivElement>(null)

  // 记录任务开始时间、流式增量与最近一次工具调用
  const startTimes = useRef<Record<string, number>>({})
  const charCounts = useRef<Record<string, number>>({})
  const lastTools = useRef<Record<string, string>>({})

  useEffect(() => {
    const unsub = events.miniStatus.on((s) => {
      const now = Date.now()
      const agents: AgentStatus[] = s.agents.map((a) => {
        const prev = status.agents.find((x) => x.id === a.id)
        // 记录开始时间
        if (a.status === 'running' && !startTimes.current[a.id]) {
          startTimes.current[a.id] = now
        }
        // 任务结束清除时间
        if (a.status !== 'running' && startTimes.current[a.id]) {
          delete startTimes.current[a.id]
        }
        return {
          ...a,
          startedAt: startTimes.current[a.id],
          errorMsg: a.status === 'error' ? prev?.errorMsg || '执行出错' : undefined,
          chars: charCounts.current[a.id] || 0,
          lastTool: lastTools.current[a.id] || '',
        }
      })
      setStatus({ agents })
    })

    // 订阅流式输出：只统计字数，不保留截断文本（截断必然落在句子中间）
    const unsubDelta = events.agentDelta.on(({ id, delta }) => {
      charCounts.current[id] = (charCounts.current[id] || 0) + delta.length
    })

    // 订阅工具调用：显示「正在做什么」的结构化字段
    const unsubTool = events.toolStart.on((e) => {
      lastTools.current[e.agentId] = e.tool
    })

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
    return () => {
      unsub()
      unsubDelta()
      unsubTool()
    }
  }, [])

  // 展开时调整窗口高度
  useEffect(() => {
    const height = expanded ? 52 + Math.min(status.agents.length * 44 + 24, 320) : 52
    Sidecar.resizeMiniWindow(height)
  }, [expanded, status.agents.length])

  const running = status.agents.filter((a) => a.status === 'running').length
  const queued = status.agents.filter((a) => a.status === 'queued').length
  const errors = status.agents.filter((a) => a.status === 'error').length
  const done = status.agents.filter((a) => a.status === 'done').length
  const idle = status.agents.filter((a) => a.status === 'idle').length

  // 5 态完整映射（与 AgentView 的 StatusBadge 保持一致）
  const statusColor = (s: string) =>
    s === 'running'
      ? 'var(--warning)'
      : s === 'queued'
        ? 'var(--accent-bright)'
        : s === 'error'
          ? 'var(--danger)'
          : s === 'done'
            ? 'var(--success)'
            : 'var(--text-disabled)'

  const statusLabel = (s: string) =>
    s === 'running'
      ? '运行中'
      : s === 'queued'
        ? '排队'
        : s === 'error'
          ? '出错'
          : s === 'done'
            ? '完成'
            : '空闲'

  // 格式化运行时间
  const formatElapsed = (startedAt?: number): string => {
    if (!startedAt) return ''
    const secs = Math.floor((Date.now() - startedAt) / 1000)
    if (secs < 60) return `${secs}s`
    const mins = Math.floor(secs / 60)
    if (mins < 60) return `${mins}m`
    return `${Math.floor(mins / 60)}h${mins % 60}m`
  }

  // 双击状态点：聚焦主窗口并切换到该 agent
  const handleAgentClick = (agentId: string) => {
    setSelectedAgent(agentId)
    Sidecar.focusAgent(agentId)
  }

  // 右键菜单
  const handleContextMenu = (e: React.MouseEvent, agentId: string) => {
    e.preventDefault()
    // 简化：右键停止 agent
    Sidecar.stopAgent(agentId)
  }

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
          transition: 'border-radius 0.3s cubic-bezier(0.34, 1.56, 0.64, 1)',
          position: 'relative',
        }}
      >
        {/* 品牌 */}
        <span style={{ fontSize: 13, fontWeight: 700, color: 'var(--accent-bright)', letterSpacing: '-0.02em' }}>
          舰队
        </span>

        {/* Agent 状态点 */}
        <div style={{ display: 'flex', gap: 6, alignItems: 'center', flex: 1, overflow: 'hidden' }}>
          {status.agents.length === 0 && (
            <span style={{ fontSize: 11, color: 'var(--text-disabled)' }}>无 agent</span>
          )}
          {status.agents.slice(0, 8).map((a) => (
            <div
              key={a.id}
              style={{ position: 'relative', flexShrink: 0 }}
              onMouseEnter={() => setHovered(a.id)}
              onMouseLeave={() => setHovered(null)}
              onClick={() => handleAgentClick(a.id)}
              onDoubleClick={() => handleAgentClick(a.id)}
              onContextMenu={(e) => handleContextMenu(e, a.id)}
            >
              <span
                style={{
                  width: a.status === 'running' ? 12 : 10,
                  height: a.status === 'running' ? 12 : 10,
                  borderRadius: '50%',
                  background: a.status === 'error' ? 'var(--danger)' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                  boxShadow:
                    a.status === 'running'
                      ? `0 0 12px ${a.color}, 0 0 4px ${a.color}`
                      : a.status === 'error'
                        ? '0 0 10px var(--danger)'
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
                    background: 'var(--danger)',
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
                  <div style={{ color: statusColor(a.status), fontSize: 10 }}>
                    {statusLabel(a.status)}
                    {a.startedAt && a.status === 'running' && ` · ${formatElapsed(a.startedAt)}`}
                  </div>
                  {a.errorMsg && a.status === 'error' && (
                    <div style={{ color: 'var(--danger)', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal' }}>
                      {a.errorMsg}
                    </div>
                  )}
                  {a.task && (
                    <div style={{ color: '#a4adbd', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal', wordBreak: 'break-all' }}>
                      {a.task}
                    </div>
                  )}
                </div>
              )}
            </div>
          ))}
          {status.agents.length > 8 && (
            <span style={{ fontSize: 10, color: 'var(--text-disabled)' }}>+{status.agents.length - 8}</span>
          )}
        </div>

        {/* 统计 */}
        <span style={{ fontSize: 11, color: '#a4adbd', display: 'flex', gap: 6, flexShrink: 0 }}>
          {running > 0 && <span style={{ color: 'var(--warning)' }}>{running} 运行</span>}
          {queued > 0 && <span style={{ color: 'var(--accent-bright)' }}>{queued} 排队</span>}
          {errors > 0 && <span style={{ color: 'var(--danger)' }}>{errors} 错</span>}
          {done > 0 && running === 0 && <span style={{ color: 'var(--success)' }}>{done} 完成</span>}
          {idle > 0 && running === 0 && done === 0 && errors === 0 && (
            <span style={{ color: 'var(--text-disabled)' }}>{idle} 空闲</span>
          )}
        </span>

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
            transition: 'all 0.3s cubic-bezier(0.34, 1.56, 0.64, 1)',
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

      {/* 展开详情面板（可滚动） */}
      <div
        ref={expandedRef}
        style={{
          background: 'rgba(15, 17, 26, 0.92)',
          borderRadius: '0 0 20px 20px',
          border: '1px solid rgba(255,255,255,0.1)',
          borderTop: '1px solid rgba(255,255,255,0.06)',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          maxHeight: expanded ? 280 : 0,
          overflowY: 'auto',
          overflowX: 'hidden',
          transition: 'max-height 0.3s cubic-bezier(0.34, 1.56, 0.64, 1), opacity 0.25s',
          opacity: expanded ? 1 : 0,
          // @ts-ignore
          '--app-region': 'no-drag',
        } as React.CSSProperties}
      >
        <div style={{ padding: '12px 16px' }}>
          {status.agents.map((a) => (
            <div
              key={a.id}
              onClick={() => handleAgentClick(a.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                padding: '8px 0',
                borderBottom: '1px solid rgba(255,255,255,0.05)',
                cursor: 'pointer',
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  background: a.status === 'error' ? 'var(--danger)' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                  boxShadow: a.status === 'running' ? `0 0 8px ${a.color}` : 'none',
                  animation: a.status === 'running' ? 'pulse 1s infinite' : a.status === 'error' ? 'errorPulse 0.8s infinite' : 'none',
                  flexShrink: 0,
                }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span style={{ fontSize: 12, color: '#e8ebf1', fontWeight: 500 }}>{a.name}</span>
                  {a.startedAt && a.status === 'running' && (
                    <span style={{ fontSize: 10, color: 'var(--text-disabled)' }}>
                      {formatElapsed(a.startedAt)}
                    </span>
                  )}
                </div>
                {a.task && (
                  <div style={{ fontSize: 10, color: 'var(--text-disabled)', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {a.task}
                  </div>
                )}
                {a.status === 'running' && (a.lastTool || (a.chars ?? 0) > 0) && (
                  <div
                    className="mono"
                    style={{
                      fontSize: 10,
                      color: 'var(--text-disabled)',
                      marginTop: 2,
                      display: 'flex',
                      gap: 8,
                      overflow: 'hidden',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {a.lastTool && <span>🔧 {a.lastTool}</span>}
                    {(a.chars ?? 0) > 0 && <span>{a.chars} 字</span>}
                  </div>
                )}
                {a.errorMsg && a.status === 'error' && (
                  <div style={{ fontSize: 10, color: 'var(--danger)', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {a.errorMsg}
                  </div>
                )}
              </div>
              <span style={{ fontSize: 10, color: statusColor(a.status), fontWeight: 600, flexShrink: 0 }}>
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
          0%, 100% { opacity: 1; box-shadow: 0 0 10px var(--danger); }
          50% { opacity: 0.6; box-shadow: 0 0 20px var(--danger); }
        }
        /* 细滚动条 */
        ::-webkit-scrollbar {
          width: 4px;
        }
        ::-webkit-scrollbar-track {
          background: transparent;
        }
        ::-webkit-scrollbar-thumb {
          background: rgba(255,255,255,0.1);
          border-radius: 2px;
        }
        ::-webkit-scrollbar-thumb:hover {
          background: rgba(255,255,255,0.2);
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
