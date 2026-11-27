import { useCallback, useEffect, useRef, useState } from 'react'
import {
  API,
  events,
  type AgentView as Agent,
  type ApprovalView,
  type Channel,
  type ChannelMessage,
  type Config,
  type FileIntentEvent,
  type Note,
  type Room,
  type ShellEvent,
  type ShellResultEvent,
} from '../mygo'
import { linkParents } from './agent-tree'
import { bodyOf, sortAgentMessages, sortChannelMessages } from './message'
import type { ToolCall } from '../components/Inspector'

export type LLMConfig = Config
export type ShellLog = {
  id: string
  command: string
  at: number
  result?: { stdout: string; stderr: string; exitCode: number; timedOut?: boolean }
}

/** 出错时该 agent 已有的消息条数。红条属于这一截之前的历史，不跟最新一句走。 */
export type SessionError = {
  text: string
  after: number
}

type State = {
  connected: boolean
  version: string
  workDir: string
  llm: LLMConfig
  agents: Agent[]
  channels: Channel[]
  feishuRooms: Room[]
  notes: Note[]
  streaming: Record<string, string>
  /** 群里正在生成的回复。不进私聊。 */
  channelStreaming: Record<string, string>
  errors: Record<string, SessionError>
  shell: ShellLog[]
  /** 每个 agent 执行过的命令，按 agentId 归集，直接渲染在对话流里 */
  shellByAgent: Record<string, ShellLog[]>
  tools: ToolCall[]
  /** 这一轮还没落成消息的思考文本，按 agent id */
  reasoning: Record<string, string>
  /** 这一轮模型请求：since 是发出时刻，first 是到首个推理或正文的毫秒，heard 表示先到的是哪一种。 */
  pace: Record<string, { since: number; first?: number; heard?: 'reason' | 'text' }>
  tokenUsage: Record<string, { in: number; out: number; lastIn?: number }>
  /** 正在看的会话。其它会话保留已加载的全文，切回来不用重新拉。 */
  opened: string
  /** 哪些会话还有更早的历史没加载。为真时才显示「加载更早」。 */
  hasOlder: Record<string, boolean>
  /** 已经乐观插入消息、但后端还没回执的 agent */
  pending: Record<string, boolean>
  /** 最近一次停止的原因与进度。「为什么停」必须能说清，不能只显示「已停止」。 */
  stops: Record<string, { reason: string; iter: number; used: number; at: number }>
  /** 等待用户决定的写操作。队列按到达顺序，模态框只展示最前面一张。 */
  approvals: ApprovalView[]
  /**
   * 最近 60 秒的写意图：谁要改哪些文件。
   * 多个会话/多个窗口同时改同一个仓库时，这是"冲突在发生前就能看见"的唯一来源。
   * 只保留近期的，过期由渲染侧按 at 过滤——不做定时清理，避免多余的重渲染。
   */
  intents: FileIntentEvent[]
}

const EMPTY: State = {
  connected: true,
  version: '',
  workDir: '',
  llm: { provider: '', baseUrl: '', apiKey: '', model: '' },
  agents: [],
  channels: [],
  feishuRooms: [],
  notes: [],
  streaming: {},
  channelStreaming: {},
  errors: {},
  shell: [],
  shellByAgent: {},
  tools: [],
  reasoning: {},
  pace: {},
  tokenUsage: {},
  opened: '',
  hasOlder: {},
  pending: {},
  stops: {},
  approvals: [],
  intents: [],
}

const PAGE = 40

function clipSentence(text: string): string {
  const s = bodyOf(text).replace(/\s+/g, ' ').trim()
  const chars = Array.from(s)
  return chars.length > 48 ? chars.slice(0, 48).join('') + '…' : s
}

function lastFrom(messages?: { role: string; content: string }[]): string {
  if (!messages) return ''
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i]
    if (m.role === 'system' || !m.content?.trim()) continue
    return clipSentence(m.content)
  }
  return ''
}

/**
 * 会话切到后台时保留已加载的消息，只补一个 last 摘要。
 *
 * 之前这里会清空 messages，切走再切回来要重新请求，请求回来前是空白页；
 * 后端只给最近 40 条，更早的永久看不到。切会话丢历史是用户第一眼就会撞到的问题，
 * 省这点内存不值得。
 */
function shellAgent<T extends Agent>(a: T): T {
  return { ...a, last: a.last || lastFrom(a.messages) }
}

