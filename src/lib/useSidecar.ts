import { useCallback, useEffect, useRef, useState } from 'react'
import {
  connect,
  fetchHealth,
  type Agent,
  type Connection,
  type Envelope,
  type LLMConfig,
} from './ws'
import type { ShellLog } from './ws'

type State = {
  connected: boolean
  version: string
  workDir: string
  llm: LLMConfig
  agents: Agent[]
  streaming: Record<string, string>
  errors: Record<string, string>
  shell: ShellLog[]
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
        streaming:
          d.message?.role === 'assistant'
            ? omit(s.streaming, d.id)
            : s.streaming,
      }

    case 'agent.error':
      return {
        ...s,
        errors: { ...s.errors, [d.id]: d.error },
        streaming: omit(s.streaming, d.id),
      }

    case 'llm.config':
      return { ...s, llm: (d.cfg ?? d) as LLMConfig }

    case 'shell.start':
      return {
        ...s,
        shell: [
          { id: `${e.ts}-${Math.random().toString(36).slice(2, 7)}`, command: d.command, at: Date.now() },
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

    case 'workdir':
      return { ...s, workDir: d.dir }

    default:
      return s
  }
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}