import type { ChannelMessage, Message } from '../mygo'

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

/**
 * 消息时间戳归并排序（u1）。
 *
 * 口径：
 *  - 会话消息用 `timestamp`（Unix 秒），频道消息用 `at`（Unix 秒）；
 *  - 服务端消息自带时间戳以它为准，本地新发的用发送时刻；
 *  - 按时间升序，**同时间戳保持到达顺序**（稳定排序，tiebreak 用原始下标）；
 *  - 时间戳缺失 / 为 0 的消息一律排最前：视为更早的历史或 system 占位，
 *    不参与时间比较；同批内仍保持到达顺序。
 *
 * 这是消息层唯一的排序入口：useBackend 在每个消息更新点（事件回推、历史拉取、
 * 补拉、乐观插入）统一走这里渲染；组件层不要再各排各的。
 */
function stableSortBy<T>(list: T[], key: (m: T) => number | undefined): T[] {
  const norm = (t: number | undefined): number => {
    const n = Number(t)
    return Number.isFinite(n) && n > 0 ? n : -Infinity
  }
  return list
    .map((m, i) => ({ m, i }))
    .sort((a, b) => {
      const ta = norm(key(a.m))
      const tb = norm(key(b.m))
      if (ta !== tb) return ta - tb
      return a.i - b.i
    })
    .map((x) => x.m)
}

/** 会话消息按 timestamp 稳定排序。 */
export function sortAgentMessages(list: Message[]): Message[] {
  return stableSortBy(list, (m) => m.timestamp)
}

/** 频道消息按 at 稳定排序。 */
export function sortChannelMessages(list: ChannelMessage[]): ChannelMessage[] {
  return stableSortBy(list, (m) => m.at)
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
