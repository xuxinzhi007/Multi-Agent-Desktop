import { useCallback, useEffect, useState } from 'react'
import {
  API,
  events,
  type AgentView as Agent,
  type Channel,
  type ChannelMessage,
  type Config,
  type Note,
  type ShellEvent,
  type ShellResultEvent,
} from '../mygo'
import type { FileChange, TodoItem, ToolCall } from '../components/Inspector'

export type LLMConfig = Config
export type ShellLog = {
  id: string
  command: string
  at: number
  result?: { stdout: string; stderr: string; exitCode: number }
}

type State = {
  connected: boolean
  version: string
  workDir: string
  llm: LLMConfig
  agents: Agent[]
  channels: Channel[]
  notes: Note[]
  streaming: Record<string, string>
  errors: Record<string, string>
  shell: ShellLog[]
  /** 每个 agent 执行过的命令，按 agentId 归集，直接渲染在对话流里 */
  shellByAgent: Record<string, ShellLog[]>
  changes: FileChange[]
  todos: TodoItem[]
  tools: ToolCall[]
  /** 这一轮还没落成消息的思考文本，按 agent id */
  reasoning: Record<string, string>
  tokenUsage: Record<string, { in: number; out: number }>
  /** 悬浮窗点击 agent 后请求主窗切换到的 agent id；消费方读完应调 clearFocus 清空 */
  focusAgentId: string | null
  /** 已经乐观插入消息、但后端还没回执的 agent */
  pending: Record<string, boolean>
  /** 最近一次停止的原因与进度。「为什么停」必须能说清，不能只显示「已停止」。 */
  stops: Record<string, { reason: string; iter: number; used: number; at: number }>
}

const EMPTY: State = {
  connected: true,
  version: '0.3.0-mygo',
  workDir: '',
  llm: { provider: 'mock', baseUrl: '', apiKey: '', model: '' },
  agents: [],
  channels: [],
  notes: [],
  streaming: {},
  errors: {},
  shell: [],
  shellByAgent: {},
  changes: [],
  todos: [],
  tools: [],
  reasoning: {},
  tokenUsage: {},
  focusAgentId: null,
  pending: {},
  stops: {},
}

