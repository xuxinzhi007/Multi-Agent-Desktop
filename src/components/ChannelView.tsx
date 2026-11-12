import { useEffect, useRef, useState } from 'react'
import type { AgentView as Agent, Channel, ChannelMessage } from '../mygo'

type Props = {
  channel: Channel | null
  agents: Agent[]
  onSend: (text: string) => void
  onDelete?: () => void
}

/**
 * ChannelView 是「群聊」视图：一个频道里的消息对所有成员广播。
 * 与 AgentView 的区别是消息带发言人身份，且成员都是 agent。
 */
export default function ChannelView({ channel, agents, onSend, onDelete }: Props) {
  const [input, setInput] = useState('')
  const [showMentions, setShowMentions] = useState(false)
  const [mentionQuery, setMentionQuery] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [channel?.messages.length])

  useEffect(() => {
    taRef.current?.focus()
  }, [channel?.id])

  if (!channel) {
    return (
      <div className="c-col" style={{ flex: 1 }}>
        <div className="c-empty" style={{ flex: 1 }}>
          <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }}>#</div>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>
            还没有频道
          </div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            在左侧点「+ 新建频道」，把多个 Agent 拉进同一个群聊
          </div>
        </div>
      </div>
    )
  }

  const members = agents.filter((a) => channel.members.includes(a.id))
  const memberById = (id: string) => agents.find((a) => a.id === id)

  const submit = () => {
    const text = input.trim()
    if (!text) return
    onSend(text)
    setInput('')
    setShowMentions(false)
  }

  const handleInputChange = (value: string) => {
    setInput(value)
    const atIndex = value.lastIndexOf('@')
    if (atIndex !== -1) {
      const query = value.slice(atIndex + 1)
      if (!query.includes(' ')) {
        setMentionQuery(query)
        setShowMentions(true)
        return
      }
    }
    setShowMentions(false)
  }

  const insertMention = (name: string) => {
    const atIndex = input.lastIndexOf('@')
    setInput(input.slice(0, atIndex) + '@' + name + ' ')
    setShowMentions(false)
    taRef.current?.focus()
  }

  const filtered = members.filter(
    (a) =>
      a.name.toLowerCase().includes(mentionQuery.toLowerCase()) ||
      a.id.toLowerCase().includes(mentionQuery.toLowerCase()),
  )

  return (
    <div className="c-col" style={{ flex: 1 }}>
      <header
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
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
            fontSize: 13,
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          #
        </span>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, lineHeight: 1.3 }}>
            {channel.name}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {members.length} 个成员 · 广播模式
          </div>
        </div>

        {/* 成员头像 */}
        <div style={{ display: 'flex', marginLeft: 12 }}>
          {members.slice(0, 6).map((m) => (
            <span
              key={m.id}
              title={m.name}
              style={{
                width: 18,
                height: 18,
                borderRadius: 'var(--radius-full)',
                background: m.color,
                color: 'var(--bg-base)',
                display: 'grid',
                placeItems: 'center',
                fontSize: 9,
                fontWeight: 700,
                marginLeft: -4,
                border: '1.5px solid var(--bg-chrome)',
              }}
            >
              {m.name.slice(0, 1).toUpperCase()}
            </span>
          ))}
        </div>

        {onDelete && (
          <button
            className="c-icon-btn c-icon-btn--danger"
            style={{ marginLeft: 'auto' }}
            onClick={() => {
              if (confirm(`删除频道「${channel.name}」？消息将一并清除。`)) onDelete()
            }}
            title="删除频道"
          >
            ×
          </button>
        )}
      </header>

      <div
        ref={scrollRef}
        style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4) var(--space-6)' }}
      >
        {(channel.messages ?? []).length === 0 && (
          <div className="c-empty" style={{ paddingTop: 'var(--space-6)' }}>
            频道已创建，发第一条消息吧
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 8, color: 'var(--text-disabled)' }}>
              消息会广播给所有成员 Agent，它们并行回复
            </div>
          </div>
        )}
        {(channel.messages ?? []).map((m) => (
          <ChannelBubble key={m.id} msg={m} agent={memberById(m.fromId)} />
        ))}
      </div>

      <div style={{ padding: 'var(--space-3) var(--space-6) var(--space-5)', flexShrink: 0 }}>
        <div
          style={{
            position: 'relative',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-lg)',
          }}
        >
          <textarea
            ref={taRef}
            value={input}
            onChange={(e) => handleInputChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                submit()
              }
            }}
            placeholder={`发消息到 #${channel.name}…  输入 @ 提及成员`}
            rows={1}
            style={{
              width: '100%',
              resize: 'none',
              border: 'none',
              background: 'transparent',
              padding: '11px 13px 4px',
              lineHeight: 'var(--leading-normal)',
              fontSize: 'var(--text-md)',
              minHeight: 24,
              maxHeight: 120,
              overflowY: 'auto',
            }}
            onInput={(e) => {
              const t = e.target as HTMLTextAreaElement
              t.style.height = 'auto'
              t.style.height = Math.min(t.scrollHeight, 120) + 'px'
            }}
          />

          {showMentions && filtered.length > 0 && (
            <div
              className="fade-up"
              style={{
                position: 'absolute',
                bottom: '100%',
                left: 13,
                right: 13,
                marginBottom: 4,
                background: 'var(--bg-elevated)',
                border: '1px solid var(--border-default)',
                borderRadius: 'var(--radius-md)',
                boxShadow: 'var(--shadow-lg)',
                maxHeight: 180,
                overflowY: 'auto',
                zIndex: 50,
              }}
            >
              {filtered.map((a) => (
                <button
                  key={a.id}
                  onClick={() => insertMention(a.name)}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    width: '100%',
                    padding: '8px 12px',
                    textAlign: 'left',
                    fontSize: 'var(--text-sm)',
                    color: 'var(--text-primary)',
                    background: 'transparent',
                    borderBottom: '1px solid var(--border-subtle)',
                    cursor: 'pointer',
                  }}
                >
                  <span
                    style={{
                      width: 16,
                      height: 16,
                      borderRadius: 'var(--radius-sm)',
                      background: a.color,
                      display: 'grid',
                      placeItems: 'center',
                      fontSize: 9,
                      fontWeight: 700,
                      color: 'var(--bg-base)',
                    }}
                  >
                    {a.name.slice(0, 1).toUpperCase()}
                  </span>
                  {a.name}
                </button>
              ))}
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', padding: '5px 8px 7px' }}>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
              广播给 {members.length} 个成员
            </span>
            <button
              className="c-btn c-btn--primary c-btn--sm"
              style={{ marginLeft: 'auto' }}
              onClick={submit}
              disabled={!input.trim()}
            >
              发送
            </button>
          </div>
        </div>
        <div
          style={{
            marginTop: 6,
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
          }}
        >
          Enter 发送 · Shift+Enter 换行
        </div>
      </div>
    </div>
  )
}

