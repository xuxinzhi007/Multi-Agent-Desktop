import { StrictMode, useEffect, useMemo, useReducer, useRef } from 'react'
import { createRoot } from 'react-dom/client'
import './styles/island.css'
import { API, events, type AgentView, type MiniStatus } from './mygo'
import { Island } from './island/Island'
import { initialModel, reduce, select, signalsFromChannel, type Scene } from './island/logic'

/**
 * 多 Agent 胶囊岛。
 *
 * 窗口始终是一颗胶囊。思考、通信、协作、完成、异常都画在岛里面。
 * 信封只来自两类真实事件：ask_agent 的 lastTool 变化，以及频道投递。
 * `?demo=1` 才出现调试条，调试条不属于岛。
 */

function MiniApp() {
  const demo = useMemo(() => new URLSearchParams(location.search).has('demo'), [])
  const [model, dispatch] = useReducer(reduce, demo ? 'demo' : 'live', initialModel)
  const view = select(model)
  const gen = useRef(0)

  useEffect(() => {
    if (view.mode !== 'completed') return
    const left = model.completedUntil - Date.now()
    if (left <= 0) return
    const t = window.setTimeout(() => dispatch({ type: 'tick', now: Date.now() }), left + 40)
    return () => window.clearTimeout(t)
  }, [view.mode, model.completedUntil])

  useEffect(() => {
    if (demo) return
    const unsubStatus = events.miniStatus.on((s) => {
      gen.current++
      dispatch({ type: 'snapshot', status: s, now: Date.now() })
    })
    const unsubChannel = events.channelMessage.on((msg) => {
      for (const sig of signalsFromChannel(msg)) dispatch({ type: 'signal', signal: sig })
    })
    const n = ++gen.current
    API.listAgents()
      .then((agents) => {
        if (gen.current !== n) return
        dispatch({ type: 'snapshot', status: fromList(agents), now: Date.now(), keepPaused: true })
      })
      .catch(() => {})
    const refresh = () => {
      const ticket = ++gen.current
      API.listAgents()
        .then((agents) => {
          if (gen.current !== ticket) return
          dispatch({ type: 'snapshot', status: fromList(agents), now: Date.now(), keepPaused: true })
        })
        .catch(() => {})
    }
    const onVis = () => {
      if (document.visibilityState === 'visible') refresh()
    }
    window.addEventListener('focus', refresh)
    document.addEventListener('visibilitychange', onVis)
    return () => {
      unsubStatus()
      unsubChannel()
      window.removeEventListener('focus', refresh)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [demo])

  return (
    <div className={demo ? 'demo-page' : 'mini-page'}>
      <Island
        view={view}
        onHome={() => {
          API.toggleMiniWindow(false).catch(() => {})
        }}
        onFlightDone={(id) => dispatch({ type: 'flight-done', id, now: Date.now() })}
      />
      {demo && <DemoBar modelCount={model.count} paused={model.paused} speed={model.speed} dispatch={dispatch} />}
    </div>
  )
}

function fromList(agents: AgentView[]): MiniStatus {
  return {
    paused: false,
    agents: agents.map((a) => ({
      id: a.id,
      name: a.name,
      role: a.role,
      color: a.color,
      status: a.status,
      progress: 0,
      chars: a.chars ?? 0,
      lastTool: a.lastTool ?? '',
      startedAt: a.startedAt ?? 0,
      blockedReason: a.blockedReason ?? '',
    })),
  }
}

const SCENES: { id: Scene; label: string }[] = [
  { id: 'idle', label: '空闲' },
  { id: 'thinking', label: '思考' },
  { id: 'communicating', label: '通信' },
  { id: 'collaborating', label: '协作' },
  { id: 'completed', label: '完成' },
  { id: 'error', label: '异常' },
]

function DemoBar({
  modelCount,
  paused,
  speed,
  dispatch,
}: {
  modelCount: number
  paused: boolean
  speed: number
  dispatch: (action: Parameters<typeof reduce>[1]) => void
}) {
  const count = useRef(modelCount)
  const speedRef = useRef(speed)
  const pausedRef = useRef(paused)
  const auto = useRef(0)
  const step = useRef(0)
  count.current = modelCount
  speedRef.current = speed
  pausedRef.current = paused

  const go = (scene: Scene) => {
    window.clearInterval(auto.current)
    auto.current = 0
    dispatch({ type: 'scene', scene, count: count.current, now: Date.now() })
  }

  useEffect(() => () => window.clearInterval(auto.current), [])

  return (
    <div className="demo">
      <div className="demo__row">
        {SCENES.map((s) => (
          <button key={s.id} type="button" onClick={() => go(s.id)}>
            {s.label}
          </button>
        ))}
        <button
          type="button"
          onClick={() => dispatch({ type: 'pause', paused: !pausedRef.current })}
        >
          {paused ? '恢复' : '暂停'}
        </button>
        <button
          type="button"
          aria-pressed={auto.current !== 0}
          onClick={(e) => {
            if (auto.current) {
              window.clearInterval(auto.current)
              auto.current = 0
              e.currentTarget.setAttribute('aria-pressed', 'false')
              return
            }
            const order: Scene[] = ['idle', 'thinking', 'communicating', 'collaborating', 'completed', 'error']
            const play = () => {
              if (pausedRef.current) return
              const scene = order[step.current % order.length]
              step.current++
              dispatch({ type: 'scene', scene, count: count.current, now: Date.now() })
            }
            play()
            const ms = Math.max(900, 2600 / speedRef.current)
            auto.current = window.setInterval(play, ms)
            e.currentTarget.setAttribute('aria-pressed', 'true')
          }}
        >
          自动演示
        </button>
      </div>
      <label className="demo__slider">
        速度
        <input
          type="range"
          min={0.5}
          max={2}
          step={0.1}
          defaultValue={1}
          aria-label="动画速度"
          onChange={(e) => dispatch({ type: 'speed', speed: Number(e.target.value) })}
        />
      </label>
      <label className="demo__slider">
        Agent 数量
        <input
          type="range"
          min={1}
          max={4}
          step={1}
          defaultValue={3}
          aria-label="Agent 数量"
          onChange={(e) => {
            count.current = Number(e.target.value)
            dispatch({ type: 'scene', scene: 'collaborating', count: count.current, now: Date.now() })
          }}
        />
      </label>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <MiniApp />
  </StrictMode>,
)
