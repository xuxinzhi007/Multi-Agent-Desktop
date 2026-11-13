import { StrictMode, useEffect, useMemo, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import { events, Sidecar, type MiniStatus } from './mygo'
import AgentRow, { type AgentRowData } from './components/AgentRow'

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

/** 分组顺序：需要你的排最前。 */
const GROUP_ORDER = ['blocked', 'running', 'queued', 'done', 'error', 'idle'] as const
const GROUP_LABEL: Record<string, string> = {
  blocked: 'Needs input',
  running: 'Working',
  queued: 'Queued',
  done: 'Completed',
  error: 'Failed',
  idle: 'Idle',
}

const COLLAPSED_ROWS = 3
const EXPANDED_ROWS = 8

function MiniApp() {
  const [status, setStatus] = useState<MiniStatus>({ agents: [] })
  const [expanded, setExpanded] = useState(false)
  const [selected, setSelected] = useState<string | null>(null)
  const [menu, setMenu] = useState<{ x: number; y: number; agentId: string } | null>(null)
  const [pendingStop, setPendingStop] = useState<string | null>(null)
  const [, tick] = useState(0)

  // 每秒刷新一次年龄显示
  useEffect(() => {
    const id = setInterval(() => tick((n) => n + 1), 1000)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    const unsub = events.miniStatus.on((s) => setStatus(s))
    Sidecar.listAgents()
      .then((agents) =>
        setStatus({
          agents: agents.map((a) => ({
            id: a.id,
            name: a.name,
            color: a.color,
            status: a.status,
            task: '',
          })),
        }),
      )
      .catch(() => {})
    return unsub
  }, [])

  const agents: AgentRowData[] = useMemo(
    () =>
      status.agents.map((a) => ({
        id: a.id,
        name: a.name,
        role: '',
        color: a.color,
        status: a.status,
        blockedReason: a.blockedReason,
        startedAt: a.startedAt,
        chars: a.chars,
        lastTool: a.lastTool,
      })),
    [status.agents],
  )

  const blocked = agents.filter((a) => a.status === 'blocked')
  const running = agents.filter((a) => a.status === 'running')
  const queued = agents.filter((a) => a.status === 'queued')

  // 分组后按「谁需要你」排序，取前 N 行
  const ordered = useMemo(() => {
    const rank = (s: string) => {
      const i = GROUP_ORDER.indexOf(s as (typeof GROUP_ORDER)[number])
      return i === -1 ? 99 : i
    }
    return [...agents].sort((a, b) => rank(a.status) - rank(b.status))
  }, [agents])

  const limit = expanded ? EXPANDED_ROWS + blocked.length : COLLAPSED_ROWS
  const visible = ordered.slice(0, limit)
  const hidden = ordered.length - visible.length

  const handleSelect = (id: string) => {
    setSelected((prev) => (prev === id ? null : id))
  }
  const handleOpen = (id: string) => {
    Sidecar.focusAgent(id)
  }
  const handleContextMenu = (e: React.MouseEvent, id: string) => {
    e.preventDefault()
    setMenu({ x: e.clientX, y: e.clientY, agentId: id })
  }

  // peek 卡：展开时在列表上方显示最紧迫的那一条的完整状态
  const peek = expanded ? (blocked[0] ?? running[0] ?? null) : null

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: 'column',
        // 不要 height: 100% —— FitContent 需要内容自然撑高
        padding: '4px',
        paddingTop: 28, // 顶部 24px 留给拖动手柄
        position: 'relative',
      }}
    >
      {/* 拖动手柄：只有顶部 24px 细条可拖。
          整窗可拖会让第二次按下触发 macOS 的 AppleActionOnDoubleClick（zoom/minimize）。 */}
      <div
        aria-hidden
        style={{
          position: 'absolute',
          top: 4,
          left: 4,
          right: 4,
          height: 24,
          zIndex: 0,
          cursor: 'grab',
          // @ts-ignore CSS 变量
          '--app-region': 'drag',
        } as React.CSSProperties}
      />

      {/* 主体 */}
      <div
        style={{
          position: 'relative',
          zIndex: 1,
          background: 'var(--island-bg)',
          borderRadius: 16,
          boxShadow: '0 12px 40px rgba(0,0,0,0.55), 0 0 0 1px var(--island-border)',
          backdropFilter: 'blur(20px) saturate(180%)',
          WebkitBackdropFilter: 'blur(20px) saturate(180%)',
          border: '1px solid var(--island-border)',
          padding: '6px 0 0',
          overflow: 'hidden',
          // @ts-ignore CSS 变量
          '--app-region': 'no-drag',
        } as React.CSSProperties}
      >
        {/* 顶部计数行：仅在有人等你时显示细节 */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            padding: '0 10px 6px',
            fontSize: 11,
            color: 'var(--text-secondary)',
          }}
        >
          {blocked.length > 0 ? (
            <span style={{ color: 'var(--danger)', fontWeight: 600 }}>
              {blocked.length} 个在等你
            </span>
          ) : (
            <span style={{ color: 'var(--text-disabled)' }}>舰队</span>
          )}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 8, color: 'var(--text-disabled)' }}>
            {running.length > 0 && <span>{running.length} 运行</span>}
            {queued.length > 0 && <span>{queued.length} 排队</span>}
          </span>
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            title={expanded ? '收起' : '展开'}
            style={{
              width: 20,
              height: 20,
              borderRadius: '50%',
              border: 'none',
              background: 'transparent',
              color: 'var(--text-tertiary)',
              fontSize: 10,
              cursor: 'pointer',
              display: 'grid',
              placeItems: 'center',
              transform: expanded ? 'rotate(180deg)' : 'none',
              transition: 'transform 0.2s ease-out',
            }}
          >
            ▲
          </button>
          <button
            type="button"
            onClick={() => Sidecar.toggleMiniWindow(false)}
            title="打开主窗口"
            style={{
              width: 20,
              height: 20,
              borderRadius: '50%',
              border: 'none',
              background: 'transparent',
              color: 'var(--text-tertiary)',
              fontSize: 10,
              cursor: 'pointer',
              display: 'grid',
              placeItems: 'center',
            }}
          >
            ⤢
          </button>
        </div>

        {/* peek 卡：展开时显示最紧迫那条的完整状态 */}
        {peek && (
          <div
            style={{
              margin: '0 8px 6px',
              padding: '7px 9px',
              borderRadius: 8,
              background:
                peek.status === 'blocked' ? 'var(--danger-dim, rgba(229,83,75,0.12))' : 'var(--bg-surface)',
              border: `1px solid ${peek.status === 'blocked' ? 'var(--danger)' : 'var(--island-border)'}`,
            }}
          >
            <div style={{ fontSize: 11, fontWeight: 600, color: peek.color || 'var(--text-primary)' }}>
              {peek.name}
            </div>
            <div
              style={{
                fontSize: 10,
                color: peek.status === 'blocked' ? 'var(--danger)' : 'var(--text-secondary)',
                marginTop: 2,
                wordBreak: 'break-word',
              }}
            >
              {peek.blockedReason || (peek.lastTool ? `正在调用 ${peek.lastTool}` : '正在处理…')}
            </div>
          </div>
        )}

        {/* 列表 */}
        <div style={{ maxHeight: expanded ? 340 : undefined, overflowY: expanded ? 'auto' : 'hidden' }}>
          {visible.length === 0 && (
            <div
              style={{
                padding: '10px 12px 14px',
                fontSize: 11,
                color: 'var(--text-disabled)',
                textAlign: 'center',
              }}
            >
              还没有 agent
            </div>
          )}
          {visible.map((a) => (
            <div
              key={a.id}
              onContextMenu={(e) => handleContextMenu(e, a.id)}
              onDoubleClick={() => handleOpen(a.id)}
            >
              <AgentRow
                agent={a}
                variant="island"
                active={selected === a.id}
                onClick={() => handleSelect(a.id)}
              />
            </div>
          ))}
          {hidden > 0 && (
            <button
              type="button"
              onClick={() => setExpanded(true)}
              style={{
                display: 'block',
                width: '100%',
                padding: '5px 12px',
                fontSize: 10,
                color: 'var(--text-secondary)',
                background: 'transparent',
                border: 'none',
                cursor: 'pointer',
                textAlign: 'left',
              }}
            >
              … 还有 {hidden} 个
            </button>
          )}
        </div>

        {/* 底部：选中项的操作提示 */}
        {selected && (
          <div
            style={{
              display: 'flex',
              gap: 6,
              padding: '6px 10px',
              borderTop: '1px solid var(--island-border-subtle)',
            }}
          >
            <button
              type="button"
              onClick={() => handleOpen(selected)}
              style={{
                flex: 1,
                padding: '4px 8px',
                fontSize: 10,
                borderRadius: 6,
                border: '1px solid var(--island-border)',
                background: 'transparent',
                color: 'var(--text-secondary)',
                cursor: 'pointer',
              }}
            >
              打开会话
            </button>
            <button
              type="button"
              onClick={() => {
                Sidecar.stopAgent(selected).catch(() => {})
                setSelected(null)
              }}
              style={{
                flex: 1,
                padding: '4px 8px',
                fontSize: 10,
                borderRadius: 6,
                border: '1px solid var(--island-border)',
                background: 'transparent',
                color: 'var(--danger)',
                cursor: 'pointer',
              }}
            >
              停止
            </button>
          </div>
        )}
      </div>

      {/* 右键菜单：停止需二次确认 */}
      {menu && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 900 }}
            onClick={() => {
              setMenu(null)
              setPendingStop(null)
            }}
          />
          <div
            style={{
              position: 'fixed',
              left: Math.min(menu.x, window.innerWidth - 150),
              top: Math.min(menu.y, window.innerHeight - 90),
              zIndex: 901,
              minWidth: 130,
              background: 'var(--island-bg)',
              border: '1px solid var(--island-border)',
              borderRadius: 8,
              boxShadow: '0 8px 24px rgba(0,0,0,0.45)',
              overflow: 'hidden',
            }}
          >
            <button
              type="button"
              onClick={() => {
                handleOpen(menu.agentId)
                setMenu(null)
              }}
              style={{
                display: 'block',
                width: '100%',
                padding: '7px 12px',
                fontSize: 11,
                textAlign: 'left',
                background: 'transparent',
                border: 'none',
                borderBottom: '1px solid var(--island-border-subtle)',
                color: 'var(--text-primary)',
                cursor: 'pointer',
              }}
            >
              打开会话
            </button>
            {pendingStop === menu.agentId ? (
              <button
                type="button"
                onClick={() => {
                  Sidecar.stopAgent(menu.agentId).catch(() => {})
                  setMenu(null)
                  setPendingStop(null)
                }}
                style={{
                  display: 'block',
                  width: '100%',
                  padding: '7px 12px',
                  fontSize: 11,
                  textAlign: 'left',
                  background: 'var(--danger-dim, rgba(229,83,75,0.12))',
                  border: 'none',
                  color: 'var(--danger)',
                  cursor: 'pointer',
                  fontWeight: 600,
                }}
              >
                确认停止？
              </button>
            ) : (
              <button
                type="button"
                onClick={() => setPendingStop(menu.agentId)}
                style={{
                  display: 'block',
                  width: '100%',
                  padding: '7px 12px',
                  fontSize: 11,
                  textAlign: 'left',
                  background: 'transparent',
                  border: 'none',
                  color: 'var(--danger)',
                  cursor: 'pointer',
                }}
              >
                停止
              </button>
            )}
          </div>
        </>
      )}
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
