import type { ReactNode } from 'react'

export type MentionTarget = { id: string; name: string; color: string }

/**
 * MentionText 把纯文本里的 @名字 渲染成带颜色的 chip。
 *
 * 用户消息不走 markdown（纯文本语义更准），所以 @ 需要自己处理：
 * 把文本按 @提及切开，命中的名字用该 agent 的身份色标出来。
 * 未命中的 @xxx 保持原样，避免把邮箱之类误染色。
 */
function MentionText({
  text,
  agents,
  selfColor,
}: {
  text: string
  agents?: MentionTarget[]
  selfColor: string
}) {
  if (!agents || agents.length === 0 || !text.includes('@')) {
    return <>{text}</>
  }

  // 长名字优先匹配，避免 @前端 吃掉 @前端开发
  const names = [...agents].sort((a, b) => b.name.length - a.name.length)
  const byName = new Map(agents.map((a) => [a.name, a]))

  const parts: ReactNode[] = []
  let cursor = 0
  let key = 0

  for (let i = 0; i < text.length; i++) {
    if (text[i] !== '@') continue
    const hit = names.find((a) => text.startsWith(a.name, i + 1))
    if (!hit) continue

    if (i > cursor) parts.push(text.slice(cursor, i))
    const color = byName.get(hit.name)?.color ?? selfColor
    parts.push(
      <span
        key={`m-${key++}`}
        className="c-mention"
        style={{
          color,
          borderColor: `color-mix(in srgb, ${color} 40%, transparent)`,
          background: `color-mix(in srgb, ${color} 12%, transparent)`,
        }}
      >
        @{hit.name}
      </span>,
    )
    cursor = i + 1 + hit.name.length
    i = cursor - 1
  }

  if (cursor < text.length) parts.push(text.slice(cursor))
  return <>{parts}</>
}
export default MentionText
