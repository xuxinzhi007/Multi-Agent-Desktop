import type { ChannelMessage, MiniAgentStatus, MiniStatus } from '../mygo'

/** 胶囊只有这六态。颜色和动画都由它决定，不随帧随机改。 */
export type Mode = 'idle' | 'thinking' | 'communicating' | 'collaborating' | 'completed' | 'error'

export type Signal = {
  type:
    | 'message.sent'
    | 'message.received'
    | 'agent.started'
    | 'agent.thinking'
    | 'agent.completed'
    | 'agent.failed'
    | 'task.assigned'
    | 'task.completed'
    | 'task.failed'
  from?: string
  to?: string
  messageId?: string
  agentId?: string
  timestamp: number
}

export type Flight = {
  id: string
  from: string
  to: string
  color: string
}

export type Dot = {
  id: string
  name: string
  color: string
  hot: boolean
  wait: boolean
  run: boolean
  bad: boolean
}

export type Scene = 'idle' | 'thinking' | 'communicating' | 'collaborating' | 'completed' | 'error'

export const PALETTE = ['#54D9FF', '#A78BFA', '#43D9A3', '#F5B85C']
export const GAP = 28
const MAX_FLIGHTS = 2
const MAX_PENDING = 6

export type Model = {
  source: 'live' | 'demo'
  agents: MiniAgentStatus[]
  flights: Flight[]
  pending: Signal[]
  seen: string[]
  hot: string
  hotKey: string
  completedUntil: number
  paused: boolean
  speed: number
  now: number
  seq: number
  count: number
}

export type View = {
  mode: Mode
  dots: Dot[]
  flights: Flight[]
  width: number
  track: number
  span: number
  label: string
  speed: number
  paused: boolean
  hot: string
  hotKey: string
}

export type Action =
  | { type: 'snapshot'; status: MiniStatus; now: number; keepPaused?: boolean }
  | { type: 'signal'; signal: Signal }
  | { type: 'scene'; scene: Scene; count: number; now: number }
  | { type: 'flight-done'; id: string; now: number }
  | { type: 'tick'; now: number }
  | { type: 'pause'; paused: boolean }
  | { type: 'speed'; speed: number }

export function demoAgents(): MiniAgentStatus[] {
  const spec = [
    ['research', 'Research', '研究', PALETTE[0]],
    ['coder', 'Coder', '实现', PALETTE[1]],
    ['review', 'Review', '审查', PALETTE[2]],
    ['ops', 'Ops', '发布', PALETTE[3]],
  ] as const
  return spec.map(([id, name, role, color]) => ({
    id,
    name,
    role,
    color,
    status: 'idle',
    progress: 0,
    chars: 0,
    lastTool: '',
  }))
}

export function initialModel(source: 'live' | 'demo'): Model {
  return {
    source,
    agents: source === 'demo' ? demoAgents() : [],
    flights: [],
    pending: [],
    seen: [],
    hot: '',
    hotKey: '',
    completedUntil: 0,
    paused: false,
    speed: 1,
    now: 0,
    seq: 1,
    count: 3,
  }
}

export function reduce(m: Model, action: Action): Model {
  switch (action.type) {
    case 'pause':
      return { ...m, paused: action.paused }
    case 'speed':
      return { ...m, speed: action.speed }
    case 'tick':
      return { ...m, now: action.now }
    case 'snapshot':
      if (m.source === 'demo') return m
      return applySnapshot(m, action.status, action.now, !!action.keepPaused)
    case 'signal':
      if (m.source === 'live' && action.signal.type !== 'message.sent') return m
      return enqueue(m, action.signal)
    case 'flight-done':
      return finishFlight(m, action.id, action.now)
    case 'scene':
      if (m.source !== 'demo') return m
      return applyScene(m, action.scene, action.count, action.now)
    default:
      return m
  }
}

export function select(m: Model): View {
  const mode = modeOf(m)
  const dots = dotsOf(m, mode)
  const flights = m.flights.filter((f) => dots.some((d) => d.id === f.from) && dots.some((d) => d.id === f.to))
  return {
    mode,
    dots,
    flights,
    width: islandWidth(mode, dots.length),
    track: dots.length <= 1 ? 10 : (dots.length - 1) * GAP + 10,
    span: Math.max(0, dots.length - 1) * GAP,
    label: labelOf(mode, m.agents, flights),
    speed: m.speed,
    paused: m.paused,
    hot: m.hot,
    hotKey: m.hotKey,
  }
}

export function signalsFromStatus(prev: MiniAgentStatus[], next: MiniAgentStatus[], now: number): Signal[] {
  if (prev.length === 0) return []
  const prevBy = new Map(prev.map((a) => [a.id, a]))
  const out: Signal[] = []
  for (const a of next) {
    const was = prevBy.get(a.id)
    if (!was || was.lastTool === a.lastTool) continue
    const tool = (a.lastTool ?? '').trim()
    const hit = /^ask_agent\s+(.+?)(?:…)?$/.exec(tool)
    if (!hit) continue
    const name = hit[1].trim()
    const to = next.find((x) => x.name === name || x.id === name)
    if (!to || to.id === a.id) continue
    out.push({
      type: 'message.sent',
      from: a.id,
      to: to.id,
      messageId: `ask:${a.id}:${to.id}:${tool}`,
      timestamp: now,
    })
  }
  return out
}

