import { StrictMode, useEffect, useMemo, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import './styles/island.css'
import { events, API, type MiniAgentStatus, type MiniStatus } from './mygo'

/**
 * 多 Agent 灵动岛。
 *
 * 两种形态承载两种意图：
 *   折叠态 —— 瞥一眼。头像堆叠 + 运行/等待/异常计数 + 总进度环。
 *   展开态 —— 操作与详情。逐 agent 卡片、任务、进度、日志时间线、全局控制。
 *
 * 视觉约束见 docs/design.md 与 skill 规格：
 *   深色玻璃、低饱和高亮、状态多通道表达（颜色+圆点+徽章+进度条）。
 */

type ViewStatus = 'running' | 'thinking' | 'waiting' | 'done' | 'error' | 'stopped'

const STATUS_META: Record<ViewStatus, { label: string; color: string; dot: string }> = {
  running: { label: '运行中', color: 'var(--st-running)', dot: 'dot--breath' },
  thinking: { label: '思考中', color: 'var(--st-thinking)', dot: 'dot--breath' },
  waiting: { label: '等待中', color: 'var(--st-waiting)', dot: '' },
  done: { label: '已完成', color: 'var(--st-done)', dot: '' },
  error: { label: '异常', color: 'var(--st-error)', dot: 'dot--fast' },
  stopped: { label: '已停止', color: 'var(--st-stopped)', dot: '' },
}

/** 把后端状态映射到界面状态。后端没有 thinking，用 running+无产出近似。 */
function viewStatus(a: MiniAgentStatus): ViewStatus {
  switch (a.status) {
    case 'running':
      return (a.chars ?? 0) === 0 ? 'thinking' : 'running'
    case 'queued':
      return 'waiting'
    case 'blocked':
      return 'waiting'
    case 'done':
      return 'done'
    case 'error':
      return 'error'
    case 'idle':
      return a.chars ? 'stopped' : 'stopped'
    default:
      return 'stopped'
  }
}

function isActive(s: ViewStatus) {
  return s === 'running' || s === 'thinking' || s === 'waiting'
}

/** 总进度：活跃 agent 的平均进度，全完成则 100。 */
function overallProgress(list: MiniAgentStatus[]): number {
  if (list.length === 0) return 0
  const sum = list.reduce((n, a) => n + (a.progress ?? 0), 0)
  return Math.round(sum / list.length)
}

/**
 * 总进度条/环的颜色。
 *
 * 注意：不要因为有异常就把整条进度染红 —— 那会让人以为「进度本身出问题了」，
 * 而红色进度条读起来像「失败率」。颜色只表达「整体在什么阶段」，
 * 异常的紧迫性交给标题与徽章。只有「全部已停止」才落到灰色。
 */
function ringColor(list: MiniAgentStatus[]): string {
  const vs = list.map(viewStatus)
  if (vs.some((s) => s === 'running' || s === 'thinking')) return 'var(--st-running)'
  if (vs.some((s) => s === 'waiting')) return 'var(--st-waiting)'
  if (vs.some((s) => s === 'done')) return 'var(--st-done)'
  return 'var(--st-stopped)'
}

function MiniApp() {
  const [status, setStatus] = useState<MiniStatus>({ agents: [], paused: false })
  /**
   * 三阶段，而不是一个布尔值。
   *
   * 之前 expanded 直接翻转，内容和容器同帧改样式 —— 收起时容器瞬间变小，
   * 内容还没开始动就被裁掉，看起来是「压缩后弹回」。
   *
   * 现在拆成：
   *   collapsed  折叠
   *   opening    正在展开（容器长大 + 内容淡入）
   *   open       完全展开
   *   closing    正在收起（内容先淡出 + 容器缩小）
   * 收起的最后一刻才真正落回 collapsed。
   */
  type Phase = 'collapsed' | 'opening' | 'open' | 'closing'
  const [phase, setPhase] = useState<Phase>('collapsed')
  const expanded = phase === 'opening' || phase === 'open'
  const timerRef = useRef<number | null>(null)

  const open = () => {
    if (timerRef.current) window.clearTimeout(timerRef.current)
    setPhase('opening')
    timerRef.current = window.setTimeout(() => setPhase('open'), 420)
  }

  const close = () => {
    if (timerRef.current) window.clearTimeout(timerRef.current)
    setPhase('closing')
    // 等容器和内容都收完再落到 collapsed，避免内容被瞬间抽走
    timerRef.current = window.setTimeout(() => setPhase('collapsed'), 400)
  }
  const [openLogs, setOpenLogs] = useState<Record<string, boolean>>({})
  const [toasts, setToasts] = useState<{ id: number; text: string }[]>([])
  const rootRef = useRef<HTMLDivElement>(null)
  const toastSeq = useRef(0)

  // 展开/收起时驱动原生窗口逐帧变化。
  // opened 是「目标态」：opening/open → 目标展开；closing/collapsed → 目标折叠。
  // 注意 closing 时窗口已经开始缩小，但内容还在退场 —— 这正是我们要的同步感。
  useEffect(() => {
    API.islandResize(expanded).catch(() => {})
  }, [expanded])

  useEffect(() => {
    const unsub = events.miniStatus.on((s) => setStatus(s))
    API.listAgents()
      .then((agents) =>
        setStatus((prev) => ({
          agents: agents.map((a) => ({
            id: a.id,
            name: a.name,
            role: a.role,
            color: a.color,
            status: a.status,
            progress: a.status === 'idle' ? 0 : 0,
            chars: a.chars ?? 0,
            startedAt: a.startedAt ?? 0,
            lastTool: a.lastTool ?? '',
            logs: [],
          })),
          paused: prev.paused,
        })),
      )
      .catch(() => {})
    return unsub
  }, [])

  // 点击岛外收起
  useEffect(() => {
    if (!expanded) return
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) close()
    }
    window.addEventListener('mousedown', onDown)
    return () => window.removeEventListener('mousedown', onDown)
  }, [expanded])

  // Esc 收起
  useEffect(() => {
    if (!expanded) return
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && close()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [expanded])

  const toast = (text: string) => {
    const id = ++toastSeq.current
    setToasts((t) => [...t, { id, text }])
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 1800)
  }

  const agents = status.agents ?? []
  const counts = useMemo(() => {
    const vs = agents.map(viewStatus)
    return {
      running: vs.filter((s) => s === 'running' || s === 'thinking').length,
      waiting: vs.filter((s) => s === 'waiting').length,
      error: vs.filter((s) => s === 'error').length,
      done: vs.filter((s) => s === 'done').length,
    }
  }, [agents])

  const progress = overallProgress(agents)

  // 只展示活跃 + 异常的，已完成/已停止的不占展开面板空间
  const visible = useMemo(
    () => agents.filter((a) => isActive(viewStatus(a)) || viewStatus(a) === 'error'),
    [agents],
  )

  const pauseAll = async () => {
    if (status.paused) {
      await API.resumeAll()
      toast('已恢复')
    } else {
      const n = await API.pauseAll()
      toast(n > 0 ? `已暂停 ${n} 个 Agent` : '没有正在运行的任务')
    }
  }

  const stopAll = async () => {
    const n = await API.stopAllAgents()
    toast(n > 0 ? `已停止 ${n} 个 Agent` : '没有可停止的任务')
  }

  return (
    <div
      ref={rootRef}
      className={`island island--${phase === 'opening' || phase === 'open' ? 'expanded' : 'collapsed'} island--phase-${phase}`}
    >
      <div className="island__seam" />

      {/* ---- 折叠态 ---- */}
      <button
        type="button"
        className="island__pill"
        onClick={open}
        aria-label="展开 Agent 协作面板"
      >
        <AvatarStack agents={agents} />
        <div className="island__meta">
          <span className="island__title">
            {counts.error > 0
              ? '有 Agent 需要处理'
              : counts.running > 0
                ? 'Agent 协作中'
                : counts.waiting > 0
                  ? 'Agent 等待中'
                  : agents.length > 0
                    ? '全部就绪'
                    : '还没有 Agent'}
          </span>
          <span className="island__sub">
            <span className="is-run">{counts.running} 运行</span>
            {counts.waiting > 0 && <span className="is-wait">{counts.waiting} 等待</span>}
            {counts.error > 0 && <span className="is-err">{counts.error} 异常</span>}
          </span>
        </div>
        <ProgressRing value={progress} color={ringColor(agents)} size={30} showLabel />
      </button>

      {/* ---- 展开态 ---- */}
      <div className="island__panel">
        <div className="panel__head">
          <span className="panel__title">Agent 协作</span>
          <span className="panel__count">{agents.length}</span>
          {status.paused && (
            <span className="panel__count" style={{ color: 'var(--st-waiting)' }}>
              已暂停
            </span>
          )}
          {/* 唯一的「回到主窗」出口。之前没有这个按钮，
              从中控点进悬浮岛之后就再也回不去了。 */}
          <button
            type="button"
            className="panel__back"
            onClick={() => API.toggleMiniWindow(false)}
            title="回到主窗口"
          >
            ⤢ 主窗
          </button>
          <button
            type="button"
            className="panel__close"
            onClick={close}
            aria-label="收起"
          >
            ✕
          </button>
        </div>

        <div className="panel__bar">
          <div
            className="panel__bar-fill"
            style={{ width: `${progress}%`, background: ringColor(agents) }}
          />
        </div>

        {visible.length === 0 ? (
          <div className="panel__empty">
            {agents.length === 0 ? (
              <>
                还没有 Agent
                <br />
                在主窗口新建一个开始协作
              </>
            ) : (
              <>
                所有 Agent 都已就绪
                <br />
                发出任务后这里会显示进度
              </>
            )}
          </div>
        ) : (
          <div className="panel__list">
            {visible.map((a, i) => (
              <AgentCard
                key={a.id}
                agent={a}
                index={i}
                open={!!openLogs[a.id]}
                onToggle={() =>
                  setOpenLogs((m) => ({ ...m, [a.id]: !m[a.id] }))
                }
              />
            ))}
          </div>
        )}

        <div className="panel__foot">
          <button
            type="button"
            className="btn btn--warn"
            onClick={pauseAll}
            disabled={visible.length === 0}
          >
            {status.paused ? '全部恢复' : '全部暂停'}
          </button>
          <button
            type="button"
            className="btn btn--danger"
            onClick={stopAll}
            disabled={visible.length === 0}
          >
            全部停止
          </button>
        </div>
      </div>

      <div className="toast-layer">
        {toasts.map((t) => (
          <div key={t.id} className="toast">
            {t.text}
          </div>
        ))}
      </div>
    </div>
  )
}

