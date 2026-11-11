const SIDECAR_HTTP = 'http://127.0.0.1:17389'
const SIDECAR_WS = 'ws://127.0.0.1:17389/ws'

export type SidecarEvent = {
  event: string
  data?: unknown
  ts?: number
}

export type Health = {
  ok: boolean
  service: string
  clients: number
  ts: number
}

export async function fetchHealth(): Promise<Health> {
  const res = await fetch(`${SIDECAR_HTTP}/healthz`)
  if (!res.ok) throw new Error(`healthz ${res.status}`)
  return res.json()
}

export function connect(onEvent: (e: SidecarEvent) => void): () => void {
  const ws = new WebSocket(SIDECAR_WS)
  ws.onmessage = (m) => {
    try {
      onEvent(JSON.parse(m.data))
    } catch {
      // 非 JSON 帧直接忽略
    }
  }
  return () => ws.close()
}

export { SIDECAR_HTTP, SIDECAR_WS }