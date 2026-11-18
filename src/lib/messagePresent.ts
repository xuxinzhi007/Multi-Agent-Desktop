export type AnchorPart =
  | { kind: 'text'; text: string }
  | { kind: 'anchor'; text: string; path: string; line: number; end?: number }

export type Artifact =
  | { kind: 'file'; path: string; label: string }
  | { kind: 'note'; label: string }

const ANCHOR_RE =
  /((?:[\w.-]+\/)*[\w.-]+\.(?:go|ts|tsx|js|jsx|py|md|css|json|ya?ml|rs|sql)):(\d+)(?:-(\d+))?/g

const MD_RE = /(?:[\w.-]+\/)*[\w.-]+\.md\b/g

export function splitAnchors(text: string): AnchorPart[] {
  const out: AnchorPart[] = []
  let last = 0
  for (const m of text.matchAll(ANCHOR_RE)) {
    const index = m.index ?? 0
    if (index > last) out.push({ kind: 'text', text: text.slice(last, index) })
    const end = m[3] ? Number(m[3]) : undefined
    out.push({
      kind: 'anchor',
      text: m[0],
      path: m[1],
      line: Number(m[2]),
      end,
    })
    last = index + m[0].length
  }
  if (last < text.length) out.push({ kind: 'text', text: text.slice(last) })
  if (out.length === 0) out.push({ kind: 'text', text })
  return out
}

export function extractArtifacts(content: string): Artifact[] {
  const out: Artifact[] = []
  const seen = new Set<string>()
  for (const m of content.matchAll(MD_RE)) {
    const path = m[0]
    if (seen.has('file:' + path)) continue
    seen.add('file:' + path)
    const label = path.split('/').pop() || path
    out.push({ kind: 'file', path, label })
  }
  if (/\bsave_note\b/.test(content)) {
    const quoted = content.match(/save_note[\s\S]{0,160}?["']([^"'\n]{1,60})["']/)
    out.push({ kind: 'note', label: quoted?.[1] ? `知识库 · ${quoted[1]}` : '知识库笔记' })
  }
  return out
}

export function workLabel(dir: string): string {
  if (!dir) return ''
  const norm = dir.replace(/\\/g, '/')
  const at = norm.indexOf('worktree/')
  if (at >= 0) return norm.slice(at)
  const parts = norm.split('/').filter(Boolean)
  return parts.slice(-2).join('/')
}

export function agentTone(index: number): number {
  const n = index < 0 ? 0 : index
  return (n % 6) + 1
}

export function elapsedBetween(from?: number, to?: number): string {
  if (!from || !to || to < from) return ''
  const s = to - from
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const r = s % 60
  return r ? `${m}m ${r}s` : `${m}m`
}

export function prevUserStamp(list: { role: string; timestamp?: number }[], index: number): number | undefined {
  for (let i = index - 1; i >= 0; i--) {
    if (list[i].role === 'user' && list[i].timestamp) return list[i].timestamp
  }
  return undefined
}
