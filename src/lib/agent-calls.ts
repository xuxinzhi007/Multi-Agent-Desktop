export type PeerCall = {
  name: string
  reply: string
  ok: boolean
}

export type Caller = { id: string; name: string; color: string }

type CallMsg = {
  role?: string
  content?: string
  toolId?: string
  toolCalls?: { id?: string; name?: string; input?: unknown }[]
}

function recordOf(input: unknown): Record<string, unknown> | null {
  if (!input) return null
  if (typeof input === 'string') {
    const text = input.trim()
    if (!text) return null
    try {
      return recordOf(JSON.parse(text))
    } catch {
      return null
    }
  }
  if (typeof input === 'object') return input as Record<string, unknown>
  return null
}

function field(input: unknown, key: string): string {
  const value = recordOf(input)?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function stripOutput(raw: string): string {
  const hit = raw.match(/<tool_output[^>]*>\n?([\s\S]*?)\n?<\/tool_output>/)
  return (hit ? hit[1] : raw).trim()
}

export function peerCallsOf(message: CallMsg, byId: Map<string, string>): PeerCall[] {
  const out: PeerCall[] = []
  for (const call of message.toolCalls ?? []) {
    if (call.name !== 'ask_agent' && call.name !== 'create_agent') continue
    const name = field(call.input, 'name')
    if (!name) continue
    const raw = (call.id && byId.get(call.id)) || ''
    out.push({
      name,
      reply: stripOutput(raw),
      ok: !/status="error"/.test(raw),
    })
  }
  return out
}

export function toolTextById(messages: CallMsg[]): Map<string, string> {
  const byId = new Map<string, string>()
  for (const message of messages) {
    if (message.role === 'tool' && message.toolId) byId.set(message.toolId, message.content || '')
  }
  return byId
}

/** 用别的会话里的 ask_agent / create_agent 参数，认出「这条用户消息其实是谁发来的」。 */
export function indexIncoming(
  rows: { id: string; name: string; color: string; messages?: CallMsg[] }[],
): Record<string, Caller> {
  const out: Record<string, Caller> = {}
  for (const row of rows) {
    for (const message of row.messages ?? []) {
      for (const call of message.toolCalls ?? []) {
        if (call.name !== 'ask_agent' && call.name !== 'create_agent') continue
        const task = field(call.input, 'task')
        if (!task) continue
        out[task] = { id: row.id, name: row.name, color: row.color }
      }
    }
  }
  return out
}
