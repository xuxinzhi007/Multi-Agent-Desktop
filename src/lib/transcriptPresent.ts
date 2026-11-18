export type ShellRow = {
  id: string
  command: string
  at: number
  result?: { stdout: string; stderr: string; exitCode: number }
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
      output: string
      masked: boolean
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
      output,
      masked: redactCommand(log.command) !== log.command,
    })
  }
  return out
}
