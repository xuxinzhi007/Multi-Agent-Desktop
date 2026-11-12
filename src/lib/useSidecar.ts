import { useCallback, useEffect, useState } from 'react'
import { Sidecar, events, type AgentView as Agent, type Config, type ShellEvent, type ShellResultEvent } from '../mygo'
import type { FileChange, TodoItem, ToolCall } from '../components/Inspector'
import type { Activity } from '../components/ActivityFeed'

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
  streaming: Record<string, string>
  errors: Record<string, string>
  shell: ShellLog[]
  changes: FileChange[]
  todos: TodoItem[]
  tools: ToolCall[]
  activities: Activity[]
  tokenUsage: Record<string, { in: number; out: number }>
}

const EMPTY: State = {
  connected: true,
  version: '0.3.0-mygo',
  workDir: '',
  llm: { provider: 'mock', baseUrl: '', apiKey: '', model: '' },
  agents: [],
  streaming: {},
  errors: {},
  shell: [],
  changes: [],
  todos: [],
  tools: [],
  activities: [],
  tokenUsage: {},
}

export function useSidecar() {
  const [state, setState] = useState<State>(EMPTY)

  useEffect(() => {
    let alive = true
    const unsubs: (() => void)[] = []

    const sub = <T>(evt: { on: (cb: (p: T) => void) => () => void }, cb: (p: T) => void) => {
      unsubs.push(evt.on((p) => alive && cb(p)))
    }

    Sidecar.listAgents().then((agents) => alive && setState((s) => ({ ...s, agents })))
    Sidecar.getLLMConfig().then((llm) => alive && setState((s) => ({ ...s, llm })))
    Sidecar.getWorkDir().then((workDir) => alive && setState((s) => ({ ...s, workDir })))

    // 辅助函数：添加活动记录
    const addActivity = (type: Activity['type'], fromId: string, text: string, toName?: string) => {
      setState((s) => {
        const from = s.agents.find((a) => a.id === fromId)
        const activity: Activity = {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          type,
          fromName: from?.name ?? '系统',
          fromColor: from?.color ?? '#888',
          toName,
          text: text.slice(0, 60),
          at: Date.now(),
        }
        return { ...s, activities: [...s.activities, activity].slice(-50) }
      })
    }

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
    sub(events.agentStatus, ({ id, status }) =>
      setState((s) => ({
        ...s,
        agents: s.agents.map((a) => (a.id === id ? { ...a, status: status as Agent['status'] } : a)),
      })),
    )
    sub(events.agentMessage, ({ id, message, tokensIn, tokensOut }) =>
      setState((s) => {
        const agent = s.agents.find((a) => a.id === id)
        const activity: Activity = {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          type: message.role === 'user' ? 'prompt' : 'status',
          fromName: message.role === 'user' ? '你' : agent?.name ?? 'Agent',
          fromColor: message.role === 'user' ? '#888' : agent?.color ?? '#5b9dff',
          text: message.content.slice(0, 60),
          at: Date.now(),
        }
        const newTokenUsage = { ...s.tokenUsage }
        if (tokensIn || tokensOut) {
          const existing = newTokenUsage[id] ?? { in: 0, out: 0 }
          newTokenUsage[id] = { in: existing.in + (tokensIn ?? 0), out: existing.out + (tokensOut ?? 0) }
        }
        return {
          ...s,
          agents: s.agents.map((a) => (a.id === id ? { ...a, messages: [...a.messages, message] } : a)),
          streaming: message.role === 'assistant' ? omit(s.streaming, id) : s.streaming,
          activities: [...s.activities, activity].slice(-50),
          tokenUsage: newTokenUsage,
        }
      }),
    )
    sub(events.agentError, ({ id, error }) =>
      setState((s) => {
        const agent = s.agents.find((a) => a.id === id)
        const activity: Activity = {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          type: 'error',
          fromName: agent?.name ?? 'Agent',
          fromColor: agent?.color ?? '#e5534b',
          text: error.slice(0, 60),
          at: Date.now(),
        }
        return {
          ...s,
          errors: { ...s.errors, [id]: error },
          streaming: omit(s.streaming, id),
          activities: [...s.activities, activity].slice(-50),
        }
      }),
    )
    sub(events.toolStart, (e) =>
      setState((s) => {
        const agent = s.agents.find((a) => a.id === e.agentId)
        const activity: Activity = {
          id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          type: 'tool',
          fromName: agent?.name ?? 'Agent',
          fromColor: agent?.color ?? '#e8a33d',
          text: `${e.tool}: ${e.target.slice(0, 40)}`,
          at: Date.now(),
        }
        return {
          ...s,
          tools: [...s.tools, { id: e.id, tool: e.tool, target: e.target, status: 'waiting', at: Date.now() }],
          activities: [...s.activities, activity].slice(-50),
        }
      }),
    )
    sub(events.shellStart, (e: ShellEvent) =>
      setState((s) => ({
        ...s,
        shell: [
          { id: `${Date.now()}-${Math.random().toString(36).slice(2, 7)}`, command: e.command, at: Date.now() },
          ...s.shell,
        ].slice(0, 40),
      })),
    )
    sub(events.shellResult, (e: ShellResultEvent) =>
      setState((s) => ({
        ...s,
        shell: s.shell.map((l) => (l.command === e.command && !l.result ? { ...l, result: e.result } : l)),
      })),
    )
    sub(events.llmConfig, (llm) => setState((s) => ({ ...s, llm })))
    sub(events.workdir, ({ dir }) => setState((s) => ({ ...s, workDir: dir })))

    return () => {
      alive = false
      unsubs.forEach((u) => u())
    }
  }, [])

  const send = useCallback((action: string, data?: unknown) => {
    switch (action) {
      case 'agent.create':
        return Sidecar.createAgent(data as { name: string; role: string })
      case 'agent.createCLI':
        return Sidecar.createCLIAgent(
          (data as { name: string }).name,
          (data as { role: string }).role,
          (data as { backend: string }).backend,
        )
      case 'agent.delete':
        return Sidecar.deleteAgent((data as { id: string }).id)
      case 'agent.stop':
        return Sidecar.stopAgent((data as { id: string }).id)
      case 'agent.prompt':
        return Sidecar.promptAgent(data as { agentId: string; text: string })
      case 'agent.route':
        return Sidecar.routeAgent(
          (data as { fromId: string }).fromId,
          (data as { toId: string }).toId,
          (data as { text: string }).text,
        )
      case 'shell.run':
        return Sidecar.runShell(data as { agentId: string; command: string })
      case 'llm.config':
        return Sidecar.setLLMConfig(data as Config)
      case 'workdir.set':
        return Sidecar.setWorkDir((data as { dir: string }).dir)
    }
  }, [])

  // 批准/拒绝工具调用
  const approveTool = useCallback((id: string) => {
    setState((s) => ({
      ...s,
      tools: s.tools.map((t) => (t.id === id ? { ...t, status: 'ok' } : t)),
    }))
  }, [])

  const rejectTool = useCallback((id: string) => {
    setState((s) => ({
      ...s,
      tools: s.tools.map((t) => (t.id === id ? { ...t, status: 'error' } : t)),
    }))
  }, [])

  return { ...state, send, approveTool, rejectTool }
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}