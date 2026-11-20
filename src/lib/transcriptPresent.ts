export type ShellRow = {
  id: string
  command: string
  at: number
  result?: { stdout: string; stderr: string; exitCode: number; timedOut?: boolean }
}

export type ShellGroup =
  | { kind: 'cd'; path: string; count: number; ids: string[] }
  | {
      kind: 'cmd'
      id: string
      shown: string
      at: number
      pending: boolean
      exitCode?: number
      timedOut?: boolean
      output: string
      masked: boolean
    }

export type RoundMsg = {
  role: string
  content?: string
  reasoning?: string
  toolCalls?: readonly unknown[]
}

export type FoldedRound<T> = { item: T; sources: T[] }

/** 工具回合的正文是过程句，不进对话。思考并到下一条真正的回复上。 */
export function foldAssistantTranscript<T extends RoundMsg>(messages: T[]): FoldedRound<T>[] {
  const out: FoldedRound<T>[] = []
  let sources: T[] = []
  let reasons: string[] = []

  const emitFold = () => {
    if (sources.length === 0) return
    out.push({
      item: { role: 'assistant', content: '', reasoning: reasons.join('\n\n') } as T,
      sources: sources.slice(),
    })
    sources = []
    reasons = []
  }

  for (const m of messages) {
    if (m.role === 'system') {
      if ((m.content || '').startsWith('运行错误：')) {
        emitFold()
        out.push({ item: m, sources: [m] })
      }
      continue
    }
    if (m.role === 'tool') continue
    const fold = m.role === 'assistant' && ((m.toolCalls?.length ?? 0) > 0 || !(m.content || '').trim())
    if (fold) {
      sources.push(m)
      if (m.reasoning) reasons.push(m.reasoning)
      continue
    }
    if (m.role === 'assistant') {
      const parts = reasons.filter((r) => r && !(m.reasoning || '').includes(r))
      if (m.reasoning) parts.push(m.reasoning)
      const reasoning = parts.join('\n\n')
      out.push({ item: reasoning ? { ...m, reasoning } : m, sources: [...sources, m] })
      sources = []
      reasons = []
      continue
    }
    emitFold()
    out.push({ item: m, sources: [m] })
  }
  emitFold()
  return out
}

export function redactCommand(input: string): string {
  let s = input.replace(/Bearer\s+[A-Za-z0-9._\-]+/gi, (m) => {
    const token = m.replace(/^Bearer\s+/i, '')
    return 'Bearer ••••' + token.slice(-4)
  })
  s = s.replace(/\bsk-[A-Za-z0-9._\-]+/g, (m) => 'sk-••••' + m.slice(-4))
  s = s.replace(/\bpt-[A-Za-z0-9._\-]+/g, (m) => 'pt-••••' + m.slice(-4))
  s = s.replace(/api_key=[^\s&]+/gi, (m) => {
    const v = m.slice(m.indexOf('=') + 1)
    return 'api_key=••••' + v.slice(-4)
  })
  return s
}

export function groupShellLogs(logs: ShellRow[]): ShellGroup[] {
  const out: ShellGroup[] = []
  for (const log of logs) {
    const cd = log.command.match(/^cd\s+(\S+)$/)
    const prev = out[out.length - 1]
    if (cd && prev && prev.kind === 'cd' && prev.path === cd[1]) {
      prev.count += 1
      prev.ids.push(log.id)
      continue
    }
    if (cd) {
      out.push({ kind: 'cd', path: cd[1], count: 1, ids: [log.id] })
      continue
    }
    const output = log.result
      ? redactCommand([log.result.stdout, log.result.stderr].filter(Boolean).join('\n').trimEnd())
      : ''
    out.push({
      kind: 'cmd',
      id: log.id,
      shown: redactCommand(log.command),
      at: log.at,
      pending: !log.result,
      exitCode: log.result?.exitCode,
      timedOut: !!log.result?.timedOut,
      output,
      masked: redactCommand(log.command) !== log.command,
    })
  }
  return out
}