function AvatarStack({ agents }: { agents: MiniAgentStatus[] }) {
  const shown = agents.slice(0, 3)
  if (shown.length === 0) {
    return (
      <div className="avatars">
        <div className="avatar" style={{ background: 'var(--st-stopped)' }}>
          ·
        </div>
      </div>
    )
  }
  return (
    <div className="avatars">
      {shown.map((a) => {
        const vs = viewStatus(a)
        return (
          <div key={a.id} className="avatar" style={{ background: a.color }}>
            {a.name.slice(0, 1).toUpperCase()}
            <span
              className={`avatar__badge ${STATUS_META[vs].dot}`}
              style={{ background: STATUS_META[vs].color }}
            />
          </div>
        )
      })}
    </div>
  )
}

function ProgressRing({
  value,
  color,
  size,
  showLabel,
}: {
  value: number
  color: string
  size: number
  showLabel?: boolean
}) {
  const stroke = 3
  const r = (size - stroke) / 2
  const c = 2 * Math.PI * r
  const offset = c - (Math.min(100, Math.max(0, value)) / 100) * c
  return (
    <div className="ring-wrap" style={{ width: size, height: size }}>
      <svg className="ring" width={size} height={size} aria-hidden>
        <circle
          className="ring__track"
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          strokeWidth={stroke}
        />
        <circle
          className="ring__fill"
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          strokeWidth={stroke}
          stroke={color}
          strokeDasharray={c}
          strokeDashoffset={offset}
        />
      </svg>
      {showLabel && <span className="ring__label">{value}</span>}
    </div>
  )
}

