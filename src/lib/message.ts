/**
 * 消息文本的内部标记清理。
 *
 * 历史上 prompt 里拼过几类标记，它们混进了真实的对话历史：
 *   [from agent-x] / [to agent-x]      跨 agent 派发
 *   [ch-1794486860541966000]           旧版把频道 ID 拼进 prompt
 *   [user] / [assistant]               旧版 speaker 前缀
 *   你: / 前端:                         旧版 speaker 名字前缀
 *
 * 这些都不该出现在用户眼前，但老数据不会因为代码改进而消失，
 * 只能靠渲染层统一兜住。所有展示消息的地方都必须走这里。
 */

const ROUTE_RE = /^\[(from|to)\s+([^\]]+)\]\s*/
const CHANNEL_ID_RE = /^\[ch-\d+\]\s*/
const SPEAKER_RE = /^\[(user|assistant|system)\]\s*/i
/** 群聊投递时的「名字: 内容」前缀，仅在后面还有内容时剥掉 */
const NAME_PREFIX_RE = /^[^:：\n]{1,24}[:：]\s*/

export type ParsedMessage = {
  body: string
  /** 跨 agent 派发方向，用于显示「来自 X / 发给 Y」 */
  from?: string
  to?: string
  isRouted: boolean
}

export function parseMessage(content: string): ParsedMessage {
  let body = content
  let from: string | undefined
  let to: string | undefined

  // 标记可能以任意顺序出现在开头，循环剥干净
  for (let i = 0; i < 6; i++) {
    const r = body.match(ROUTE_RE)
    if (r) {
      if (r[1] === 'from') from = r[2]
      else to = r[2]
      body = body.slice(r[0].length)
      continue
    }
    const ch = body.match(CHANNEL_ID_RE)
    if (ch) {
      body = body.slice(ch[0].length)
      continue
    }
    const sp = body.match(SPEAKER_RE)
    if (sp) {
      body = body.slice(sp[0].length)
      continue
    }
    break
  }

  // 「你: 在吗」这类 speaker 前缀：只在剥完还剩内容时才剥，
  // 且限制前缀长度，避免把「注意: xxx」这种正文误伤
  const np = body.match(NAME_PREFIX_RE)
  if (np && body.slice(np[0].length).trim() !== '') {
    body = body.slice(np[0].length)
  }

  const isRouted = !!(from || to)
  return { body, from, to, isRouted }
}

/** bodyOf 只取正文，用于列表预览。 */
export function bodyOf(content: string): string {
  return parseMessage(content).body
}

/** truncate 截断到 n 个字符，超出加省略号。 */
export function truncate(text: string, n: number): string {
  const t = text.trim()
  return t.length > n ? `${t.slice(0, n)}…` : t
}
