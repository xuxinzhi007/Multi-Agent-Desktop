import { StrictMode, useEffect, useMemo, useReducer, useRef, useState, type FormEvent } from 'react'
import { createRoot } from 'react-dom/client'
import './styles/island.css'
import { API, events, type AgentView, type MiniAgentStatus, type MiniStatus, type ModelChoice, type ScreenHint } from './mygo'
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
  const [assist, setAssist] = useState<ScreenHint>({ on: false })
  const [draft, setDraft] = useState('')
  const [sayErr, setSayErr] = useState('')
  const [bound, setBound] = useState<{ provider: string; model: string } | null>(null)
  const [focusId, setFocusId] = useState('')
  const [choices, setChoices] = useState<ModelChoice[] | null>(null)
  const [pickerOn, setPickerOn] = useState(false)
  const [rows, setRows] = useState<Row[]>([])
  const speakerRef = useRef('')
  const agentsRef = useRef<MiniAgentStatus[]>([])
  agentsRef.current = model.agents
  const agentRef = useRef('')
  const focus = focusId || model.agents[0]?.id || ''
  agentRef.current = focus
  const focusAgent = model.agents.find((a) => a.id === focus)

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

  useEffect(() => {
    if (demo) return
    return events.screenHint.on(setAssist)
  }, [demo])

  useEffect(() => {
    if (demo) return
    const id = focus
    if (!id) return
    API.getAgentConfig(id)
      .then((cfg) => setBound({ provider: cfg.provider, model: cfg.model }))
      .catch(() => {})
  }, [demo, focus])

  useEffect(() => {
    if (demo) return
    const unsubDelta = events.agentDelta.on(({ id, delta }) => {
      if (!delta) return
      setRows((prev) => upsertAgent(prev, id, agentsRef.current, speakerRef, (row) => ({ ...row, text: row.text + delta, live: true })))
    })
    const unsubReason = events.agentReasoning.on(({ id, reasoning }) => {
      if (!reasoning) return
      setRows((prev) => upsertAgent(prev, id, agentsRef.current, speakerRef, (row) => ({ ...row, reasoning: row.reasoning + reasoning, fold: false, live: true })))
    })
    const unsubMessage = events.agentMessage.on(({ id, message }) => {
      if (message.role !== 'assistant') return
      setFocusId(id)
      const text = showText(message.content)
      setRows((prev) =>
        upsertAgent(prev, id, agentsRef.current, speakerRef, (row) => ({
          ...row,
          text: text || showText(row.text),
          reasoning: message.reasoning || row.reasoning,
          live: false,
          fold: true,
          finalAt: row.finalAt ?? Date.now(),
        })),
      )
    })
    return () => {
      unsubDelta()
      unsubReason()
      unsubMessage()
    }
  }, [demo])

  const floats = rows.filter((r) => r.kind === 'user' || r.kind === 'agent')
  const reply = [...floats].reverse().find((r): r is AgentRow => r.kind === 'agent')
  const hint = !demo && (assist.text || assist.err)
  const [hover, setHover] = useState(false)
  const showRise = floats.length > 0 || !!hint
  const pickerRows = pickerOn ? Math.min(grouped(choices).reduce((n, g) => n + 1 + g.items.length, 0) || 1, 6) : 0
  const extra = Math.min(3, floats.length + (hint && !reply ? 1 : 0)) * 78 + (assist.on ? 58 : 0) + (pickerRows ? pickerRows * 28 + 12 : 0)

  useEffect(() => {
    if (hover) return
    const now = Date.now()
    let soon = Infinity
    const due: string[] = []
    for (const r of rows) {
      if (r.kind === 'hand' || r.leaving) continue
      if (r.kind === 'agent' && r.live) continue
      const at = r.finalAt
      if (!at) continue
      const left = at + holdFor(r.text) - now
      if (left <= 0) due.push(r.id)
      else soon = Math.min(soon, left)
    }
    if (due.length > 0) {
      setRows((prev) => prev.map((r) => (due.includes(r.id) ? { ...r, leaving: true } : r)))
      return
    }
    if (soon === Infinity) return
    const t = window.setTimeout(() => {
      setRows((prev) =>
        prev.map((r) => {
          if (r.kind === 'hand' || r.leaving) return r
          if (r.kind === 'agent' && r.live) return r
          if (!r.finalAt) return r
          return Date.now() >= r.finalAt + holdFor(r.text) ? { ...r, leaving: true } : r
        }),
      )
    }, soon + 40)
    return () => window.clearTimeout(t)
  }, [rows, hover])

  useEffect(() => {
    if (demo) return
    API.layoutMini(extra).catch(() => {})
  }, [demo, extra])

  const togglePicker = () => {
    const id = agentRef.current
    if (!id) {
      setSayErr('还没有会话')
      return
    }
    if (pickerOn) {
      setPickerOn(false)
      return
    }
    setChoices(null)
    setPickerOn(true)
    API.listAllModels()
      .then((models) => setChoices(models))
      .catch((err: unknown) => {
        setPickerOn(false)
        setSayErr(err instanceof Error ? err.message : '读不到模型')
      })
  }

  const pickModel = (choice: ModelChoice) => {
    if (!choice.ready) return
    const id = agentRef.current
    if (!id) return
    API.getAgentConfig(id)
      .then((cfg) =>
        API.setAgentLLM(id, {
          ...cfg,
          provider: choice.provider,
          baseUrl: choice.baseUrl || (choice.provider === cfg.provider ? cfg.baseUrl : ''),
          apiKey: choice.provider === cfg.provider ? cfg.apiKey : '',
          model: choice.model,
        }),
      )
      .then(() => API.getAgentConfig(id))
      .then((cfg) => {
        setBound({ provider: cfg.provider, model: cfg.model })
        setPickerOn(false)
      })
      .catch((err: unknown) => {
        setSayErr(err instanceof Error ? err.message : '换不了模型')
      })
  }

  const send = (e: FormEvent) => {
    e.preventDefault()
    const text = draft.trim()
    if (!text) return
    const id = focus
    if (!id) {
      setSayErr('还没有会话')
      return
    }
    setRows((prev) => trim([...prev, { kind: 'user', id: rid(), text, finalAt: Date.now() }]))
    setDraft('')
    setSayErr('')
    API.promptAgent({ agentId: id, text }).catch((err: unknown) => {
      setSayErr(err instanceof Error ? err.message : '没发出去')
    })
  }

  return (
    <div className={demo ? 'demo-page' : hint ? 'mini-page has-hint' : 'mini-page'}>
      <div className="dock">
      {showRise && (
        <div
          className="rise"
          onMouseEnter={() => setHover(true)}
          onMouseLeave={() => setHover(false)}
        >
          {floats.map((row) =>
            row.kind === 'user' ? (
              <p
                key={row.id}
                className={row.leaving ? 'rise__me is-gone' : 'rise__me'}
                onAnimationEnd={(e) => {
                  if (e.animationName !== 'rise-away') return
                  setRows((prev) => prev.filter((r) => r.id !== row.id))
                }}
              >
                {row.text}
              </p>
            ) : (
              <article
                key={row.id}
                className={row.leaving ? 'rise__card is-gone' : 'rise__card'}
                style={{ ['--accent' as string]: row.color }}
                onAnimationEnd={(e) => {
                  if (e.animationName !== 'rise-away') return
                  setRows((prev) => prev.filter((r) => r.id !== row.id))
                }}
              >
                <header>
                  <i style={{ background: row.color }} />
                  <b>{row.name}</b>
                  {row.live && (
                    <button type="button" onClick={() => API.stopAgent(row.agentId).catch(() => {})}>
                      停止
                    </button>
                  )}
                </header>
                <p>{row.text || (row.live ? '在想' : '')}</p>
              </article>
            ),
          )}
          {!reply && hint && (
            <article className="rise__card" style={{ ['--accent' as string]: 'var(--cyan)' }}>
              <header>
                <i style={{ background: 'var(--cyan)' }} />
                <b>来自{assist.app || '当前窗口'}这一屏的字</b>
                {assist.action && (
                  <button
                    type="button"
                    onClick={() => {
                      API.acceptScreenHint().catch((e: unknown) => {
                        const msg = e instanceof Error ? e.message : '没能开始'
                        setAssist((cur) => ({ ...cur, err: msg, text: '', action: '' }))
                      })
                    }}
                  >
                    执行
                  </button>
                )}
              </header>
              <p>{assist.err || assist.text}</p>
              {assist.action && <p>{assist.action}</p>}
            </article>
          )}
        </div>
      )}
      {assist.on && (
        <p className="eye">
          <b>{watchCopy(assist).title}</b>
          <span>{watchCopy(assist).body}</span>
        </p>
      )}
      <Island
        view={view}
        assistOn={assist.on}
        onToggleAssist={
          demo
            ? undefined
            : () => {
                const next = !assist.on
                setAssist((cur) => ({ ...cur, on: next, text: next ? cur.text : '', action: next ? cur.action : '', err: '' }))
                API.setScreenAssist(next).catch((e: unknown) => {
                    const msg = e instanceof Error ? e.message : '打不开协助'
                    setAssist({ on: false, err: msg })
                  })
              }
        }
        onHome={() => {
          API.toggleMiniWindow(false).catch(() => {})
        }}
        onFlightDone={(id) => dispatch({ type: 'flight-done', id, now: Date.now() })}
      />
      {!demo && pickerOn && (
        <div className="models" role="listbox" aria-label="选择模型">
          {choices === null && <p className="models__vendor">读取中</p>}
          {choices !== null && grouped(choices).length === 0 && <p className="models__vendor">没有可选模型</p>}
          {grouped(choices).map((g) => (
            <div key={g.provider}>
              <div className="models__vendor">{vendorName(g.provider)}</div>
              {g.items.map((m) => (
                <button
                  key={`${m.provider}/${m.model}`}
                  type="button"
                  role="option"
                  aria-selected={!!bound && m.provider === bound.provider && m.model === bound.model}
                  className={bound && m.provider === bound.provider && m.model === bound.model ? 'is-on' : ''}
                  disabled={!m.ready}
                  onClick={() => pickModel(m)}
                >
                  <span>{m.ready ? m.model : `${m.model} · ${m.note || '还不可用'}`}</span>
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
      {!demo && (
        <form className="say" onSubmit={send}>
          <button
            type="button"
            className={pickerOn ? 'model is-on' : 'model'}
            aria-expanded={pickerOn}
            onClick={togglePicker}
            title={focusAgent ? `发给 ${focusAgent.name}` : '选择模型'}
          >
            {chipLabel(focusAgent?.name || '', bound)}
          </button>
          <input
            value={draft}
            placeholder={sayErr || '发一句给会话'}
            aria-label="发给会话的消息"
            onChange={(e) => {
              setDraft(e.target.value)
              if (sayErr) setSayErr('')
            }}
          />
          <button type="submit" disabled={!draft.trim()}>
            发送
          </button>
        </form>
      )}
      </div>
      {demo && <DemoBar modelCount={model.count} paused={model.paused} speed={model.speed} dispatch={dispatch} />}
    </div>
  )
}

type AgentRow = {
  kind: 'agent'
  id: string
  agentId: string
  name: string
  role: string
  color: string
  text: string
  reasoning: string
  live: boolean
  fold: boolean
  finalAt?: number
  leaving?: boolean
}

type Row =
  | AgentRow
  | { kind: 'user'; id: string; text: string; finalAt: number; leaving?: boolean }
  | { kind: 'hand'; id: string; text: string }

let rowSeq = 0
function spoken(text: string) {
  const header = '## 群聊上下文'
  const at = text.lastIndexOf(header)
  if (at < 0) return text.trim()
  const lines = text
    .slice(at + header.length)
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
  const last = lines[lines.length - 1] ?? ''
  const sep = last.indexOf(': ')
  return (sep >= 0 ? last.slice(sep + 2) : last).trim()
}

function showText(text: string) {
  const body = text.startsWith('## 群聊上下文') ? spoken(text) : text.trim()
  return body.replace(/\*\*/g, '')
}

function watchCopy(a: { app?: string; seen?: string; text?: string; busy?: string; err?: string }) {
  const app = a.app || '当前窗口'
  const title = `正在看 ${app}`
  if (a.err) return { title, body: a.err }
  if (a.text) return { title, body: `建议：${a.text}。依据是这一屏上的字。` }
  if (a.busy) return { title, body: '正在根据这一屏上的字想一条建议。' }
  if (a.seen === '画面变了，停一下再读') return { title, body: '画面还在动。停稳后才读这一屏的字。' }
  if (a.seen) return { title, body: `还没有建议。这一屏上先看到的字是「${a.seen}」。` }
  return { title, body: '画面停稳后，读这一屏上的字，再给一条建议。' }
}

function chipLabel(name: string, cfg: { provider: string; model: string } | null) {
  const who = name || '会话'
  if (!cfg?.provider && !cfg?.model) return who
  const local = cfg.provider === 'llamacpp' || cfg.provider === 'ollama'
  const vendor = local ? '本地' : vendorName(cfg.provider)
  return `${who} · ${vendor}${cfg.model ? ' ' + cfg.model : ''}`
}

function holdFor(text: string) {
  const n = Array.from(text).length
  return Math.min(18000, Math.max(3200, 1600 + n * 140))
}

function rid() {
  rowSeq += 1
  return `r${rowSeq}`
}

function trim(rows: Row[]) {
  return rows.length > 40 ? rows.slice(rows.length - 40) : rows
}

function upsertAgent(
  prev: Row[],
  id: string,
  agents: MiniAgentStatus[],
  speaker: { current: string },
  patch: (row: AgentRow) => AgentRow,
) {
  const who = agents.find((a) => a.id === id)
  const name = who?.name || '助手'
  const hit = prev.findIndex((r) => r.kind === 'agent' && r.agentId === id && r.live)
  if (hit >= 0) {
    const cur = prev[hit]
    if (cur.kind !== 'agent') return prev
    const next = prev.slice()
    next[hit] = patch(cur)
    return next
  }
  speaker.current = id
  const blank: AgentRow = {
    kind: 'agent',
    id: rid(),
    agentId: id,
    name,
    role: who?.role || '',
    color: who?.color || '#54d9ff',
    text: '',
    reasoning: '',
    live: true,
    fold: true,
  }
  return trim([...prev, patch(blank)])
}

const VENDORS: Record<string, string> = {
  deepseek: 'DeepSeek',
  qoder: 'Qoder',
  openai: 'OpenAI',
  claude: 'Claude',
  llamacpp: '本地 llama.cpp',
}

function vendorName(id: string) {
  return VENDORS[id] || id
}

function grouped(items: ModelChoice[] | null) {
  if (!items) return []
  const order: string[] = []
  const map = new Map<string, ModelChoice[]>()
  for (const item of items) {
    const bucket = map.get(item.provider)
    if (bucket) bucket.push(item)
    else {
      order.push(item.provider)
      map.set(item.provider, [item])
    }
  }
  return order.map((provider) => ({ provider, items: map.get(provider) ?? [] }))
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
