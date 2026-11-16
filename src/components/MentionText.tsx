import type { ReactNode } from 'react'

export type MentionTarget = { id: string; name: string; color: string }

/** 匹配 @后面的提及名：允许中文、字母、数字、下划线、连字符、引号与空格前的部分 */
const AT_NAME_RE = /@([^\s@,，。.!！?？:：;；]{1,40})/g

/**
 * MentionText 把纯文本里的 @名字 渲染成 chip。
 *
 * 三种情况要区分开：
 *   1. 名字在 agents 里 → 用该 agent 的身份色
 *   2. 名字不在（agent 已被删除）→ 仍然渲染 chip，用中性色 + 删除线提示
 *      否则历史上 @过的人一旦被删，文本就退化成一串没人看得懂的字
 *   3. 邮箱之类明显不是提及的 → 保持纯文本
 */
export default function MentionText({
  text,
  agents,
  selfColor,
}: {
  text: string
  agents?: MentionTarget[]
  selfColor: string
}) {
  if (!text.includes('@')) return <>{text}</>

  const known = agents ?? []
  // 长名字优先匹配，避免 @前端 吃掉 @前端开发
  const names = [...known].sort((a, b) => b.name.length - a.name.length)

  const parts: ReactNode[] = []
  let cursor = 0
  let key = 0
  AT_NAME_RE.lastIndex = 0

  let m: RegExpExecArray | null
  while ((m = AT_NAME_RE.exec(text)) !== null) {
    const start = m.index
    // 邮箱：@ 前面紧跟字母数字 → 不是提及
    const prev = start > 0 ? text[start - 1] : ''
    if (/[A-Za-z0-9._%+-]/.test(prev)) continue

    // 先按已知名字做最长前缀匹配
    const knownHit = names.find((a) => text.startsWith(a.name, start + 1))
    const raw = m[1]
    const name = knownHit?.name ?? raw

    // 已知名字必须后面紧跟分隔符，避免 @后端开发 被 @后端 命中
    const afterIdx = start + 1 + name.length
    const after = text[afterIdx] ?? ''
    if (knownHit && after && !/[\s@,，。.!！?？:：;；]/.test(after)) continue

    if (start > cursor) parts.push(text.slice(cursor, start))

    const color = knownHit?.color ?? selfColor
    const deleted = !knownHit
    parts.push(
      <span
        key={`m-${key++}`}
        className="c-mention"
        title={deleted ? `${name}（该 agent 已不存在）` : undefined}
        style={{
          color: deleted ? 'var(--text-disabled)' : color,
          borderColor: deleted
            ? 'var(--border-default)'
            : `color-mix(in srgb, ${color} 40%, transparent)`,
          background: deleted
            ? 'var(--bg-inset)'
            : `color-mix(in srgb, ${color} 12%, transparent)`,
          textDecoration: deleted ? 'line-through' : undefined,
        }}
      >
        @{name}
      </span>,
    )
    cursor = afterIdx
    AT_NAME_RE.lastIndex = afterIdx
  }

  if (cursor < text.length) parts.push(text.slice(cursor))
  return <>{parts}</>
}