export function signalsFromChannel(msg: ChannelMessage): Signal[] {
  const targets = (msg.deliveries ?? []).filter((d) => d.status === 'delivered' && d.agentId && d.agentId !== msg.fromId)
  return targets.slice(0, 3).map((d) => ({
    type: 'message.sent' as const,
    from: msg.fromId,
    to: d.agentId,
    messageId: `${msg.id}:${d.agentId}`,
    timestamp: (msg.at || 0) * 1000 || Date.now(),
  }))
}

function applySnapshot(m: Model, status: MiniStatus, now: number, keepPaused: boolean): Model {
  const sigs = signalsFromStatus(m.agents, status.agents ?? [], now)
  const prevActive = countActive(m.agents)
  const nextAgents = status.agents ?? []
  const nextActive = countActive(nextAgents)
  const failed = nextAgents.some((a) => a.status === 'error')
  const done = nextAgents.some((a) => a.status === 'done')
  let completedUntil = m.completedUntil
  if (prevActive > 0 && nextActive === 0 && done && !failed) completedUntil = now + 2200
  if (nextActive > 0 || failed) completedUntil = 0
  let next: Model = {
    ...m,
    agents: nextAgents,
    paused: keepPaused ? m.paused : status.paused,
    completedUntil,
    now,
  }
  for (const sig of sigs) next = enqueue(next, sig)
  return next
}

function applyScene(m: Model, scene: Scene, count: number, now: number): Model {
  const n = clamp(count, 1, 4)
  let next: Model = {
    ...m,
    agents: m.agents.map((a) => ({ ...a, status: 'idle', lastTool: '', chars: 0 })),
    flights: [],
    pending: [],
    hot: '',
    hotKey: '',
    completedUntil: 0,
    now,
    count: n,
    seen: [],
  }
  const ids = next.agents.map((a) => a.id)
  if (scene === 'idle') return next
  if (scene === 'thinking') {
    next.agents = patch(next.agents, ids[0], { status: 'running', chars: 0 })
    return next
  }
  if (scene === 'error') {
    next.agents = patch(next.agents, ids[0], { status: 'error' })
    return next
  }
  if (scene === 'completed') {
    const k = clamp(n, 1, 4)
    for (let i = 0; i < k; i++) next.agents = patch(next.agents, ids[i], { status: 'done', progress: 100 })
    next.completedUntil = now + 2200
    return next
  }
  if (scene === 'communicating') {
    next.agents = patch(next.agents, ids[0], { status: 'running' })
    next.agents = patch(next.agents, ids[1], { status: 'running' })
    return enqueue(next, {
      type: 'message.sent',
      from: ids[0],
      to: ids[1],
      messageId: `demo-comm-${now}`,
      timestamp: now,
    })
  }
  const k = clamp(n, 2, 4)
  for (let i = 0; i < k; i++) next.agents = patch(next.agents, ids[i], { status: 'running' })
  next = enqueue(next, {
    type: 'message.sent',
    from: ids[0],
    to: ids[1],
    messageId: `demo-col-${now}-0`,
    timestamp: now,
  })
  if (k > 2) {
    next = enqueue(next, {
      type: 'message.sent',
      from: ids[1],
      to: ids[2],
      messageId: `demo-col-${now}-1`,
      timestamp: now,
    })
  }
  if (k > 3) {
    next = enqueue(next, {
      type: 'message.sent',
      from: ids[2],
      to: ids[3],
      messageId: `demo-col-${now}-2`,
      timestamp: now,
    })
  }
  return next
}

function enqueue(m: Model, sig: Signal): Model {
  if (sig.type !== 'message.sent') return m
  const id = sig.messageId || `msg_${m.seq}`
  if (m.seen.indexOf(id) >= 0) return m
  const from = resolveId(m.agents, sig.from)
  const to = resolveId(m.agents, sig.to)
  if (!from || !to || from === to) return m
  const seen = m.seen.length >= 40 ? m.seen.slice(m.seen.length - 31) : m.seen.slice()
  seen.push(id)
  const flight: Flight = { id, from, to, color: colorOf(m.agents, from) }
  if (m.flights.length >= MAX_FLIGHTS) {
    if (m.pending.length >= MAX_PENDING) return { ...m, seen }
    return { ...m, seen, pending: [...m.pending, { ...sig, messageId: id, from, to }] }
  }
  return { ...m, seen, seq: m.seq + 1, flights: [...m.flights, flight] }
}

