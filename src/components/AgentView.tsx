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
}

export default function AgentView(p: Props) {
  const [input, setInput] = useState('')
  const [mode, setMode] = useState<'chat' | 'shell'>('chat')
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
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

  const submit = () => {
    const text = input.trim()
    if (!text) return
    if (mode === 'shell') p.onShell(text)
    else p.onPrompt(text)
    setInput('')
  }

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
          <StatusBadge status={p.agent.status} />
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
        style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4) var(--space-6) var(--space-2)' }}
      >
        {visible.length === 0 && !p.streaming && (
          <EmptyChat color={p.agent.color} name={p.agent.name} />
        )}

        {visible.map((m, i) => (
          <Bubble key={i} m={m} color={p.agent!.color} />
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
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-lg)',
            overflow: 'hidden',
            transition: 'border-color var(--dur-fast) var(--ease)',
          }}
          onFocusCapture={(e) => (e.currentTarget.style.borderColor = 'var(--border-focus)')}
          onBlurCapture={(e) => (e.currentTarget.style.borderColor = 'var(--border-default)')}
        >
          <textarea
            ref={taRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
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
              {p.workDir || '—'}
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

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { cls: string; t: string }> = {
    idle: { cls: '', t: '空闲' },
    running: { cls: 'c-badge--warning', t: '运行中' },
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

function Bubble({ m, color, live }: { m: Message; color: string; live?: boolean }) {
  const isUser = m.role === 'user'
  return (
    <div
      className="fade-up"
      style={{ marginBottom: 'var(--space-4)', display: 'flex', gap: 'var(--space-3)' }}
    >
      {!isUser && (
        <span
          style={{
            width: 22,
            height: 22,
            borderRadius: 'var(--radius-sm)',
            background: color,
            color: 'var(--bg-base)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 11,
            fontWeight: 700,
            flexShrink: 0,
            marginTop: 1,
            opacity: 0.9,
          }}
        >
          ◆
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
            color: isUser ? 'var(--text-disabled)' : 'var(--text-tertiary)',
            fontWeight: 600,
            letterSpacing: '0.02em',
            lineHeight: 1.3,
          }}
        >
          {isUser ? '你' : 'Agent'}
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
            color: isUser ? 'var(--text-primary)' : 'var(--text-secondary)',
          }}
        >
          {m.content}
        </div>
      </div>
    </div>
  )
}