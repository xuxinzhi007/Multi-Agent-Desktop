import { useCallback, useEffect, useRef, useState } from 'react'
import {
  connect,
  fetchHealth,
  type Agent,
  type Connection,
  type Envelope,
  type LLMConfig,
  type ShellLog,
} from './ws'
import type { FileChange, TodoItem, ToolCall } from '../components/Inspector'

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
}

const EMPTY: State = {
  connected: false,
  version: '',
  workDir: '',
  llm: { provider: 'mock', baseUrl: '', apiKey: '', model: '' },
  agents: [],
  streaming: {},
  errors: {},
  shell: [],
  changes: [],
  todos: [],
  tools: [],
}

export function useSidecar() {
  const [state, setState] = useState<State>(EMPTY)
  const connRef = useRef<Connection | null>(null)

  useEffect(() => {
    let alive = true

    fetchHealth().catch(() => undefined)

    const conn = connect({
      onOpen: () => alive && setState((s) => ({ ...s, connected: true })),
      onClose: () => alive && setState((s) => ({ ...s, connected: false })),
      onEvent: (e: Envelope) => {
        if (!alive) return
        setState((s) => reduce(s, e))
      },
    })
    connRef.current = conn

    return () => {
      alive = false
      conn.close()
    }
  }, [])

  const send = useCallback((action: string, data?: unknown) => {
    connRef.current?.send(action, data)
  }, [])

  return { ...state, send }
}

function reduce(s: State, e: Envelope): State {
  const d = e.data

  switch (e.event) {
    case 'bootstrap':
      return {
        ...s,
        connected: true,
        version: d.version ?? '',
        workDir: d.workDir ?? '',
        llm: d.llm ?? s.llm,
        agents: d.agents ?? [],
      }

    case 'agent.created':
      if (s.agents.some((a) => a.id === d.id)) return s
      return { ...s, agents: [...s.agents, d as Agent] }

    case 'agent.deleted':
      return {
        ...s,
        agents: s.agents.filter((a) => a.id !== d.id),
        streaming: omit(s.streaming, d.id),
      }

    case 'agent.status':
      return {
        ...s,
        agents: s.agents.map((a) => (a.id === d.id ? { ...a, status: d.status } : a)),
      }

    case 'agent.delta':
      return {
        ...s,
        streaming: { ...s.streaming, [d.id]: (s.streaming[d.id] ?? '') + d.delta },
      }

    case 'agent.message':
      return {
        ...s,
        agents: s.agents.map((a) =>
          a.id === d.id ? { ...a, messages: [...a.messages, d.message] } : a,
        ),
        streaming: d.message?.role === 'assistant' ? omit(s.streaming, d.id) : s.streaming,
      }

    case 'agent.error':
      return {
        ...s,
        errors: { ...s.errors, [d.id]: d.error },
        streaming: omit(s.streaming, d.id),
      }

    case 'llm.config':
      return { ...s, llm: (d.cfg ?? d) as LLMConfig }

    case 'workdir':
      return { ...s, workDir: d.dir }

    /* ---------- 检查器事件（W3 后端接入前由 mock 驱动）---------- */
    case 'file.change':
      return {
        ...s,
        changes: upsertChange(s.changes, d as FileChange),
      }

    case 'todo.update':
      return {
        ...s,
        todos: d.items as TodoItem[],
      }

    case 'tool.start':
      return {
        ...s,
        tools: [
          {
            id: d.id,
            tool: d.tool,
            target: d.target,
            status: 'running',
            at: Date.now(),
          } as ToolCall,
          ...s.tools,
        ].slice(0, 40),
      }

    case 'tool.result':
      return {
        ...s,
        tools: s.tools.map((t) =>
          t.id === d.id
            ? { ...t, status: d.status ?? 'ok', detail: d.detail }
            : t,
        ),
      }

    case 'shell.start':
      return {
        ...s,
        shell: [
          {
            id: `${e.ts}-${Math.random().toString(36).slice(2, 7)}`,
            command: d.command,
            at: Date.now(),
          },
          ...s.shell,
        ].slice(0, 40),
      }

    case 'shell.result':
      return {
        ...s,
        shell: s.shell.map((l) =>
          l.command === d.command && !l.result ? { ...l, result: d.result } : l,
        ),
      }

    default:
      return s
  }
}

function upsertChange(list: FileChange[], next: FileChange): FileChange[] {
  const i = list.findIndex((c) => c.path === next.path)
  if (i === -1) return [next, ...list]
  const out = [...list]
  out[i] = next
  return out
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}