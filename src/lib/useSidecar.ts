import { useCallback, useEffect, useState } from 'react'
import { Sidecar, events, type Agent, type Config, type ShellEvent, type ShellResultEvent } from '../mygo'
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
  streaming: Record<string, string>
  errors: Record<string, string>
  shell: ShellLog[]
  changes: FileChange[]
  todos: TodoItem[]
  tools: ToolCall[]
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
    sub(events.agentDelta, ({ id, delta }) =>
      setState((s) => ({ ...s, streaming: { ...s.streaming, [id]: (s.streaming[id] ?? '') + delta } })),
    )
    sub(events.agentMessage, ({ id, message }) =>
      setState((s) => ({
        ...s,
        agents: s.agents.map((a) => (a.id === id ? { ...a, messages: [...a.messages, message] } : a)),
        streaming: message.role === 'assistant' ? omit(s.streaming, id) : s.streaming,
      })),
    )
    sub(events.agentError, ({ id, error }) =>
      setState((s) => ({ ...s, errors: { ...s.errors, [id]: error }, streaming: omit(s.streaming, id) })),
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
      case 'agent.delete':
        return Sidecar.deleteAgent((data as { id: string }).id)
      case 'agent.stop':
        return Sidecar.stopAgent((data as { id: string }).id)
      case 'agent.prompt':
        return Sidecar.promptAgent(data as { agentId: string; text: string })
      case 'shell.run':
        return Sidecar.runShell(data as { agentId: string; command: string })
      case 'llm.config':
        return Sidecar.setLLMConfig(data as Config)
      case 'workdir.set':
        return Sidecar.setWorkDir((data as { dir: string }).dir)
    }
  }, [])

  return { ...state, send }
}

function omit<T extends Record<string, unknown>>(obj: T, key: string): T {
  const out = { ...obj }
  delete out[key]
  return out
}