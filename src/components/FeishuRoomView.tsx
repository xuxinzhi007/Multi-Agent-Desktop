import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { API, type Line, type Media, type Room } from '../mygo'

// 图片地址在页面生命周期内缓存一份，滚动或重渲染不必重复下载。
const imageCache = new Map<string, string>()

function MessageImage({ messageID, item }: { messageID: string; item: Media }) {
  const key = item.key ?? ''
  const [src, setSrc] = useState(() => imageCache.get(key) ?? '')
  const [err, setErr] = useState('')

  useEffect(() => {
    if (src || !key) return
    let alive = true
    API.feishuMedia(messageID, key)
      .then((url) => {
        imageCache.set(key, url)
        if (alive) setSrc(url)
      })
      .catch((e: unknown) => {
        if (alive) setErr(e instanceof Error ? e.message : String(e))
      })
    return () => {
      alive = false
    }
  }, [key, messageID, src])

  if (src) {
    return (
      <img
        src={src}
        alt={item.name || '图片'}
        style={{
          maxWidth: 260,
          maxHeight: 200,
          borderRadius: 'var(--radius-sm)',
          border: '1px solid var(--border-subtle)',
          display: 'block',
        }}
      />
    )
  }
  return (
    <div
      style={{
        maxWidth: 260,
        padding: '10px var(--space-3)',
        borderRadius: 'var(--radius-sm)',
        border: '1px dashed var(--border-subtle)',
        color: 'var(--text-tertiary)',
        fontSize: 'var(--text-xs)',
        lineHeight: 1.5,
      }}
    >
      {err ? `图片没能下载：${err}` : '图片加载中…'}
    </div>
  )
}

// LineBody 按消息类型渲染。没解析出来的类型也给一块占位，不留空行。
function LineBody({ line }: { line: Line }) {
  const kind = line.kind || 'text'
  const media = line.media ?? []
  const images = media.filter((m) => m.kind === 'image' && m.key)

  // 机器人自己发的回复和卡片正文都是 markdown，用 markdown 渲染；
  // 用户的普通消息保持纯文本，免得把 ** 之类当排版吞掉。
  const md = (text: string, color = 'var(--text-secondary)') => (
    <div
      className="markdown-body"
      style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, wordBreak: 'break-word', color }}
    >
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  )

  const muted = (
    <div
      style={{
        fontSize: 'var(--text-sm)',
        lineHeight: 1.6,
        color: 'var(--text-tertiary)',
        whiteSpace: 'pre-wrap',
        wordBreak: 'break-word',
      }}
    >
      {line.text}
    </div>
  )

  if (kind === 'image') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {images.length > 0 ? (
          images.map((m) => <MessageImage key={m.key} messageID={line.id} item={m} />)
        ) : (
          muted
        )}
      </div>
    )
  }

  if (kind === 'card') {
    // 降级卡片拿不到正文时单独标一行状态，不把占位文案当内容；
    // 能从卡片实体取回正文就直接渲染，标题和正文按 markdown 排。
    const missing = line.text.includes('[卡片内容需在飞书客户端查看]')
    const body = line.text.replace(/\n?\[卡片内容需在飞书客户端查看（card_id: [^)]+）?\]/g, '').trim()
    return (
      <div
        style={{
          borderLeft: '3px solid var(--accent-bright)',
          background: 'var(--bg-surface-active)',
          borderRadius: 'var(--radius-sm)',
          padding: '8px var(--space-3)',
        }}
      >
        <div
          style={{
            fontSize: 'var(--text-xs)',
            fontWeight: 700,
            letterSpacing: 0.6,
            color: 'var(--accent-bright)',
            marginBottom: 4,
          }}
        >
          卡片
        </div>
        {body && md(body)}
        {missing && (
          <div
            style={{
              fontSize: 'var(--text-xs)',
              lineHeight: 1.5,
              marginTop: body ? 4 : 0,
              color: 'var(--text-tertiary)',
            }}
          >
            卡片内容需在飞书客户端查看
          </div>
        )}
      </div>
    )
  }

  if (kind === 'file' || kind === 'audio' || kind === 'video') {
    return (
      <div
        style={{
          display: 'inline-block',
          maxWidth: '100%',
          padding: '6px var(--space-3)',
          border: '1px solid var(--border-subtle)',
          borderRadius: 'var(--radius-sm)',
          fontSize: 'var(--text-sm)',
          color: 'var(--text-secondary)',
          wordBreak: 'break-all',
        }}
      >
        {line.text}
      </div>
    )
  }

  if (kind === 'post') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {line.self ? (
          md(line.text)
        ) : (
          <div
            style={{
              fontSize: 'var(--text-md)',
              lineHeight: 1.7,
              wordBreak: 'break-word',
              color: 'var(--text-secondary)',
              whiteSpace: 'pre-wrap',
            }}
          >
            {line.text}
          </div>
        )}
        {images.map((m) => <MessageImage key={m.key} messageID={line.id} item={m} />)}
      </div>
    )
  }

  if (kind === 'text') {
    return line.self ? md(line.text) : (
      <div
        style={{
          fontSize: 'var(--text-md)',
          lineHeight: 1.7,
          wordBreak: 'break-word',
          color: 'var(--text-secondary)',
          whiteSpace: 'pre-wrap',
        }}
      >
        {line.text}
      </div>
    )
  }

  // system / share / sticker / forward / unsupported 等：统一用弱化样式，
  // 但文字本身要看得懂这是什么消息。
  return muted
}

