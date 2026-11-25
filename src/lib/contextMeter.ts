/** 和 internal/llm/compact.go 的 ContextLimit 保持同一组默认值。 */
export function contextLimit(provider: string, configured?: number, model?: string): number {
  if (configured && configured > 0) return configured
  switch (provider) {
    case 'llamacpp':
    case 'ollama':
      return 8192
    case 'deepseek':
      return 65536
    case 'qoder':
      return 131072
    case 'openai':
    case 'claude': {
      const m = (model || '').toLowerCase()
      if (provider === 'openai') {
        if (m.includes('gpt-4o') || m.includes('gpt-4.1') || m.includes('gpt-5')) return 131072
        if (m.includes('gpt-4-mini') || m.includes('gpt-3.5')) return 16384
      } else {
        if (m.includes('claude-3') || m.includes('claude-4') || m.includes('claude-opus') || m.includes('claude-sonnet') || m.includes('claude-haiku')) return 200000
      }
      return 32768
    }
    default:
      return 8192
  }
}

/** 和 llm.EstTokens 同一口径：中文一字一个，英文大约四字一个。 */
export function estimateTokens(text: string): number {
  if (!text) return 0
  let ascii = 0
  let other = 0
  for (const ch of text) {
    if (ch.codePointAt(0)! < 128) ascii++
    else other++
  }
  return other + Math.ceil(ascii / 4)
}

export type TokenUsage = { in: number; out: number; lastIn?: number }

export function contextMeter(input: {
  messages: { content?: string }[]
  streaming?: string
  usage?: TokenUsage
  provider?: string
  contextWindow?: number
}): { used: number; limit: number; spent: number; estimated: boolean } {
  const limit = contextLimit(input.provider || '', input.contextWindow)
  const lastIn = input.usage?.lastIn ?? 0
  const estimated = lastIn <= 0
  let used = lastIn
  if (estimated) {
    used = input.messages.reduce((n, m) => n + estimateTokens(m.content || '') + 4, 0)
    used += estimateTokens(input.streaming || '')
  }
  const spent = (input.usage?.in ?? 0) + (input.usage?.out ?? 0)
  return { used, limit, spent, estimated }
}