function AgentCard({
  agent,
  index,
  open,
  onToggle,
}: {
  agent: MiniAgentStatus
  index: number
  open: boolean
  onToggle: () => void
}) {
  const vs = viewStatus(agent)
  const meta = STATUS_META[vs]
  const waiting = vs === 'waiting'
  const logs = agent.logs ?? []

  return (
    <div
      className={`card ${open ? 'card--expanded' : ''}`}
      style={{ ['--i' as string]: index }}
    >
      <button
        type="button"
        onClick={onToggle}
        style={{
          display: 'block',
          width: '100%',
          border: 'none',
          background: 'transparent',
          color: 'inherit',
          padding: 0,
          textAlign: 'left',
        }}
        aria-expanded={open}
      >
        <div className="card__top">
          <div className="card__avatar" style={{ background: agent.color }}>
            {agent.name.slice(0, 1).toUpperCase()}
            <span
              className={`avatar__badge ${meta.dot}`}
              style={{
                background: meta.color,
                width: 9,
                height: 9,
                right: -3,
                bottom: -3,
              }}
            />
          </div>
          <div className="card__body">
            <div className="card__name">
              <span
                style={{
                  minWidth: 0,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {agent.name}
              </span>
              <span className="badge" style={{ color: meta.color }}>
                <span className={`badge__dot ${meta.dot}`} />
                {meta.label}
              </span>
            </div>
            <div className="card__role">{agent.role || '通用助手'}</div>
            {agent.task && (
              <div className={`card__task ${waiting ? 'card__task--waiting' : ''}`}>
                {agent.task}
              </div>
            )}
            {waiting && agent.dep && (
              <div className="card__dep">等待上游产出 · {agent.dep}</div>
            )}
            {waiting && !agent.dep && agent.blockedReason && (
              <div className="card__dep">{agent.blockedReason}</div>
            )}
          </div>
          <ProgressRing value={agent.progress ?? 0} color={meta.color} size={26} showLabel />
        </div>

        <div className="card__progress">
          <div className="card__track">
            <div
              className="card__fill"
              style={{ width: `${agent.progress ?? 0}%`, background: meta.color }}
            />
          </div>
          <span className="card__pct">{agent.progress ?? 0}%</span>
        </div>
      </button>

      <div className={`card__logs ${open ? 'card__logs--open' : ''}`}>
        <div className="card__logs-inner">
          {logs.length === 0 && (
            <div className="log-row" style={{ color: '#4b5468' }}>
              暂无日志
            </div>
          )}
          {logs.map((l, i) => (
            <div
              key={i}
              className={`log-row ${l.kind === 'error' ? 'log-row--error' : 'log-row--tool'}`}
            >
              <span className="log-row__time">{fmtTime(l.at)}</span>
              <span style={{ minWidth: 0 }}>{l.text}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function fmtTime(at: number): string {
  if (!at) return '--:--'
  const d = new Date(at * 1000)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