export function useBackend() {
  const [state, setState] = useState<State>(EMPTY)

  useEffect(() => {
    let alive = true
    const unsubs: (() => void)[] = []

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

    load(API.listAgents(), (agents) => setState((s) => ({ ...s, agents: agents ?? [] })))
    load(API.listChannels(), (channels) => setState((s) => ({ ...s, channels: channels ?? [] })))
    load(API.queryNotes({ Scope: '', Tags: [], Text: '', Limit: 0 }), (notes) =>
      setState((s) => ({ ...s, notes: notes ?? [] })),
    )
    load(API.getLLMConfig(), (llm) => setState((s) => ({ ...s, llm })))
    load(API.getWorkDir(), (workDir) => setState((s) => ({ ...s, workDir })))

    sub(events.agentCreated, (a) =>
      setState((s) => (s.agents.some((x) => x.id === a.id) ? s : { ...s, agents: [...s.agents, a] })),
    )
    sub(events.agentDeleted, (id) =>
      setState((s) => ({
        ...s,
        agents: s.agents.filter((a) => a.id !== id),
        streaming: omit(s.streaming, id),
      })),
    )
    // token 级流式片段：累积到 streaming[agentId]，assistant 完整消息到达时清掉。
    sub(events.agentFocused, ({ agentId }) => setState((s) => ({ ...s, focusAgentId: agentId })))
    sub(events.agentDelta, ({ id, delta }) => {
      if (!delta) return
      setState((s) => ({ ...s, streaming: { ...s.streaming, [id]: (s.streaming[id] ?? '') + delta } }))
    })
    sub(events.agentStatus, ({ id, status }) =>
      setState((s) => ({
        ...s,
        agents: s.agents.map((a) => (a.id === id ? { ...a, status: status as Agent['status'] } : a)),
      })),
    )
    sub(events.agentMessage, ({ id, message, tokensIn, tokensOut }) =>
      setState((s) => {
        const newTokenUsage = { ...s.tokenUsage }
        if (tokensIn || tokensOut) {
          const existing = newTokenUsage[id] ?? { in: 0, out: 0 }
          newTokenUsage[id] = { in: existing.in + (tokensIn ?? 0), out: existing.out + (tokensOut ?? 0) }
        }
        return {
          ...s,
          agents: s.agents.map((a) => {
            if (a.id !== id) return a
            const list = a.messages ?? []
            // 乐观插入过同一条 user 消息时不要再追加一份
            const last = list[list.length - 1]
            if (message.role === 'user' && last?.role === 'user' && last.content === message.content) {
              return a
            }
            return { ...a, messages: [...list, message] }
          }),
          streaming: message.role === 'assistant' ? omit(s.streaming, id) : s.streaming,
          reasoning: message.role === 'assistant' ? omit(s.reasoning, id) : s.reasoning,
          tokenUsage: newTokenUsage,
          // 后端回推了消息说明确实在处理，清掉 pending
          pending: omit(s.pending, id),
        }
      }),
    )
    sub(events.agentError, ({ id, error }) =>
      setState((s) => {
        return {
          ...s,
          errors: { ...s.errors, [id]: error },
          streaming: omit(s.streaming, id),
        }
      }),
    )
    sub(events.toolStart, (e) => {
      // 工具已开始执行 —— 这是信息事件，不是审批请求。
      // 真正的「等你审批」要等后端接入工具审批流程后由后端发状态，
      // 前端不能在这里把 agent 标成 blocked（会永久锁在等人状态）。
      setState((s) => ({
        ...s,
        tools: [...s.tools, { id: e.id, tool: e.tool, target: e.target, status: 'ok', at: Date.now() }],
      }))
    })
    // loop 内工具调用：start 时记一条「正在做什么」，
    // end 时补上结果。这些信息让用户能看到循环真的在推进，
    // 而不是只看到一个「运行中」然后干等。
    sub(events.agentReasoning, (e) => {
      if (!e.reasoning) return
      setState((s) => {
        const prev = s.reasoning[e.id]
        const next = prev ? `${prev}\n\n${e.reasoning}` : e.reasoning
        return { ...s, reasoning: { ...s.reasoning, [e.id]: next } }
      })
    })
    sub(events.agentTool, (e) => {
      // 后端 status 是 string，这里收窄到 Inspector 认识的联合类型，
      // 未知状态一律当 ok —— 不要因为一个新状态值就让类型崩掉。
      const status: ToolCall['status'] =
        e.phase === 'start'
          ? 'running'
          : e.status === 'error' || e.status === 'timeout' || e.status === 'denied'
            ? 'error'
            : 'ok'
      setState((s) => {
        if (e.phase === 'start') {
          return {
            ...s,
            tools: [
              ...s.tools,
              { id: e.id, tool: e.tool, target: e.content ?? '', status, at: Date.now() },
            ].slice(-60),
          }
        }
        const tools = s.tools.slice()
        for (let i = tools.length - 1; i >= 0; i--) {
          if (tools[i].id === e.id && tools[i].tool === e.tool && tools[i].status === 'running') {
            tools[i] = { ...tools[i], status }
            break
          }
        }
        return { ...s, tools }
      })
    })
    // 循环结局：done / failed。失败时要把原因带到界面上，
    // 否则用户只看到「停止了」但不知道为什么。
    sub(events.agentLoop, (e) => {
      setState((s) => {
        if (e.kind !== 'failed') return s
        return { ...s, errors: { ...s.errors, [e.id]: e.reason || '循环失败' } }
      })
    })
    // 停止原因：用户按停止和预算用尽是两回事，必须能区分。
    sub(events.agentStopped, (e) => {
      setState((s) => ({
        ...s,
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
      setState((s) => ({ ...s, agents: s.agents.map((x) => (x.id === a.id ? a : x)) })),
    )

    // 频道
    sub(events.channelCreated, (ch) =>
      setState((s) =>
        s.channels.some((x) => x.id === ch.id) ? s : { ...s, channels: [...s.channels, ch] },
      ),
    )
    sub(events.channelMessage, (msg: ChannelMessage) =>
      setState((s) => ({
        ...s,
        channels: s.channels.map((ch) =>
          ch.id === msg.channelId
            ? {
                ...ch,
                // 同一 ID 重发时覆盖旧记录（用于回填 deliveries）
                messages: (ch.messages ?? []).some((m) => m.id === msg.id)
                  ? (ch.messages ?? []).map((m) => (m.id === msg.id ? msg : m))
                  : [...(ch.messages ?? []), msg],
              }
            : ch,
        ),
      })),
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

    return () => {
      alive = false
      unsubs.forEach((u) => u())
    }
  }, [])

  /** send 返回按 action 变化的类型；用泛型让调用处拿到精确结果。 */
  /**
   * 乐观插入用户消息。
   *
   * 之前消息完全依赖后端的 agent.message 事件回推 —— 后端一慢、一卡、
   * 或者事件丢了，用户敲完字屏幕上什么都不出现，看起来就是「发出去没反应」。
   * 现在先在本地插入，让用户立刻看到自己的话；后端回推时按内容去重。
   */
  const optimisticMessage = useCallback(
    (agentId: string, text: string, role: 'user' | 'assistant' = 'user') => {
      const msg = { role, content: text, timestamp: Math.floor(Date.now() / 1000) }
      setState((s) => ({
        ...s,
        agents: s.agents.map((a) =>
          a.id === agentId ? { ...a, messages: [...(a.messages ?? []), msg] } : a,
        ),
        // 立刻进入「处理中」，否则用户不知道有没有发出去
        pending: { ...s.pending, [agentId]: true },
      }))
    },
    [],
  )

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
      case 'agent.delete':
        return API.deleteAgent((data as { id: string }).id)
      case 'agent.stop':
        return API.stopAgent((data as { id: string }).id)
      case 'agent.prompt': {
        const { agentId, text } = data as { agentId: string; text: string }
        optimisticMessage(agentId, text)
        return API.promptAgent({ agentId, text }).catch((e) => {
          // 失败要把乐观插入的消息标出来，不能假装成功
          setState((s) => ({ ...s, pending: omit(s.pending, agentId) }))
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
                agents: s.agents.map((x) => (x.id === view.id ? { ...x, ...view } : x)),
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
      case 'channel.send': {
        const { channelId, fromId, fromName, text } = data as {
          channelId: string
          fromId: string
          fromName: string
          text: string
        }
        return API.sendToChannel(channelId, fromId, fromName, text).catch((e) => {
          console.error('sendToChannel failed', e)
          return null
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

  // 批准/拒绝工具调用。
  // 当前后端还没有审批流（工具直接执行），所以这里只更新本地展示状态，
  // 不去调 unblockAgent —— 那会把真正在等人的 agent 错误解除。
  const approveTool = useCallback((id: string) => {
    setState((s) => ({ ...s, tools: s.tools.map((t) => (t.id === id ? { ...t, status: 'ok' } : t)) }))
  }, [])

  const rejectTool = useCallback((id: string) => {
    setState((s) => ({ ...s, tools: s.tools.map((t) => (t.id === id ? { ...t, status: 'error' } : t)) }))
  }, [])

  const clearFocus = useCallback(
    () => setState((s) => (s.focusAgentId === null ? s : { ...s, focusAgentId: null })),
    [],
  )

  return { ...state, send, approveTool, rejectTool, clearFocus }
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}