import { StrictMode, useEffect, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { events, Sidecar, type MiniStatus, type MiniAgentStatus } from './mygo'

// 主题跟随主窗：localStorage 同源共享，storage 事件做实时同步。
const THEME_KEY = 'mad-theme'
function applyTheme() {
  const t = localStorage.getItem(THEME_KEY) === 'light' ? 'light' : 'dark'
  document.documentElement.dataset.theme = t
}
applyTheme()
window.addEventListener('storage', (e) => {
  if (e.key === THEME_KEY) applyTheme()
})

// 扩展 MiniAgentStatus 类型
type AgentStatus = MiniAgentStatus & {
  startedAt?: number
  errorMsg?: string
  chars?: number     // 本轮已生成字符数
  lastTool?: string  // 最近一次工具调用名
  blockedReason?: string // 「在等人」的原因
}

type ExtendedStatus = {
  agents: AgentStatus[]
}

function MiniApp() {
  const [status, setStatus] = useState<ExtendedStatus>({ agents: [] })
  const [expanded, setExpanded] = useState(false)
  const [hovered, setHovered] = useState<string | null>(null)
  const [selectedAgent, setSelectedAgent] = useState<string | null>(null)
  const [menu, setMenu] = useState<{ agentId: string; x: number; y: number } | null>(null)
  const [pendingStop, setPendingStop] = useState<string | null>(null)
  const expandedRef = useRef<HTMLDivElement>(null)

  // 所有指标（startedAt / chars / lastTool / errMsg / blockedReason）都来自后端，
  // 前端不再从增量事件里猜 —— 窗口晚开也不丢时长，错误也不会退化成「执行出错」。
  useEffect(() => {
    const unsub = events.miniStatus.on((s) => {
      setStatus({
        agents: s.agents.map((a) => ({
          id: a.id,
          name: a.name,
          color: a.color,
          status: a.status,
          task: a.task,
          startedAt: a.startedAt,
          chars: a.chars,
          lastTool: a.lastTool,
          blockedReason: a.blockedReason,
          errorMsg: a.errMsg,
        })),
      })
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
    return unsub
  }, [])

  const running = status.agents.filter((a) => a.status === 'running').length
  const queued = status.agents.filter((a) => a.status === 'queued').length
  const errors = status.agents.filter((a) => a.status === 'error').length
  const done = status.agents.filter((a) => a.status === 'done').length
  const idle = status.agents.filter((a) => a.status === 'idle').length
  const blocked = status.agents.filter((a) => a.status === 'blocked').length

  // 5 态完整映射（与 AgentView 的 StatusBadge 保持一致）
  const statusColor = (s: string) =>
    s === 'blocked'
      ? 'var(--danger)'
      : s === 'running'
        ? 'var(--warning)'
        : s === 'queued'
          ? 'var(--accent-bright)'
          : s === 'error'
            ? 'var(--danger)'
            : s === 'done'
              ? 'var(--success)'
              : 'var(--text-disabled)'

  const statusLabel = (s: string) =>
    s === 'blocked'
      ? '等你'
      : s === 'running'
        ? '运行中'
        : s === 'queued'
          ? '排队'
          : s === 'error'
            ? '出错'
            : s === 'done'
              ? '完成'
              : '空闲'

  // 按「谁需要你」排序：blocked > error > running > queued > done > idle
  const RANK: Record<string, number> = {
    blocked: 0,
    error: 1,
    running: 2,
    queued: 3,
    done: 4,
    idle: 5,
  }
  const ordered = [...status.agents].sort(
    (a, b) => (RANK[a.status] ?? 9) - (RANK[b.status] ?? 9),
  )
  const MAX_DOTS = 8

  // 格式化运行时间
  const formatElapsed = (startedAt?: number): string => {
    if (!startedAt) return ''
    const secs = Math.floor((Date.now() - startedAt) / 1000)
    if (secs < 60) return `${secs}s`
    const mins = Math.floor(secs / 60)
    if (mins < 60) return `${mins}m`
    return `${Math.floor(mins / 60)}h${mins % 60}m`
  }

  // 单击 = 选中（就地高亮，不隐藏窗口）
  const handleSelect = (agentId: string) => {
    setSelectedAgent((cur) => (cur === agentId ? null : agentId))
  }

  // 双击 / 明确动作 = 跳转主窗
  const handleOpen = (agentId: string) => {
    Sidecar.focusAgent(agentId)
  }

  // 右键 = 弹菜单，不直接执行
  const handleContextMenu = (e: React.MouseEvent, agentId: string) => {
    e.preventDefault()
    e.stopPropagation()
    setMenu({ agentId, x: e.clientX, y: e.clientY })
  }

  // 停止必须二次确认
  const confirmStop = (agentId: string) => {
    Sidecar.stopAgent(agentId)
    setPendingStop(null)
    setMenu(null)
  }

  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    window.addEventListener('click', close)
    window.addEventListener('keydown', close)
    return () => {
      window.removeEventListener('click', close)
      window.removeEventListener('keydown', close)
    }
  }, [menu])

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        // 不要 height: 100% —— FitContent 需要内容自然撑高
        padding: '4px',
        position: 'relative',
      }}
    >
      {/* 拖动手柄：唯一持有 drag 的元素，不绑定任何 onClick */}
      <div
        aria-hidden
        style={{
          position: 'absolute',
          inset: 4,
          zIndex: 0,
          // @ts-ignore CSS 变量
          '--app-region': 'drag',
        } as React.CSSProperties}
      />

      {/* 灵动岛主体 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 16px',
          background: 'var(--island-bg)',
          borderRadius: expanded ? '20px 20px 0 0' : 24,
          boxShadow: '0 12px 40px rgba(0,0,0,0.6), 0 0 0 1px rgba(255,255,255,0.08)',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          border: '1px solid var(--island-border)',
          borderBottom: expanded ? 'none' : undefined,
          height: 52,
          boxSizing: 'border-box',
          transition: 'border-radius 0.3s cubic-bezier(0.34, 1.56, 0.64, 1)',
          position: 'relative',
          zIndex: 1,
          // 内容层显式 no-drag：否则会继承手柄的 drag，事件被吞
          // @ts-ignore CSS 变量
          '--app-region': 'no-drag',
        } as React.CSSProperties}
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
          {ordered.slice(0, MAX_DOTS).map((a) => (
            <button
              key={a.id}
              type="button"
              aria-label={`${a.name} ${statusLabel(a.status)}`}
              title={`${a.name} · ${statusLabel(a.status)}`}
              style={{
                position: 'relative',
                flexShrink: 0,
                display: 'grid',
                placeItems: 'center',
                width: 20,
                height: 20,
                padding: 0,
                border: 'none',
                borderRadius: '50%',
                background: 'transparent',
                cursor: 'pointer',
              }}
              onMouseEnter={() => setHovered(a.id)}
              onMouseLeave={() => setHovered(null)}
              onClick={() => handleSelect(a.id)}
              onDoubleClick={() => handleOpen(a.id)}
              onContextMenu={(e) => handleContextMenu(e, a.id)}
            >
              <span
                style={{
                  width: a.status === 'running' || a.status === 'blocked' ? 12 : 10,
                  height: a.status === 'running' || a.status === 'blocked' ? 12 : 10,
                  borderRadius: '50%',
                  background:
                    a.status === 'error' || a.status === 'blocked' ? 'var(--danger)' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : a.status === 'queued' ? 0.7 : 1,
                  border: a.status === 'queued' ? '1.5px dashed var(--accent-bright)' : 'none',
                  boxSizing: 'border-box',
                  // 选中环：不依赖颜色，避免与状态色打架
                  outline: selectedAgent === a.id ? '2px solid var(--text-secondary)' : 'none',
                  outlineOffset: 2,
                  boxShadow:
                    a.status === 'blocked'
                      ? '0 0 14px var(--danger), 0 0 5px var(--danger)'
                      : a.status === 'running'
                        ? `0 0 12px ${a.color}, 0 0 4px ${a.color}`
                        : a.status === 'error'
                          ? '0 0 10px var(--danger)'
                          : a.status === 'queued'
                            ? `0 0 6px ${a.color}`
                            : 'none',
                  animation:
                    a.status === 'blocked'
                      ? 'attention 1.2s ease-out 3' // 一次性 3 次，之后静止靠角标
                      : a.status === 'running'
                        ? 'breathe 2.6s ease-in-out infinite' // 柔和呼吸，不是闪
                        : 'none',
                  cursor: 'pointer',
                  display: 'block',
                  transition: 'opacity 0.2s, outline-color 0.2s',
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
                    left: '50%',
                    transform: 'translateX(-50%)',
                    background: 'rgba(20, 23, 32, 0.95)',
                    border: '1px solid var(--island-border)',
                    borderRadius: 8,
                    padding: '8px 12px',
                    fontSize: 11,
                    color: 'var(--text-primary)',
                    whiteSpace: 'nowrap',
                    zIndex: 100,
                    boxShadow: '0 4px 16px rgba(0,0,0,0.4)',
                    pointerEvents: 'none',
                    // 向上无空间（窗口仅 52px），改为向下方岛内展开，避开 overflow:hidden
                    top: 'calc(100% + 2px)',
                  }}
                >
                  <div style={{ fontWeight: 600, marginBottom: 2 }}>{a.name}</div>
                  <div style={{ color: statusColor(a.status), fontSize: 10 }}>
                    {statusLabel(a.status)}
                    {a.startedAt && a.status === 'running' && ` · ${formatElapsed(a.startedAt)}`}
                  </div>
                  {a.blockedReason && a.status === 'blocked' && (
                    <div style={{ color: 'var(--danger)', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal' }}>
                      {a.blockedReason}
                    </div>
                  )}
                  {a.errorMsg && a.status === 'error' && (
                    <div style={{ color: 'var(--danger)', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal' }}>
                      {a.errorMsg}
                    </div>
                  )}
                  {a.task && (
                    <div style={{ color: 'var(--text-secondary)', fontSize: 10, marginTop: 4, maxWidth: 200, whiteSpace: 'normal', wordBreak: 'break-all' }}>
                      {a.task}
                    </div>
                  )}
                </div>
              )}
            </button>
          ))}
          {status.agents.length > MAX_DOTS && (
            <button
              type="button"
              onClick={() => setExpanded(true)}
              title={`还有 ${status.agents.length - MAX_DOTS} 个，点击展开`}
              style={{
                fontSize: 10,
                color: 'var(--text-secondary)',
                background: 'transparent',
                border: 'none',
                cursor: 'pointer',
                padding: '2px 4px',
                borderRadius: 4,
              }}
            >
              +{status.agents.length - MAX_DOTS}
            </button>
          )}
        </div>

        {/* 统计：blocked 排最前，因为它需要用户动作 */}
        <span style={{ fontSize: 11, display: 'flex', gap: 6, flexShrink: 0, alignItems: 'center' }}>
          {blocked > 0 && (
            <span
              style={{
                color: 'var(--danger)',
                fontWeight: 700,
                display: 'inline-flex',
                alignItems: 'center',
                gap: 3,
              }}
            >
              <span className="blink">●</span>
              {blocked} 个在等你
            </span>
          )}
          {running > 0 && <span style={{ color: 'var(--warning)' }}>{running} 运行</span>}
          {queued > 0 && <span style={{ color: 'var(--accent-bright)' }}>{queued} 排队</span>}
          {errors > 0 && <span style={{ color: 'var(--danger)' }}>{errors} 错</span>}
          {done > 0 && running === 0 && !blocked && (
            <span style={{ color: 'var(--success)' }}>{done} 完成</span>
          )}
          {idle > 0 && running === 0 && done === 0 && errors === 0 && !blocked && (
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
            background: 'var(--island-border)',
            color: 'var(--text-secondary)',
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
            background: 'var(--island-border)',
            color: 'var(--text-secondary)',
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

      {/* 展开详情面板：grid-template-rows 0fr↔1fr 过渡，无魔数、无过冲 */}
      <div
        style={{
          display: 'grid',
          gridTemplateRows: expanded ? '1fr' : '0fr',
          transition: 'grid-template-rows 0.28s cubic-bezier(0.32, 0.72, 0, 1)',
          // @ts-ignore
          '--app-region': 'no-drag',
        } as React.CSSProperties}
      >
      <div
        ref={expandedRef}
        style={{
          background: 'var(--island-bg, rgba(15, 17, 26, 0.92))',
          borderRadius: '0 0 20px 20px',
          border: '1px solid var(--island-border, rgba(255,255,255,0.1))',
          borderTop: '1px solid var(--island-border, rgba(255,255,255,0.06))',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          overflowY: 'auto',
          overflowX: 'hidden',
          minHeight: 0,
          opacity: expanded ? 1 : 0,
          transition: 'opacity 0.2s ease-out',
          position: 'relative',
          zIndex: 1,
        }}
      >
        <div style={{ padding: '12px 16px' }}>
          {ordered.map((a) => (
            <div
              key={a.id}
              onClick={() => handleSelect(a.id)}
              onDoubleClick={() => handleOpen(a.id)}
              onContextMenu={(e) => handleContextMenu(e, a.id)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                padding: '8px 0',
                borderBottom: '1px solid var(--island-border-subtle)',
                cursor: 'pointer',
                background: selectedAgent === a.id ? 'var(--island-border-subtle)' : 'transparent',
                borderRadius: 6,
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: '50%',
                  background:
                    a.status === 'error' || a.status === 'blocked' ? 'var(--danger)' : a.color,
                  opacity: a.status === 'idle' ? 0.35 : 1,
                  boxShadow: a.status === 'running' ? `0 0 8px ${a.color}` : 'none',
                  animation: a.status === 'running' ? 'breathe 2.6s ease-in-out infinite' : 'none',
                  flexShrink: 0,
                }}
              />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span style={{ fontSize: 12, color: 'var(--text-primary)', fontWeight: 500 }}>{a.name}</span>
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
                {a.blockedReason && a.status === 'blocked' && (
                  <div
                    style={{
                      fontSize: 10,
                      color: 'var(--danger)',
                      marginTop: 2,
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    ⚠ {a.blockedReason} · 点击去处理
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
      </div>

      {/* 右键菜单：停止需二次确认 */}
      {menu && (
        <div
          style={{
            position: 'fixed',
            left: Math.min(menu.x, window.innerWidth - 160),
            top: Math.min(menu.y, window.innerHeight - 80),
            zIndex: 999,
            minWidth: 130,
            padding: 4,
            background: 'rgba(28, 32, 42, 0.98)',
            border: '1px solid var(--island-border)',
            borderRadius: 8,
            boxShadow: '0 8px 24px rgba(0,0,0,0.5)',
            // @ts-ignore
            '--app-region': 'no-drag',
          } as React.CSSProperties}
        >
          <button
            type="button"
            onClick={() => {
              handleOpen(menu.agentId)
              setMenu(null)
            }}
            style={menuItemStyle}
          >
            ⤢ 打开会话
          </button>
          {pendingStop === menu.agentId ? (
            <button
              type="button"
              onClick={() => confirmStop(menu.agentId)}
              style={{ ...menuItemStyle, color: 'var(--danger)', fontWeight: 600 }}
            >
              ⚠ 确认停止？
            </button>
          ) : (
            <button
              type="button"
              onClick={() => setPendingStop(menu.agentId)}
              style={menuItemStyle}
            >
              ■ 停止
            </button>
          )}
        </div>
      )}

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
          background: var(--island-border);
          border-radius: 2px;
        }
        ::-webkit-scrollbar-thumb:hover {
          background: var(--text-disabled);
        }
      `}</style>
    </div>
  )
}

function menuItemStyle(): React.CSSProperties {
  return {
    display: 'block',
    width: '100%',
    padding: '6px 10px',
    textAlign: 'left',
    fontSize: 11,
    color: 'var(--text-primary)',
    background: 'transparent',
    border: 'none',
    borderRadius: 5,
    cursor: 'pointer',
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