function ChannelBubble({ msg, agent }: { msg: ChannelMessage; agent?: Agent }) {
  const color = agent?.color ?? 'var(--text-tertiary)'
  const name = msg.fromName || agent?.name || msg.fromId
  const isUser = msg.fromId === 'user'

  return (
    <div style={{ display: 'flex', gap: 'var(--space-3)', marginBottom: 'var(--space-4)' }}>
      <span
        style={{
          width: 22,
          height: 22,
          borderRadius: 'var(--radius-sm)',
          background: isUser ? 'var(--bg-surface-active)' : color,
          color: isUser ? 'var(--text-secondary)' : 'var(--bg-base)',
          display: 'grid',
          placeItems: 'center',
          fontSize: 11,
          fontWeight: 700,
          flexShrink: 0,
          marginTop: 1,
        }}
      >
        {name.slice(0, 1).toUpperCase()}
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
            color: isUser ? 'var(--text-disabled)' : color,
          }}
        >
          {name}
          <span style={{ marginLeft: 'auto', fontWeight: 400, color: 'var(--text-disabled)' }}>
            {new Date(msg.at * 1000).toLocaleTimeString('zh-CN', {
              hour: '2-digit',
              minute: '2-digit',
            })}
          </span>
        </div>
        <div
          style={{
            fontSize: 'var(--text-md)',
            lineHeight: 1.7,
            wordBreak: 'break-word',
            color: 'var(--text-secondary)',
            whiteSpace: 'pre-wrap',
          }}
        >
          {msg.text}
        </div>
      </div>
    </div>
  )
}