export default function FeishuRoomView({ room }: { room: Room }) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const lines = room.messages ?? []
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [room.id, lines.length])

  const pull = () => {
    setBusy(true)
    setNote('')
    API.syncFeishuChat(room.id)
      .then((n) => setNote(n > 0 ? `拉取到 ${n} 条新消息` : '没有新消息'))
      .catch((e: unknown) => setNote(`拉取失败：${e instanceof Error ? e.message : String(e)}`))
      .finally(() => setBusy(false))
  }

  return (
    <div className="c-col" style={{ flex: 1 }}>
      <header
        style={{
          display: 'flex',
          alignItems: 'center',
          height: 52,
          gap: 'var(--space-2)',
          padding: '0 var(--space-4)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span
          style={{
            width: 24,
            height: 24,
            borderRadius: 'var(--radius-sm)',
            background: 'var(--accent-dim)',
            color: 'var(--accent-bright)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 'var(--text-sm)',
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          飞
        </span>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, lineHeight: 1.3 }}>
            {room.name || '飞书会话'}
            {room.readOnly && (
              <span
                style={{
                  marginLeft: 8,
                  fontSize: 'var(--text-xs)',
                  fontWeight: 600,
                  color: 'var(--text-tertiary)',
                  border: '1px solid var(--border-subtle)',
                  borderRadius: 4,
                  padding: '1px 5px',
                  verticalAlign: 'middle',
                }}
              >
                只读
              </span>
            )}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {note ||
              (room.readOnly
                ? '机器人不在这个群：默认不自动同步、不自动回答，点右侧手动放行一次。'
                : `由${room.agentName || '本机会话'}回答。这里只能看记录。`)}
          </div>
        </div>
        <button
          type="button"
          className="c-btn c-btn--ghost"
          style={{ marginLeft: 'auto' }}
          disabled={busy}
          title="手动放行一次：只读拉取，不会自动同步和回答"
          onClick={pull}
        >
          {busy ? '拉取中…' : '从飞书拉取'}
        </button>
      </header>
      <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4)' }}>
        {/* 和主对话用同一个阅读宽度：行太长时眼睛回不了行。 */}
        <div className="c-transcript">
        {lines.length === 0 ? (
          <div className="c-empty">还没有记下消息</div>
        ) : (
          lines.map((line) => {
            const name = line.self ? room.botName || '机器人' : line.from || '有人'
            return (
              <div key={line.id} style={{ display: 'flex', gap: 'var(--space-3)', marginBottom: 'var(--space-4)' }}>
                <span
                  style={{
                    width: 22,
                    height: 22,
                    borderRadius: 'var(--radius-sm)',
                    background: line.self ? 'var(--accent-bright)' : 'var(--bg-surface-active)',
                    color: line.self ? 'var(--bg-base)' : 'var(--text-secondary)',
                    display: 'grid',
                    placeItems: 'center',
                    fontSize: 'var(--text-xs)',
                    fontWeight: 700,
                    flexShrink: 0,
                    marginTop: 1,
                  }}
                >
                  {name.slice(0, 1)}
                </span>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 6,
                      marginBottom: 3,
                      fontSize: 'var(--text-xs)',
                      fontWeight: 600,
                      color: line.self ? 'var(--accent-bright)' : 'var(--text-tertiary)',
                    }}
                  >
                    {name}
                    {line.at > 0 && (
                      <span style={{ marginLeft: 'auto', fontWeight: 400, color: 'var(--text-disabled)' }}>
                        {new Date((line.at > 1e12 ? line.at : line.at * 1000)).toLocaleTimeString('zh-CN', {
                          hour: '2-digit',
                          minute: '2-digit',
                        })}
                      </span>
                    )}
                  </div>
                  <LineBody line={line} />
                </div>
              </div>
            )
          })
        )}
        </div>
      </div>
    </div>
  )
}