export function useBackend() {
  const [state, setState] = useState<State>(EMPTY)
  const openedRef = useRef('')
  const pageGen = useRef(0)

  useEffect(() => {
    let alive = true
    const unsubs: (() => void)[] = []
    const deltaGen = new Map<string, number>()
    const reasonGen = new Map<string, number>()
    const channelGen = new Map<string, number>()
    const deltaBuf = new Map<string, { g: number; text: string }>()
    const reasonBuf = new Map<string, { g: number; text: string }>()
    const channelBuf = new Map<string, { g: number; text: string }>()
    const reasonGap = new Set<string>()
    let raf = 0
    let toolSeq = 0

    const schedule = () => {
      if (raf) return
      raf = requestAnimationFrame(() => {
        raf = 0
        if (!alive) return
        const deltas = [...deltaBuf.entries()]
        const reasons = [...reasonBuf.entries()]
        const channelDeltas = [...channelBuf.entries()]
        deltaBuf.clear()
        reasonBuf.clear()
        channelBuf.clear()
        if (deltas.length === 0 && reasons.length === 0 && channelDeltas.length === 0) return
        setState((s) => {
          const streaming = { ...s.streaming }
          const channelStreaming = { ...s.channelStreaming }
          const reasoning = { ...s.reasoning }
          const pace = { ...(s.pace ?? {}) }
          let touchS = false
          let touchC = false
          let touchR = false
          let touchP = false
          const hear = (id: string, kind: 'reason' | 'text') => {
            const row = pace[id]
            if (!row) return
            const first = row.first ?? Date.now() - row.since
            const heard = row.heard === 'text' || kind === 'text' ? 'text' : 'reason'
            if (row.first === first && row.heard === heard) return
            pace[id] = { ...row, first, heard }
            touchP = true
          }
          for (const [id, item] of deltas) {
            if ((deltaGen.get(id) ?? 0) !== item.g) continue
            streaming[id] = (streaming[id] ?? '') + item.text
            touchS = true
            hear(id, 'text')
          }
          for (const [id, item] of channelDeltas) {
            if ((channelGen.get(id) ?? 0) !== item.g) continue
            channelStreaming[id] = (channelStreaming[id] ?? '') + item.text
            touchC = true
          }
          for (const [id, item] of reasons) {
            if ((reasonGen.get(id) ?? 0) !== item.g) continue
            const prev = reasoning[id] ?? ''
            if (prev && reasonGap.has(id)) {
              reasoning[id] = `${prev}\n\n${item.text}`
              reasonGap.delete(id)
            } else {
              reasoning[id] = prev + item.text
            }
            touchR = true
            hear(id, 'reason')
          }
          if (!touchS && !touchR && !touchC && !touchP) return s
          return {
            ...s,
            streaming: touchS ? streaming : s.streaming,
            channelStreaming: touchC ? channelStreaming : s.channelStreaming,
            reasoning: touchR ? reasoning : s.reasoning,
            pace: touchP ? pace : s.pace,
          }
        })
      })
    }

    const pushBuf = (buf: Map<string, { g: number; text: string }>, gen: Map<string, number>, id: string, text: string) => {
      const g = gen.get(id) ?? 0
      const prev = buf.get(id)
      buf.set(id, { g, text: prev && prev.g === g ? prev.text + text : text })
      schedule()
    }

    const dropDeltas = (id: string) => {
      deltaGen.set(id, (deltaGen.get(id) ?? 0) + 1)
      deltaBuf.delete(id)
      channelGen.set(id, (channelGen.get(id) ?? 0) + 1)
      channelBuf.delete(id)
    }

    const dropReason = (id: string) => {
      reasonGen.set(id, (reasonGen.get(id) ?? 0) + 1)
      reasonBuf.delete(id)
      reasonGap.delete(id)
    }

    // 状态落盘只走这一条：事件流和对账都用它。
    //
    // 分开写两份曾经出过事——对账只改 status 不清 streaming，输入框照样显示
    // 「这一轮还在进行」、发送键照样是「停止」，用户看到的就是「完成了还在跑」。
    const applyAgentStatus = (s: State, id: string, status: string, busy?: string): State => ({
      ...s,
      agents: s.agents.map((a) =>
        a.id === id
          ? { ...a, status: status as Agent['status'], busy: status === 'running' ? busy || '' : '' }
          : a,
      ),
      streaming: status === 'running' ? s.streaming : omit(s.streaming, id),
      channelStreaming: status === 'running' ? s.channelStreaming : omit(s.channelStreaming, id),
      reasoning: status === 'running' ? s.reasoning : omit(s.reasoning, id),
      pace: status === 'running' ? { ...(s.pace ?? {}), [id]: s.pace?.[id] ?? { since: Date.now() } } : omit(s.pace, id),
    })

    const sub = <T>(evt: { on: (cb: (p: T) => void) => () => void }, cb: (p: T) => void) => {
      unsubs.push(evt.on((p) => alive && cb(p)))
    }

    // 每个初始请求都必须 catch。
    // 之前的版本没有，于是任何一次失败（含在浏览器里直接打开时
    // 根本没有 mygo runtime）都会变成未处理的 Promise 拒绝，
    // 冒到 ErrorBoundary 把整个应用渲染成错误页。
    // 单个数据源失败应该只让那一块为空，不该让整页崩掉。
    const load = <T,>(p: Promise<T>, apply: (v: T) => void) => {
      p.then((v) => alive && apply(v)).catch(() => {
        /* 保底：这一块留空，其余照常渲染 */
      })
    }

    load(API.listAgents(), (agents) =>
      setState((s) => {
        const opened = s.opened
        return {
          ...s,
          agents: linkParents(agents ?? []).map((a) => {
            if (a.id !== opened) return shellAgent(a)
            const cur = s.agents.find((x) => x.id === a.id)
            return { ...shellAgent(a), messages: cur?.messages ?? [], last: a.last || cur?.last || '' }
          }),
        }
      }),
    )
    load(API.listChannels(), (channels) =>
      setState((s) => ({
        ...s,
        channels: (channels ?? []).map((ch) => ({ ...ch, messages: sortChannelMessages(ch.messages ?? []) })),
      })),
    )
    load(API.queryNotes({ Scope: '', Tags: [], Text: '', Limit: 0 }), (notes) =>
      setState((s) => ({ ...s, notes: notes ?? [] })),
    )
    load(API.getLLMConfig(), (llm) => setState((s) => ({ ...s, llm })))
    load(API.getWorkDir(), (workDir) => setState((s) => ({ ...s, workDir })))
    // 审批队列要补一次状态：界面刷新或后端已经卡在等审批时，不能靠事件补历史。
    const pullApprovals = () =>
      load(API.listApprovals(), (list) => setState((s) => ({ ...s, approvals: list ?? [] })))
    pullApprovals()

    const pullAgents = () =>
      load(API.listAgents(), (agents) =>
        setState((s) => ({
          ...s,
          agents: linkParents(agents ?? []).map((a) => {
            const cur = s.agents.find((x) => x.id === a.id)
            return { ...shellAgent(a), messages: cur?.messages ?? [], last: a.last || cur?.last || '' }
          }),
        })),
      )

    // 多端对账：桌面端处理掉一张卡时，网页端靠 `tool.approval.done` 收卡；
    // 但 SSE 断线/重连期间的事件没有补发（服务端不做缓冲，也没有 Last-Event-ID），
    // 于是网页会一直显示一张已失效的卡，点下去报「审批已失效或已完成」。
    //
    // 三条对账路径，成本都很低、都不做定时轮询：
    //   1) 网关每次连上（含重连）发 `gateway.connected` → 全量覆盖；
    //   2) 窗口重新获得焦点 / 从后台切回可见 → 全量覆盖（跨端操作后的主路径）；
    //   3) 决定失败且原因是「已失效」→ 单张收卡（见 decideApproval）。
    const resync = () => {
      pullApprovals()
      pullAgents()
    }

    // 状态对账：事件是主路径，但断线窗口里丢一条 idle，界面就永久停在「执行中」——
    // 进程里早已回 idle，用户看到的是「活干完了还在跑」（known-issues #25）。
    // 8 秒一次、只取 id+status+busy，14 个会话不到 1KB；页面不可见时跳过。
    const syncStates = () => {
      if (document.visibilityState !== 'visible') return
      API.agentStates()
        .then((list) => {
          if (!alive || !list) return
          setState((s) => {
            let next = s
            for (const st of list) {
              const cur = s.agents.find((a) => a.id === st.id)
              if (!cur || (cur.status === st.status && (cur.busy ?? '') === (st.busy ?? ''))) continue
              if (st.status !== 'running') {
                dropDeltas(st.id)
                dropReason(st.id)
              }
              next = applyAgentStatus(next, st.id, st.status, st.busy)
            }
            return next
          })
        })
        .catch(() => {})
    }
    const syncTimer = window.setInterval(syncStates, 8000)
    unsubs.push(() => window.clearInterval(syncTimer))
    const rawOn = (globalThis as { mygo?: { on?: (n: string, f: () => void) => () => void } }).mygo?.on
    if (rawOn) unsubs.push(rawOn('gateway.connected', resync))

    const onFocus = () => resync()
    const onVisible = () => {
      if (document.visibilityState === 'visible') resync()
    }
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVisible)
    unsubs.push(() => {
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVisible)
    })

    sub(events.agentCreated, (a) =>
      setState((s) => (s.agents.some((x) => x.id === a.id) ? s : { ...s, agents: [...s.agents, shellAgent(a)] })),
    )
    sub(events.agentProfile, (a) =>
      setState((s) => ({
        ...s,
        agents: s.agents.map((x) =>
          x.id === a.id
            ? {
                ...x,
                name: a.name,
                role: a.role,
                proposal: a.proposal,
                learnAt: a.learnAt,
                learnTotal: a.learnTotal,
                learnBy: a.learnBy,
                learnBusy: a.learnBusy,
              }
            : x,
        ),
      })),
    )
    sub(events.agentDeleted, (id) =>
      setState((s) => ({
        ...s,
        agents: s.agents.filter((a) => a.id !== id),
        streaming: omit(s.streaming, id),
        channelStreaming: omit(s.channelStreaming, id),
        // 删会话要把它的全部临时状态一起带走。只清一半的话，
        // errors / tokenUsage 这些按 id 取值的 map 会留着上一个会话的值，
        // 界面按 id 读时就串味（架构评审 #5/#6 指出过）。
        errors: omit(s.errors, id),
        shellByAgent: omit(s.shellByAgent, id),
        reasoning: omit(s.reasoning, id),
        pace: omit(s.pace, id),
        tokenUsage: omit(s.tokenUsage, id),
        stops: omit(s.stops, id),
        tools: s.tools.filter((t) => t.id !== id),
      })),
    )
    // token 级流式片段：累积到 streaming[agentId]，assistant 完整消息到达时清掉。
    sub(events.agentDelta, ({ id, delta, channelId }) => {
      if (!delta || !id) return
      if (channelId) {
        pushBuf(channelBuf, channelGen, id, delta)
        return
      }
      if (openedRef.current && openedRef.current !== id) return
      pushBuf(deltaBuf, deltaGen, id, delta)
    })
    sub(events.agentStatus, ({ id, status, busy }) => {
      if (status !== 'running') {
        dropDeltas(id)
        dropReason(id)
      }
      setState((s) => applyAgentStatus(s, id, status, busy))
    })
    sub(events.agentMessage, ({ id, message, tokensIn, tokensOut }) => {
      if (message.role === 'assistant') {
        dropDeltas(id)
        dropReason(id)
      }
      setState((s) => {
        const newTokenUsage = { ...s.tokenUsage }
        if (tokensIn || tokensOut) {
          const existing = newTokenUsage[id] ?? { in: 0, out: 0 }
          newTokenUsage[id] = {
            in: existing.in + (tokensIn ?? 0),
            out: existing.out + (tokensOut ?? 0),
            lastIn: tokensIn ? tokensIn : existing.lastIn,
          }
        }
        return {
          ...s,
          agents: s.agents.map((a) => {
            if (a.id !== id) return a
            const list = a.messages ?? []
            // 乐观插入过同一条 user 消息时不要再追加一份。
            // 工具回合已经把流式文本收成消息时，结束事件再带同一段也不再追加。
            const last = list[list.length - 1]
            if (message.role === 'user' && last?.role === 'user' && last.content === message.content) {
              // 服务端没带时间戳时保留本地发送时刻；自带 timestamp 以服务端为准。
              const merged = { ...last, ...message, timestamp: message.timestamp ?? last.timestamp }
              return { ...a, messages: sortAgentMessages([...list.slice(0, -1), merged]) }
            }
            const spoken = clipSentence(message.content) || a.last
            if (s.opened !== id) return { ...a, last: spoken }
            if (message.role === 'assistant' && last?.role === 'assistant' && last.content === message.content) {
              if (message.reasoning && message.reasoning !== last.reasoning) {
                return {
                  ...a,
                  last: spoken,
                  messages: sortAgentMessages([...list.slice(0, -1), { ...last, reasoning: message.reasoning }]),
                }
              }
              return { ...a, last: spoken }
            }
            return { ...a, last: spoken, messages: sortAgentMessages([...list, message]) }
          }),
          streaming: message.role === 'assistant' ? omit(s.streaming, id) : s.streaming,
          channelStreaming: message.role === 'assistant' ? omit(s.channelStreaming, id) : s.channelStreaming,
          reasoning: message.role === 'assistant' ? omit(s.reasoning, id) : s.reasoning,
          tokenUsage: newTokenUsage,
          // 后端回推了消息说明确实在处理，清掉 pending
          pending: omit(s.pending, id),
        }
      })
    })
    sub(events.agentError, ({ id, error }) => {
      dropDeltas(id)
      dropReason(id)
      // 出错不再把排队文本补插进列表：插话在发送时刻就已乐观可见（见 agent.prompt），
      // 这里只记录错误、清掉流式过程；真正发送失败的回滚在 sendImpl 的 catch 里做。
      setState((s) => ({
        ...s,
        errors: rememberError(s, id, error),
        streaming: omit(s.streaming, id),
        channelStreaming: omit(s.channelStreaming, id),
        reasoning: omit(s.reasoning, id),
        pending: omit(s.pending, id),
      }))
    })
    sub(events.toolStart, (e) => {
      // 工具已开始执行 —— 这是信息事件。真正的「等你审批」走 tool.approval，
      // 审批卡由 ApprovalModal 单独渲染；这里不能把 agent 标成 blocked。
      setState((s) => ({
        ...s,
        tools: [
          ...s.tools,
          { key: `tool-${++toolSeq}`, id: e.agentId || e.id, tool: e.tool, target: e.target, status: 'ok' as const, at: Date.now() },
        ].slice(-60),
      }))
    })
    // loop 内工具调用：start 时只记下正在做什么，并清掉还没落成回复的过程句。
    // 过程句不进对话。end 时补上这一步的结果摘要。
    sub(events.agentReasoning, (e) => {
      if (!e.reasoning || !e.id) return
      if (openedRef.current && openedRef.current !== e.id) return
      pushBuf(reasonBuf, reasonGen, e.id, e.reasoning)
    })
    sub(events.agentTool, (event) => {
      // 新字段由后端运行时事件补充；生成的旧客户端类型未更新时也能平滑接收。
      const e = event as typeof event & {
        callId?: string
        input?: string
        output?: string
        path?: string
        diff?: string
        truncated?: boolean
        exitCode?: number
        durationMs?: number
      }
      // cancelled 是「被用户插话打断」，不是错误，单独走中性态，不进失败统计。
      const status: ToolCall['status'] =
        e.status === 'waiting'
          ? 'waiting'
          : e.phase === 'start'
            ? 'running'
            : e.status === 'cancelled'
              ? 'cancelled'
              : e.status === 'error' || e.status === 'timeout' || e.status === 'denied'
                ? 'error'
                : 'ok'
      if (e.phase === 'start') {
        dropDeltas(e.id)
        reasonGap.add(e.id)
      }
      const detail =
        e.phase !== 'end'
          ? ''
          : oneLine(e.content) || (status === 'cancelled' ? '调用被用户插话打断，未完成' : '')
      const output = e.phase === 'end' ? (e.content ?? '').trim().slice(0, 4000) : ''
      setState((s) => {
        if (e.phase === 'start') {
          // 同一个 callId 的 start 可能来两次（流式 content_block_start 先到、
          // 完整 assistant 消息带着入参后到）。第二次只补细节，不再加一行。
          const at = s.tools.findIndex(
            (t) => t.id === e.id && e.callId && t.callId === e.callId && t.status === 'running',
          )
          const row = {
            id: e.id,
            callId: e.callId,
            tool: e.tool,
            target: e.content ?? '',
            status,
            input: e.input,
            path: e.path,
            at: Date.now(),
          }
          if (at >= 0) {
            const tools = s.tools.slice()
            const prev = tools[at]
            tools[at] = {
              ...prev,
              tool: e.tool || prev.tool,
              target: e.content || prev.target,
              input: e.input || prev.input,
              path: e.path || prev.path,
            }
            return {
              ...s,
              streaming: omit(s.streaming, e.id),
              pace: { ...(s.pace ?? {}), [e.id]: { since: Date.now() } },
              tools,
            }
          }
          return {
            ...s,
            streaming: omit(s.streaming, e.id),
            pace: { ...(s.pace ?? {}), [e.id]: { since: Date.now() } },
            tools: [...s.tools, { key: `tool-${++toolSeq}`, ...row }].slice(-120),
          }
        }
        const tools = s.tools.slice()
        let idx = -1
        for (let i = tools.length - 1; i >= 0; i--) {
          const t = tools[i]
          if (
            t.id === e.id &&
            (t.status === 'running' || t.status === 'waiting') &&
            (e.callId ? t.callId === e.callId : t.tool === e.tool)
          ) {
            idx = i
            break
          }
        }
        const previous = idx >= 0 ? tools[idx] : undefined
        const finished: ToolCall = {
          key: previous?.key ?? `tool-${++toolSeq}`,
          id: e.id,
          callId: e.callId,
          // end 事件只带 callId，工具名是占位的 "tool"。别把行名从 Bash 改成 tool。
          tool: previous?.tool ?? e.tool,
          target: previous?.target ?? e.tool,
          status,
          detail: detail || previous?.detail,
          input: e.input || previous?.input,
          output: output || previous?.output,
          path: e.path || previous?.path,
          diff: e.diff || previous?.diff,
          truncated: e.truncated,
          exitCode: e.exitCode,
          durationMs: e.durationMs,
          at: previous?.at ?? Date.now(),
        }
        // 窗口中途订阅时可能只收到 end；补一条完成记录，不再静默丢结果。
        if (idx >= 0) tools[idx] = finished
        else tools.push(finished)
        return { ...s, tools }
      })
    })
    // 循环结局：done / failed。失败时要把原因带到界面上，
    // 否则用户只看到「停止了」但不知道为什么。
    sub(events.agentLoop, (e) => {
      setState((s) => {
        if (e.kind !== 'failed') return s
        return { ...s, errors: rememberError(s, e.id, e.reason || '循环失败') }
      })
    })
    // 停止原因：用户按停止和预算用尽是两回事，必须能区分。
    sub(events.agentStopped, (e) => {
      dropDeltas(e.id)
      dropReason(e.id)
      setState((s) => ({
        ...s,
        streaming: omit(s.streaming, e.id),
        channelStreaming: omit(s.channelStreaming, e.id),
        reasoning: omit(s.reasoning, e.id),
        stops: { ...s.stops, [e.id]: { reason: e.reason, iter: e.iter, used: e.used, at: Date.now() } },
      }))
    })
    // 命令执行结果直接进对话流，不再另开一个面板：
    // 用户在一个地方就能看到「我敲了什么 → 输出了什么」。
    sub(events.shellStart, (e: ShellEvent) =>
      setState((s) => {
        const log: ShellLog = {
          id: `sh-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          command: e.command,
          at: Date.now(),
        }
        return {
          ...s,
          shell: [log, ...s.shell].slice(0, 40),
          shellByAgent: {
            ...s.shellByAgent,
            [e.agentId]: [...(s.shellByAgent[e.agentId] ?? []), log].slice(-40),
          },
        }
      }),
    )
    sub(events.shellResult, (e: ShellResultEvent) =>
      setState((s) => {
        const apply = (logs: ShellLog[]) => {
          // 从后往前找最近一条尚未有结果的同命令记录
          const next = [...logs]
          for (let i = next.length - 1; i >= 0; i--) {
            if (next[i].command === e.command && !next[i].result) {
              next[i] = { ...next[i], result: e.result }
              break
            }
          }
          return next
        }
        return {
          ...s,
          shell: apply(s.shell),
          shellByAgent: {
            ...s.shellByAgent,
            [e.agentId]: apply(s.shellByAgent[e.agentId] ?? []),
          },
        }
      }),
    )
    sub(events.llmConfig, (llm) => setState((s) => ({ ...s, llm })))
    sub(events.workdir, ({ dir }) => setState((s) => ({ ...s, workDir: dir })))

    // 会话级配置变更：整体替换该 agent
    sub(events.agentConfig, (a) =>
      setState((s) => ({
        ...s,
        agents: s.agents.map((x) => {
          if (x.id !== a.id) return x
          const parentId = a.parentId || x.parentId
          if (s.opened === a.id) return { ...x, ...a, parentId, messages: x.messages, last: a.last || x.last }
          return shellAgent({ ...x, ...a, parentId, messages: x.messages })
        }),
      })),
    )

    // 频道
    sub(events.channelDeleted, (id) =>
      setState((s) => ({ ...s, channels: s.channels.filter((c) => c.id !== id) })),
    )
    sub(events.channelCreated, (ch) =>
      setState((s) =>
        s.channels.some((x) => x.id === ch.id)
          ? { ...s, channels: s.channels.map((x) => (x.id === ch.id ? ch : x)) }
          : { ...s, channels: [...s.channels, ch] },
      ),
    )
    sub(events.channelMessage, (msg: ChannelMessage) =>
      setState((s) => {
        const existed = s.channels.some((ch) =>
          ch.id === msg.channelId && (ch.messages ?? []).some((m) => m.id === msg.id),
        )
        return {
          ...s,
          channelStreaming: existed ? s.channelStreaming : omit(s.channelStreaming, msg.fromId),
          channels: s.channels.map((ch) =>
            ch.id === msg.channelId
              ? {
                  ...ch,
                  messages: sortChannelMessages(
                    (ch.messages ?? []).some((m) => m.id === msg.id)
                      ? (ch.messages ?? []).map((m) => (m.id === msg.id ? msg : m))
                      : [...(ch.messages ?? []), msg],
                  ),
                }
              : ch,
          ),
        }
      }),
    )

    // 知识库
    sub(events.knowledgeChanged, (n: Note) =>
      setState((s) => ({
        ...s,
        notes: s.notes.some((x) => x.id === n.id)
          ? s.notes.map((x) => (x.id === n.id ? n : x))
          : [...s.notes, n],
      })),
    )
    sub(events.knowledgeDeleted, (id) =>
      setState((s) => ({ ...s, notes: s.notes.filter((n) => n.id !== id) })),
    )

    // 审批：按 id 去重后入队。决定送达后端时本地先收卡，后端随后发的 done 是幂等的；
    // 送达失败时不收卡，错误会留在卡片上。
    sub(events.toolApproval, (v: ApprovalView) =>
      setState((s) =>
        s.approvals.some((x) => x.id === v.id)
          ? { ...s, approvals: s.approvals.map((x) => (x.id === v.id ? v : x)) }
          : { ...s, approvals: [...s.approvals, v] },
      ),
    )
    sub(events.toolApprovalDone, (id) =>
      setState((s) => ({ ...s, approvals: s.approvals.filter((x) => x.id !== id) })),
    )

    // 写意图：落盘前广播"谁要改哪个文件"。
    // 同一 agent 对同一文件的重复意图只留最后一条，避免刷屏；最多留 12 条。
    sub(events.agentFileIntent, (e) =>
      setState((s) => {
        const fresh = s.intents.filter((x) => Date.now() / 1000 - x.at < 60)
        const kept = fresh.filter((x) => !(x.agentId === e.agentId && x.paths[0] === e.paths[0]))
        return { ...s, intents: [...kept, e].slice(-12) }
      }),
    )

    return () => {
      alive = false
      if (raf) cancelAnimationFrame(raf)
      unsubs.forEach((u) => u())
    }
  }, [])

  const sendImpl = useCallback((action: string, data?: unknown): Promise<unknown> => {
    switch (action) {
      case 'agent.create':
        return API.createAgent(data as { name: string; role: string })
      case 'agent.createCLI':
        return API.createCLIAgent(
          (data as { name: string }).name,
          (data as { role: string }).role,
          (data as { backend: string }).backend,
        )
      case 'agent.createSupervisor':
        return API.createSupervisor(data as { name: string; role: string; goal: string; workDir: string })
      case 'agent.delete':
        return API.deleteAgent((data as { id: string }).id)
      case 'agent.profile': {
        const { id, name, role } = data as { id: string; name: string; role: string }
        return API.setAgentProfile(id, name, role)
      }
      case 'feishu.bind': {
        const { chatId, agentId } = data as { chatId: string; agentId: string }
        return API.setFeishuRoomAgent(chatId, agentId).then(() => {
          setState((s) => ({
            ...s,
            feishuRooms: s.feishuRooms.map((r) =>
              r.id === chatId
                ? {
                    ...r,
                    agentId,
                    agentName: s.agents.find((a) => a.id === agentId)?.name || r.agentName,
                  }
                : r,
            ),
          }))
        })
      }
      case 'agent.stop':
        return API.stopAgent((data as { id: string }).id)
      case 'agent.prompt': {
        const { agentId, text, images } = data as {
          agentId: string
          text: string
          images?: { mime: string; data: string }[]
        }
        // 发送时刻立刻盖章：无论后面是排队、补拉还是重试，这条消息的位置都按发送时刻归并，
        // 不再等后端回执 / flush 时另盖一次时间戳（案例 4/5 的根因）。
        const stamp = Math.floor(Date.now() / 1000)
        setState((s) => {
          const msg = {
            role: 'user' as const,
            content: text,
            timestamp: stamp,
            blocks: images?.map((img) => ({ mime: img.mime, data: img.data })),
          }
          return {
            ...s,
            // busy 也直接乐观插进正文：插话发送即可见，位置按发送时刻排；
            // 后端在工具回合之间读到新消息由 loop 侧 Steer/SteerCh 负责，前端不再另排队列。
            agents: s.agents.map((a) =>
              a.id === agentId
                ? { ...a, messages: sortAgentMessages([...(a.messages ?? []), msg]) }
                : a,
            ),
            pending: { ...s.pending, [agentId]: true },
            errors: omit(s.errors, agentId),
            tools: s.tools.filter((t) => t.id !== agentId),
          }
        })
        return API.promptAgent({ agentId, text, images }).catch((e) => {
          const msg = e instanceof Error ? e.message : String(e)
          setState((s) => ({
            ...s,
            pending: omit(s.pending, agentId),
            streaming: omit(s.streaming, agentId),
            errors: rememberError(s, agentId, msg || '发送失败'),
            agents: s.agents.map((a) => {
              if (a.id !== agentId) return a
              // 发送被后端拒绝：回滚刚才那条还没拿到回执的乐观消息，不留在列表里假装成功。
              const list = [...(a.messages ?? [])]
              for (let i = list.length - 1; i >= 0; i--) {
                if (list[i].role === 'user' && list[i].content === text) {
                  list.splice(i, 1)
                  break
                }
              }
              const status = a.status === 'running' || a.status === 'queued' ? 'idle' : a.status
              return { ...a, status, messages: sortAgentMessages(list) }
            }),
          }))
          throw e
        })
      }
      case 'agent.route':
        return API.routeAgent(
          (data as { fromId: string }).fromId,
          (data as { toId: string }).toId,
          (data as { text: string }).text,
        )
      case 'shell.run':
        return API.runShell(data as { agentId: string; command: string })
      case 'llm.config':
        return API.setLLMConfig(data as Config).then((cfg) => {
          if (cfg) setState((s) => ({ ...s, llm: cfg }))
          return cfg
        })
      case 'workdir.set':
        return API.setWorkDir((data as { dir: string }).dir)
      case 'agent.workDir': {
        const { id, dir } = data as { id: string; dir: string }
        return API.setAgentWorkDir(id, dir).catch((e) => {
          console.error('setAgentWorkDir failed', e)
          return null
        })
      }
      case 'agent.clearLLM':
        return API.clearAgentLLM((data as { id: string }).id).catch((e) => {
          console.error('clearAgentLLM failed', e)
          return null
        })
      case 'agent.llm': {
        const { id, cfg } = data as { id: string; cfg: Config }
        return API.setAgentLLM(id, cfg)
          .then((view) => {
            // 返回值先落进本地状态。只等 agent.config 事件的话，
            // 按钮上的模型要等事件到了才算数，中间发出去的消息仍用旧配置。
            if (view?.id) {
              setState((s) => ({
                ...s,
                agents: s.agents.map((x) => {
                  if (x.id !== view.id) return x
                  const parentId = view.parentId || x.parentId
                  if (s.opened === x.id) return { ...x, ...view, parentId, messages: x.messages, last: view.last || x.last }
                  return shellAgent({ ...x, ...view, parentId, messages: x.messages })
                }),
              }))
            }
            return view
          })
          .catch((e) => {
            console.error('setAgentLLM failed', e)
            return null
          })
      }
      case 'channel.create': {
        const { name, members } = data as { name: string; members: string[] }
        return API.createChannel(name, members, 'discuss')
      }
      case 'channel.speaker': {
        const { channelId, agentId } = data as { channelId: string; agentId: string }
        return API.setChannelSpeaker(channelId, agentId).catch((e) => {
          console.error('setChannelSpeaker failed', e)
          return null
        })
      }
      case 'channel.delete':
        return API.deleteChannel((data as { id: string }).id).then((ok) => {
          if (ok) {
            const id = (data as { id: string }).id
            setState((s) => ({ ...s, channels: s.channels.filter((c) => c.id !== id) }))
          }
          return ok
        })
      case 'channel.send': {
        const { channelId, fromId, fromName, text, images } = data as {
          channelId: string
          fromId: string
          fromName: string
          text: string
          images?: { mime: string; data: string }[]
        }
        const sent = images?.length
          ? API.sendChannelImages(channelId, fromId, fromName, text, images)
          : API.sendToChannel(channelId, fromId, fromName, text)
        return sent.catch((e) => {
          console.error('sendToChannel failed', e)
          throw e
        })
      }
      case 'note.upsert':
        return API.upsertNote(data as Note)
      case 'note.delete':
        return API.deleteNote((data as { id: string }).id)
      case 'note.attach': {
        const { noteId, agentId } = data as { noteId: string; agentId: string }
        return API.attachNoteToAgent(noteId, agentId)
      }
      case 'note.detach': {
        const { noteId, agentId } = data as { noteId: string; agentId: string }
        return API.detachNoteFromAgent(noteId, agentId)
      }
    }
    return Promise.resolve(undefined)
  }, [])

  const send = sendImpl as <T = unknown>(action: string, data?: unknown) => Promise<T>

  const openAgent = useCallback((id: string) => {
    if (!id) return
    const ticket = ++pageGen.current
    openedRef.current = id
    setState((s) => ({
      ...s,
      opened: id,
      agents: s.agents.map((a) => (a.id === id ? a : shellAgent(a))),
      streaming: s.streaming[id] !== undefined ? { [id]: s.streaming[id] } : {},
      reasoning: s.reasoning[id] !== undefined ? { [id]: s.reasoning[id] } : {},
    }))
    API.agentMessages(id, PAGE)
      .then((messages) => {
        if (pageGen.current !== ticket || openedRef.current !== id) return
        const list = messages ?? []
        setState((s) => {
          if (s.opened !== id) return s
          const more = list.length >= PAGE
          const cur = s.agents.find((a) => a.id === id)
          const curMsgs = cur?.messages ?? []
          const prefix =
            curMsgs.length >= list.length &&
            list.every((m, i) => curMsgs[i]?.role === m.role && curMsgs[i]?.content === m.content)
          const extra = curMsgs.filter(
            (m) => m.role === 'user' && !list.some((x) => x.role === 'user' && x.content === m.content),
          )
          const next = sortAgentMessages(prefix ? curMsgs : [...list, ...extra])
          return {
            ...s,
            hasOlder: { ...s.hasOlder, [id]: more },
            agents: s.agents.map((a) =>
              a.id === id ? { ...a, messages: next, last: lastFrom(next) || a.last } : shellAgent(a),
            ),
          }
        })
      })
      .catch(() => {})
  }, [])

  /**
   * 往前再拉一页历史。
   *
   * 后端只发最近 40 条，没有这个入口的话更早的对话永远看不到。
   * before 用当前已有条数当绝对下标：后端按 messages[0, before) 取一页。
   */
  const loadOlder = useCallback((id: string) => {
    setState((s) => {
      const cur = s.agents.find((a) => a.id === id)
      if (!cur || !s.hasOlder[id]) return s
      const before = (cur.messages ?? []).length
      API.agentMessagesBefore(id, before, PAGE)
        .then((older) => {
          if (!older?.length) {
            setState((p) => ({ ...p, hasOlder: { ...p.hasOlder, [id]: false } }))
            return
          }
          setState((p) => ({
            ...p,
            hasOlder: { ...p.hasOlder, [id]: older.length >= PAGE },
            agents: p.agents.map((a) =>
              a.id === id
                ? {
                    ...a,
                    messages: sortAgentMessages([...older, ...(a.messages ?? [])]),
                    last: a.last || lastFrom(older),
                  }
                : a,
            ),
          }))
        })
        .catch(() => {})
      return s
    })
  }, [])

  const parkAgents = useCallback(() => {
    pageGen.current++
    openedRef.current = ''
    setState((s) => ({
      ...s,
      opened: '',
      agents: s.agents.map(shellAgent),
      streaming: {},
      reasoning: {},
    }))
  }, [])

  const refreshNotes = useCallback(() => {
    API.queryNotes({ Scope: '', Tags: [], Text: '', Limit: 0 })
      .then((notes) => setState((s) => ({ ...s, notes: notes ?? [] })))
      .catch(() => {})
  }, [])

  useEffect(() => {
    let stop = false
    const pull = () => {
      API.listFeishuRooms()
        .then((rooms) => {
          if (!stop) setState((s) => ({ ...s, feishuRooms: rooms ?? [] }))
        })
        .catch(() => {})
    }
    pull()
    const timer = window.setInterval(pull, 4000)
    return () => {
      stop = true
      window.clearInterval(timer)
    }
  }, [])

  useEffect(() => {
    // 心跳：connected 之前永远是 true，断线横幅从不亮。用最轻的已有 RPC 探活，
    // 连续两次失败才判断线，避免启动期单次抖动误报。
    let stop = false
    let fails = 0
    const beat = () => {
      API.getWorkDir()
        .then(() => {
          fails = 0
          if (!stop) setState((s) => (s.connected ? s : { ...s, connected: true }))
        })
        .catch(() => {
          fails++
          if (!stop && fails >= 2) setState((s) => (s.connected ? { ...s, connected: false } : s))
        })
    }
    const timer = window.setInterval(beat, 5000)
    return () => {
      stop = true
      window.clearInterval(timer)
    }
  }, [])

  useEffect(() => {
    // 版本号取自后端打包信息，不在前端写死——写死的那个永远和实际跑的进程对不上。
    API.info()
      .then((i) => {
        if (i?.version) setState((s) => ({ ...s, version: i.version }))
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    const pull = () => refreshNotes()
    const onVis = () => {
      if (document.visibilityState === 'visible') pull()
    }
    window.addEventListener('focus', pull)
    document.addEventListener('visibilitychange', onVis)
    return () => {
      window.removeEventListener('focus', pull)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [refreshNotes])

  // 决定送达后端后先收卡，界面立刻有反馈；后端随后发的 done 是幂等的。
  // 送达失败时不收卡，错误留在卡片上，用户能重试。
  const decideApproval = useCallback(async (id: string, allow: boolean, remember: boolean) => {
    try {
      await API.decideApproval(id, allow, remember)
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      // 「已失效或已完成」是幂等信号：这张卡已经被别的端（桌面/飞书）处理掉了。
      // 继续把它留在界面上只会让人反复点一张死卡，所以也按已收卡处理，再把原因抛出去。
      if (/失效|已完成|not found/i.test(msg)) {
        setState((s) => ({ ...s, approvals: s.approvals.filter((x) => x.id !== id) }))
      }
      throw e
    }
    setState((s) => ({ ...s, approvals: s.approvals.filter((x) => x.id !== id) }))
  }, [])

  return { ...state, send, refreshNotes, openAgent, parkAgents, loadOlder, decideApproval }
}

function oneLine(text?: string) {
  const line = (text ?? '').replace(/\s+/g, ' ').trim()
  if (!line) return ''
  return line.length > 80 ? `${line.slice(0, 80)}…` : line
}

function rememberError(s: State, id: string, text: string): Record<string, SessionError> {
  const after = s.agents.find((a) => a.id === id)?.messages?.length ?? 0
  return { ...s.errors, [id]: { text, after } }
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}