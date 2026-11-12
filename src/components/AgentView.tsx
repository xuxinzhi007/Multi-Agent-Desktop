import { useEffect, useRef, useState } from 'react'
import type { Agent, Message } from '../lib/mygo'

type Props = {
  agent: Agent | null
  streaming: string
  error?: string
  workDir: string
  onPrompt: (text: string) => void
  onStop: () => void
  onShell: (command: string) => void
  onToggleInspector: () => void
  inspectorOpen: boolean
  onRoute?: (targetName: string, text: string) => void
  agents?: Agent[]
}

export default function AgentView(p: Props) {
  const [input, setInput] = useState('')
  const [mode, setMode] = useState<'chat' | 'shell'>('chat')
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; msg: Message } | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const pinnedToBottom = useRef(true)

  // 右键菜单 handler
  const handleContextMenu = (e: React.MouseEvent, msg: Message) => {
    e.preventDefault()
    setContextMenu({ x: e.clientX, y: e.clientY, msg })
  }

  const closeContextMenu = () => setContextMenu(null)

  const copyMessage = (msg: Message) => {
    navigator.clipboard.writeText(msg.content)
    closeContextMenu()
  }

  const regenerateMessage = (msg: Message) => {
    // 重新生成：把消息内容作为新 prompt 发给当前 agent
    if (msg.role === 'assistant') {
      p.onPrompt(msg.content)
    }
    closeContextMenu()
  }

  const quoteToAgent = (msg: Message, targetAgentId: string) => {
    const target = p.agents?.find((a) => a.id === targetAgentId)
    if (target && p.onRoute) {
      p.onRoute(target.id, `引用：${msg.content.slice(0, 200)}`)
    }
    closeContextMenu()
  }

  // 跟踪用户是否在底部（决定是否自动跟随滚动）
  const handleScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const threshold = 40 // px，距底部多少内算「在底部」
    pinnedToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < threshold
  }

  useEffect(() => {
    const el = scrollRef.current
    if (el && pinnedToBottom.current) {
      el.scrollTop = el.scrollHeight
    }
  }, [p.agent?.messages.length, p.streaming])

  useEffect(() => {
    taRef.current?.focus()
  }, [p.agent?.id, mode])

  if (!p.agent) {
    return (
      <div className="c-col" style={{ flex: 1 }}>
        <div className="c-empty" style={{ flex: 1 }}>
          <div style={{ fontSize: 32, opacity: 0.18, marginBottom: 'var(--space-4)' }}>◇</div>
          左侧新建 agent，或点击已有会话
          <div style={{ fontSize: 'var(--text-xs)', marginTop: 'var(--space-3)' }}>
            多个 agent 可同时运行，各自隔离
          </div>
        </div>
      </div>
    )
  }

  const [showMentions, setShowMentions] = useState(false)
  const [mentionQuery, setMentionQuery] = useState('')
  const [selectedMentions, setSelectedMentions] = useState<string[]>([])

  // 解析 @提及（支持多选）
  const parseMentions = (text: string): { targets: string[]; message: string } => {
    const mentionRegex = /@(\S+)/g
    const targets: string[] = []
    let match
    while ((match = mentionRegex.exec(text)) !== null) {
      targets.push(match[1])
    }
    const message = text.replace(mentionRegex, '').trim()
    return { targets, message }
  }

  const submit = () => {
    const text = input.trim()
    if (!text) return
    if (mode === 'shell') {
      p.onShell(text)
    } else {
      const { targets, message } = parseMentions(text)
      if (targets.length > 0 && p.onRoute && p.agents) {
        // 多选 @：发给多个 agent
        targets.forEach((targetName) => {
          const target = p.agents!.find((a) => a.name === targetName || a.id === targetName)
          if (target) {
            p.onRoute!(target.id, message || text)
          }
        })
      } else {
        p.onPrompt(text)
      }
    }
    setInput('')
    setSelectedMentions([])
  }

  // 输入时检测 @ 弹出补全
  const handleInputChange = (value: string) => {
    setInput(value)
    const atIndex = value.lastIndexOf('@')
    if (atIndex !== -1) {
      const query = value.slice(atIndex + 1)
      if (!query.includes(' ')) {
        setMentionQuery(query)
        setShowMentions(true)
      } else {
        setShowMentions(false)
      }
    } else {
      setShowMentions(false)
    }
  }

  const insertMention = (agentName: string) => {
    const atIndex = input.lastIndexOf('@')
    const newInput = input.slice(0, atIndex) + '@' + agentName + ' '
    setInput(newInput)
    setShowMentions(false)
    taRef.current?.focus()
  }

  const filteredAgents = p.agents?.filter(
    (a) =>
      a.name.toLowerCase().includes(mentionQuery.toLowerCase()) ||
      a.id.toLowerCase().includes(mentionQuery.toLowerCase()),
  ) || []

  const running = p.agent.status === 'running'
  const visible = p.agent.messages.filter((m) => m.role !== 'system')

  return (
    <div className="c-col" style={{ flex: 1 }}>
      {/* 头部 */}
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
            background: p.agent.color,
            color: 'var(--bg-base)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 12,
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          {p.agent.name.slice(0, 1).toUpperCase()}
        </span>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, lineHeight: 1.3 }}>
            {p.agent.name}
          </div>
          <div
            style={{
              fontSize: 'var(--text-xs)',
              color: 'var(--text-tertiary)',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              maxWidth: 300,
            }}
          >
            {p.agent.role}
          </div>
        </div>

        <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}>
          {running && (
            <button className="c-btn c-btn--danger c-btn--sm" onClick={p.onStop}>
              停止
            </button>
          )}
          <StatusBadge status={p.agent.status} queueLen={p.agent.queueLen} />
          <button
            className={`c-icon-btn ${p.inspectorOpen ? 'c-icon-btn--active' : ''}`}
            onClick={p.onToggleInspector}
            title={p.inspectorOpen ? '收起检查器' : '展开检查器'}
          >
            ◫
          </button>
        </div>
      </header>

      {/* 消息流 */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4) var(--space-6) var(--space-2)' }}
      >
        {visible.length === 0 && !p.streaming && (
          <EmptyChat color={p.agent.color} name={p.agent.name} />
        )}

        {visible.map((m, i) => (
          <Bubble key={i} m={m} color={p.agent!.color} onContextMenu={handleContextMenu} />
        ))}

        {p.streaming && <Bubble m={{ role: 'assistant', content: p.streaming }} color={p.agent.color} live />}

        {running && !p.streaming && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 7,
              color: 'var(--text-tertiary)',
              fontSize: 'var(--text-sm)',
              padding: '4px 0',
            }}
          >
            <span className="blink" style={{ color: 'var(--warning)' }}>
              ●
            </span>
            正在思考…
          </div>
        )}

        {p.error && (
          <div
            style={{
              marginTop: 'var(--space-3)',
              background: 'var(--danger-dim)',
              border: '1px solid var(--danger-border)',
              borderRadius: 'var(--radius-md)',
              padding: '9px 12px',
              fontSize: 'var(--text-sm)',
              color: 'var(--danger)',
              lineHeight: 'var(--leading-normal)',
            }}
          >
            {p.error}
          </div>
        )}
      </div>

      {/* 输入区 */}
      <div
        style={{
          padding: 'var(--space-3) var(--space-6) var(--space-5)',
          flexShrink: 0,
        }}
      >
        <div
          style={{
            position: 'relative',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-lg)',
            overflow: 'visible',
            transition: 'border-color var(--dur-fast) var(--ease)',
          }}
          onFocusCapture={(e) => (e.currentTarget.style.borderColor = 'var(--border-focus)')}
          onBlurCapture={(e) => (e.currentTarget.style.borderColor = 'var(--border-default)')}
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
            placeholder={
              mode === 'shell'
                ? '输入 shell 命令，回车执行'
                : `给 ${p.agent.name} 发消息…`
            }
            rows={2}
            className={mode === 'shell' ? 'mono' : ''}
            style={{
              width: '100%',
              resize: 'none',
              border: 'none',
              background: 'transparent',
              padding: '11px 13px 4px',
              lineHeight: 'var(--leading-normal)',
              fontSize: 'var(--text-md)',
            }}
          />

          {/* @提及自动补全 */}
          {showMentions && filteredAgents.length > 0 && mode === 'chat' && (
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
                maxHeight: 200,
                overflowY: 'auto',
                zIndex: 50,
              }}
            >
              {filteredAgents.map((a) => (
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
                    borderBottom: '1px solid var(--border-subtle)',
                    fontSize: 'var(--text-sm)',
                    color: 'var(--text-primary)',
                    background: 'transparent',
                    cursor: 'pointer',
                  }}
                  onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
                  onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
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
                  <span>{a.name}</span>
                  <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                    {a.role.slice(0, 20)}
                  </span>
                </button>
              ))}
            </div>
          )}

          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 4,
              padding: '5px 8px 7px',
            }}
          >
            <ModeBtn active={mode === 'chat'} onClick={() => setMode('chat')}>
              对话
            </ModeBtn>
            <ModeBtn active={mode === 'shell'} onClick={() => setMode('shell')}>
              终端
            </ModeBtn>

            <span
              className="mono"
              style={{
                marginLeft: 'auto',
                fontSize: 'var(--text-xs)',
                color: 'var(--text-disabled)',
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                maxWidth: 260,
                marginRight: 6,
              }}
              title={p.workDir}
            >
              {p.workDir ? p.workDir.split('/').filter(Boolean).pop() : '—'}
            </span>

            <button
              className="c-btn c-btn--primary c-btn--sm"
              onClick={submit}
              disabled={!input.trim()}
            >
              {mode === 'shell' ? '执行' : '发送'}
            </button>
          </div>
        </div>

        <div
          style={{
            display: 'flex',
            gap: 'var(--space-3)',
            marginTop: 6,
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
          }}
        >
          <span>Enter 发送 · Shift+Enter 换行</span>
        </div>
      </div>

      {/* 右键菜单 */}
      {contextMenu && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 999 }}
            onClick={closeContextMenu}
            onContextMenu={(e) => { e.preventDefault(); closeContextMenu() }}
          />
          <div
            className="fade-up"
            style={{
              position: 'fixed',
              left: contextMenu.x,
              top: contextMenu.y,
              zIndex: 1000,
              background: 'var(--bg-elevated)',
              border: '1px solid var(--border-default)',
              borderRadius: 'var(--radius-md)',
              boxShadow: 'var(--shadow-lg)',
              minWidth: 180,
              overflow: 'hidden',
            }}
          >
            <button
              onClick={() => copyMessage(contextMenu.msg)}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                width: '100%',
                padding: '10px 14px',
                textAlign: 'left',
                fontSize: 'var(--text-sm)',
                color: 'var(--text-primary)',
                background: 'transparent',
                borderBottom: '1px solid var(--border-subtle)',
                cursor: 'pointer',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
              onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
            >
              📋 复制
            </button>
            {contextMenu.msg.role === 'assistant' && (
              <button
                onClick={() => regenerateMessage(contextMenu.msg)}
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  width: '100%',
                  padding: '10px 14px',
                  textAlign: 'left',
                  fontSize: 'var(--text-sm)',
                  color: 'var(--text-primary)',
                  background: 'transparent',
                  borderBottom: '1px solid var(--border-subtle)',
                  cursor: 'pointer',
                }}
                onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
                onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
              >
                🔄 重新生成
              </button>
            )}
            {p.agents && p.agents.length > 1 && (
              <div style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                <div style={{ padding: '8px 14px 4px', fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                  引用给…
                </div>
                {p.agents
                  .filter((a) => a.id !== p.agent?.id)
                  .map((a) => (
                    <button
                      key={a.id}
                      onClick={() => quoteToAgent(contextMenu.msg, a.id)}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                        width: '100%',
                        padding: '8px 14px',
                        textAlign: 'left',
                        fontSize: 'var(--text-sm)',
                        color: 'var(--text-primary)',
                        background: 'transparent',
                        cursor: 'pointer',
                      }}
                      onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
                      onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                    >
                      <span
                        style={{
                          width: 14,
                          height: 14,
                          borderRadius: 'var(--radius-sm)',
                          background: a.color,
                          display: 'grid',
                          placeItems: 'center',
                          fontSize: 8,
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
          </div>
        </>
      )}
    </div>
  )
}

function ModeBtn({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: '4px 10px',
        borderRadius: 'var(--radius-sm)',
        fontSize: 'var(--text-xs)',
        fontWeight: active ? 600 : 400,
        color: active ? 'var(--text-primary)' : 'var(--text-tertiary)',
        background: active ? 'var(--bg-surface-active)' : 'transparent',
        transition: 'all var(--dur-fast) var(--ease)',
      }}
    >
      {children}
    </button>
  )
}

function StatusBadge({ status, queueLen }: { status: string; queueLen?: number }) {
  const map: Record<string, { cls: string; t: string }> = {
    idle: { cls: '', t: '空闲' },
    running: { cls: 'c-badge--warning', t: '运行中' },
    queued: { cls: 'c-badge--accent', t: queueLen ? `排队 ${queueLen}` : '排队' },
    done: { cls: 'c-badge--success', t: '完成' },
    error: { cls: 'c-badge--danger', t: '出错' },
  }
  const s = map[status] ?? map.idle
  return <span className={`c-badge ${s.cls}`}>{s.t}</span>
}

function EmptyChat({ color, name }: { color: string; name: string }) {
  const hints = [
    '这个模块有什么风险？',
    '给我一个最小可行实现',
    '看下 query 能不能优化',
  ]
  return (
    <div style={{ paddingTop: 'var(--space-6)' }}>
      <div
        style={{
          width: 34,
          height: 34,
          borderRadius: 'var(--radius-md)',
          background: color,
          color: 'var(--bg-base)',
          display: 'grid',
          placeItems: 'center',
          fontSize: 16,
          fontWeight: 700,
          marginBottom: 'var(--space-4)',
        }}
      >
        {name.slice(0, 1).toUpperCase()}
      </div>
      <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 6 }}>
        {name} 已就绪
      </div>
      <div
        style={{
          fontSize: 'var(--text-sm)',
          color: 'var(--text-tertiary)',
          lineHeight: 'var(--leading-relaxed)',
          marginBottom: 'var(--space-5)',
        }}
      >
        这个会话是独立的，不影响其他 agent。
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {hints.map((h) => (
          <span
            key={h}
            style={{
              fontSize: 'var(--text-xs)',
              color: 'var(--text-tertiary)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-full)',
              padding: '5px 11px',
              background: 'var(--bg-surface)',
            }}
          >
            {h}
          </span>
        ))}
      </div>
    </div>
  )
}