function finishFlight(m: Model, id: string, now: number): Model {
  const done = m.flights.find((f) => f.id === id)
  if (!done) return m
  let next: Model = {
    ...m,
    now,
    flights: m.flights.filter((f) => f.id !== id),
    hot: done.to,
    hotKey: `${done.to}:${m.seq}`,
    seq: m.seq + 1,
  }
  if (next.flights.length < MAX_FLIGHTS && next.pending.length > 0) {
    const sig = next.pending[0]
    next = { ...next, pending: next.pending.slice(1) }
    next = enqueue({ ...next, seen: next.seen.filter((s) => s !== sig.messageId) }, sig)
  }
  return next
}

function modeOf(m: Model): Mode {
  const agents = m.agents
  if (agents.some((a) => a.status === 'error')) return 'error'
  const active = countActive(agents)
  const traffic = m.flights.length + m.pending.length
  if (traffic > 1 || (traffic > 0 && active >= 3)) return 'collaborating'
  if (traffic === 1) return 'communicating'
  if (active >= 2) return 'collaborating'
  if (active === 1) return 'thinking'
  if (m.completedUntil > m.now) return 'completed'
  return 'idle'
}

function dotsOf(m: Model, mode: Mode): Dot[] {
  if (mode === 'idle') return []
  const want = new Set<string>()
  for (const f of m.flights) {
    want.add(f.from)
    want.add(f.to)
  }
  for (const sig of m.pending) {
    if (sig.from) want.add(sig.from)
    if (sig.to) want.add(sig.to)
  }
  const pool = m.agents.filter((a) => {
    if (want.has(a.id)) return true
    if (mode === 'completed') return a.status === 'done'
    if (mode === 'error') return a.status === 'error' || isActive(a.status)
    return isActive(a.status)
  })
  pool.sort((a, b) => {
    const ar = want.has(a.id) ? -1 : rank(a.status)
    const br = want.has(b.id) ? -1 : rank(b.status)
    return ar - br
  })
  return pool.slice(0, 4).map((a, i) => ({
    id: a.id,
    name: a.name,
    color: a.color || PALETTE[i % PALETTE.length],
    hot: m.hot === a.id,
    wait: a.status === 'blocked' || a.status === 'queued',
    run: a.status === 'running',
    bad: a.status === 'error',
  }))
}

function labelOf(mode: Mode, agents: MiniAgentStatus[], flights: Flight[]): string {
  const nameOf = (id?: string) => agents.find((a) => a.id === id)?.name ?? ''
  let label = '空闲'
  if (mode === 'thinking') label = `${nameOf(agents.find((a) => isActive(a.status))?.id) || 'Agent'} 思考中`
  if (mode === 'communicating') {
    const f = flights[0]
    const a = nameOf(f?.from)
    const b = nameOf(f?.to)
    label = a && b ? `${a} 发给 ${b}` : 'Agent 通信'
  }
  if (mode === 'collaborating') {
    const n = agents.filter((a) => isActive(a.status)).length
    label = `${Math.max(n, 2)} 个 Agent 协作中`
  }
  if (mode === 'completed') label = '任务完成'
  if (mode === 'error') {
    const n = agents.filter((a) => a.status === 'error').length
    label = n > 0 ? `${n} 个 Agent 异常` : '任务异常'
  }
  if (agents.some((a) => a.status === 'blocked')) label += '，有 Agent 在等待'
  return label
}

function islandWidth(mode: Mode, dots: number): number {
  if (mode === 'idle') return 180
  let w = 128 + Math.max(dots, 1) * 24
  if (mode === 'communicating' || mode === 'collaborating') w += 24
  if (mode === 'completed' || mode === 'error') w += 8
  if (w < 188) w = 188
  if (w > 300) w = 300
  return w
}

function countActive(agents: MiniAgentStatus[]): number {
  return agents.filter((a) => isActive(a.status)).length
}

function isActive(status: string): boolean {
  return status === 'running' || status === 'queued' || status === 'blocked'
}

function rank(status: string): number {
  if (status === 'error') return 0
  if (status === 'running') return 1
  if (status === 'blocked' || status === 'queued') return 2
  if (status === 'done') return 3
  return 9
}

function resolveId(agents: MiniAgentStatus[], key?: string): string {
  if (!key) return ''
  const hit = agents.find((a) => a.id === key || a.name === key)
  return hit ? hit.id : ''
}

function colorOf(agents: MiniAgentStatus[], id: string): string {
  const i = agents.findIndex((a) => a.id === id)
  const c = i >= 0 ? agents[i].color : ''
  if (c) return c
  return PALETTE[(i < 0 ? 0 : i) % PALETTE.length]
}

function patch(agents: MiniAgentStatus[], id: string, part: Partial<MiniAgentStatus>): MiniAgentStatus[] {
  return agents.map((a) => (a.id === id ? { ...a, ...part } : a))
}

function clamp(n: number, lo: number, hi: number): number {
  if (n < lo) return lo
  if (n > hi) return hi
  return n
}
