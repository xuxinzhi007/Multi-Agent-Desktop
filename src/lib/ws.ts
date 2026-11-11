export type AgentStatus = 'idle' | 'running' | 'done' | 'error'

export type Message = {
  role: 'system' | 'user' | 'assistant'
  content: string
}

export type Agent = {
  id: string
  name: string
  role: string
  color: string
  status: AgentStatus
  createdAt: number
  messages: Message[]
}

export type LLMConfig = {
  provider: string
  baseUrl: string
  apiKey: string
  model: string
}

export type ShellResult = {
  stdout: string
  stderr: string
  exitCode: number
}

export type ShellLog = {
  id: string
  command: string
  at: number
  result?: ShellResult
}

export type Envelope = {
  event: string
  data?: any
  ts?: number
}

export const SIDECAR_HTTP = 'http://127.0.0.1:17389'
export const SIDECAR_WS = 'ws://127.0.0.1:17389/ws'

export function fetchHealth() {
  return fetch(`${SIDECAR_HTTP}/healthz`).then((r) => {
    if (!r.ok) throw new Error(`healthz ${r.status}`)
    return r.json()
  })
}

type Handlers = {
  onEvent: (e: Envelope) => void
  onOpen?: () => void
  onClose?: () => void
}

export type Connection = {
  send: (action: string, data?: unknown) => void
  close: () => void
}

export function connect(h: Handlers): Connection {
  let ws: WebSocket | null = null
  let closed = false
  let retry = 0

  const open = () => {
    if (closed) return
    ws = new WebSocket(SIDECAR_WS)

    ws.onopen = () => {
      retry = 0
      h.onOpen?.()
    }

    ws.onmessage = (m) => {
      try {
        h.onEvent(JSON.parse(m.data))
      } catch {
        // 忽略非 JSON 帧
      }
    }

    ws.onclose = () => {
      if (closed) return
      h.onClose?.()
      retry = Math.min(retry + 1, 10)
      setTimeout(open, 400 * retry)
    }

    ws.onerror = () => ws?.close()
  }

  open()

  return {
    send: (action, data) => {
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ action, data }))
      }
    },
    close: () => {
      closed = true
      ws?.close()
    },
  }
}