function Bubble({ m, color, live, onContextMenu }: { m: Message; color: string; live?: boolean; onContextMenu?: (e: React.MouseEvent, msg: Message) => void }) {
  const isUser = m.role === 'user'

  // 检测路由消息：[from xxx] 或 [to xxx]
  const fromMatch = m.content.match(/^\[from\s+(\S+)\]\s*/)
  const toMatch = m.content.match(/^\[to\s+(\S+)\]\s*/)
  const isRouted = !!(fromMatch || toMatch)
  const routeInfo = fromMatch ? `来自 ${fromMatch[1]}` : toMatch ? `发给 ${toMatch[1]}` : ''
  const displayContent = m.content.replace(/^\[(from|to)\s+\S+\]\s*/, '')

  return (
    <div
      className="fade-up"
      style={{
        marginBottom: 'var(--space-4)',
        display: 'flex',
        gap: 'var(--space-3)',
        opacity: isRouted ? 0.85 : 1,
        cursor: onContextMenu ? 'context-menu' : 'default',
      }}
      onContextMenu={onContextMenu ? (e) => onContextMenu(e, m) : undefined}
    >
      {!isUser && (
        <span
          style={{
            width: 22,
            height: 22,
            borderRadius: 'var(--radius-sm)',
            background: isRouted ? 'var(--accent-dim)' : color,
            color: isRouted ? 'var(--accent-bright)' : 'var(--bg-base)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 11,
            fontWeight: 700,
            flexShrink: 0,
            marginTop: 1,
            opacity: 0.9,
          }}
        >
          {isRouted ? '✉' : '◆'}
        </span>
      )}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            marginBottom: 3,
            fontSize: 'var(--text-xs)',
            color: isUser ? 'var(--text-disabled)' : isRouted ? 'var(--accent-bright)' : 'var(--text-tertiary)',
            fontWeight: 600,
            letterSpacing: '0.02em',
            lineHeight: 1.3,
          }}
        >
          {isUser ? '你' : isRouted ? `✉ ${routeInfo}` : 'Agent'}
          {m.timestamp && (
            <span style={{ fontWeight: 400, fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', marginLeft: 'auto' }}>
              {new Date(m.timestamp * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}
            </span>
          )}
          {live && (
            <span className="blink" style={{ color: 'var(--warning)', fontSize: 9 }}>
              ●
            </span>
          )}
        </div>
        <div
          style={{
            fontSize: 'var(--text-md)',
            lineHeight: 1.7,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            color: isUser ? 'var(--text-primary)' : isRouted ? 'var(--text-secondary)' : 'var(--text-secondary)',
            fontStyle: isRouted ? 'italic' : 'normal',
          }}
        >
          {displayContent}
        </div>
      </div>
    </div>
  )
}