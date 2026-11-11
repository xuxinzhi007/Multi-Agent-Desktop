import { useEffect, useRef, useState } from 'react'
import type { Agent } from '../lib/ws'

type Props = {
  agent: Agent | null
  streaming: string
  error?: string
  workDir: string
  onPrompt: (text: string) => void
  onStop: () => void
  onShell: (command: string) => void
}

export default function AgentView(p: Props) {
  const [input, setInput] = useState('')
  const [shellMode, setShellMode] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [p.agent?.messages.length, p.streaming])

  useEffect(() => {
    taRef.current?.focus()
  }, [p.agent?.id, shellMode])

  if (!p.agent) {
    return (
      <div
        style={{
          flex: 1,
          display: 'grid',
          placeItems: 'center',
          color: 'var(--dim)',
          fontSize: 12.5,
          lineHeight: 2,
          textAlign: 'center',
        }}
      >
        <div>
          <div style={{ fontSize: 28, marginBottom: 14, opacity: 0.25 }}>◇</div>
          左侧新建一个 agent，或点击已有会话
          <br />
          <span style={{ fontSize: 11, opacity: 0.7 }}>
            多个 agent 可以同时运行，互不干扰
          </span>
        </div>
      </div>
    )
  }

  const submit = () => {
    const text = input.trim()
    if (!text) return
    if (shellMode) {
      p.onShell(text)
    } else {
      p.onPrompt(text)
    }
    setInput('')
  }

  const running = p.agent.status === 'running'
  const visible = p.agent.messages.filter((m) => m.role !== 'system')

  return (
    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', height: '100%', minWidth: 0 }}>
      <header
        style={{
          padding: '12px 20px',
          borderBottom: '1px solid var(--border)',
          display: 'flex',
          alignItems: 'center',
          gap: 11,
          flexShrink: 0,
        }}
      >
        <span
          style={{
            width: 3,
            height: 26,
            borderRadius: 2,
            background: p.agent.color,
            flexShrink: 0,
          }}
        />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 600, fontSize: 13.5 }}>{p.agent.name}</div>
          <div
            style={{
              fontSize: 11,
              color: 'var(--muted)',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {p.agent.role}
          </div>
        </div>
        {running && (
          <button
            onClick={p.onStop}
            style={{
              background: 'rgba(229,72,77,0.12)',
              border: '1px solid rgba(229,72,77,0.4)',
              color: 'var(--red)',
              padding: '5px 12px',
              borderRadius: 5,
              fontSize: 11.5,
            }}
          >
            停止
          </button>
        )}
        <StatusPill status={p.agent.status} />
      </header>

      <div ref={scrollRef} style={{ flex: 1, overflowY: 'auto', padding: '18px 20px' }}>
        {visible.length === 0 && !p.streaming && (
          <div style={{ color: 'var(--dim)', fontSize: 12, lineHeight: 1.9 }}>
            会话已就绪。发一条消息试试：
            <div
              className="mono"
              style={{
                marginTop: 10,
                background: 'var(--panel)',
                border: '1px solid var(--border)',
                borderRadius: 6,
                padding: 10,
                fontSize: 11.5,
                color: 'var(--muted)',
              }}
            >
              给我一个这个项目的重构建议
            </div>
          </div>
        )}

        {visible.map((m, i) => (
          <Bubble key={i} role={m.role} content={m.content} color={p.agent!.color} />
        ))}

        {p.streaming && (
          <Bubble role="assistant" content={p.streaming} color={p.agent.color} live />
        )}

        {running && !p.streaming && (
          <div style={{ color: 'var(--muted)', fontSize: 12, padding: '6px 0' }}>
            <span className="blink">●</span> 正在思考…
          </div>
        )}

        {p.error && (
          <div
            style={{
              marginTop: 12,
              background: 'rgba(229,72,77,0.09)',
              border: '1px solid rgba(229,72,77,0.35)',
              borderRadius: 6,
              padding: '9px 12px',
              fontSize: 11.5,
              color: '#ff9a9e',
            }}
          >
            {p.error}
          </div>
        )}
      </div>

      <div style={{ padding: '10px 20px 16px', flexShrink: 0, borderTop: '1px solid var(--border)' }}>
        <div style={{ display: 'flex', gap: 7, marginBottom: 7, alignItems: 'center' }}>
          <button
            onClick={() => setShellMode(false)}
            style={{
              padding: '3px 10px',
              borderRadius: 4,
              fontSize: 11,
              background: !shellMode ? 'var(--panel-2)' : 'transparent',
              color: !shellMode ? 'var(--text)' : 'var(--dim)',
              border: `1px solid ${!shellMode ? 'var(--border)' : 'transparent'}`,
            }}
          >
            对话
          </button>
          <button
            onClick={() => setShellMode(true)}
            style={{
              padding: '3px 10px',
              borderRadius: 4,
              fontSize: 11,
              background: shellMode ? 'var(--panel-2)' : 'transparent',
              color: shellMode ? 'var(--text)' : 'var(--dim)',
              border: `1px solid ${shellMode ? 'var(--border)' : 'transparent'}`,
            }}
          >
            终端
          </button>
          <span
            className="mono"
            style={{
              marginLeft: 'auto',
              fontSize: 10.5,
              color: 'var(--dim)',
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              maxWidth: 300,
            }}
            title={p.workDir}
          >
            {p.workDir || '—'}
          </span>
        </div>

        <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
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
              shellMode ? '输入 shell 命令，回车执行' : '给这个 agent 发消息，Enter 发送 / Shift+Enter 换行'
            }
            rows={2}
            className={shellMode ? 'mono' : ''}
            style={{
              flex: 1,
              resize: 'none',
              lineHeight: 1.6,
              background: 'var(--panel)',
              fontSize: shellMode ? 12 : 13,
            }}
          />
          <button
            onClick={submit}
            disabled={!input.trim()}
            style={{
              background: input.trim() ? 'var(--accent)' : 'var(--panel-2)',
              color: input.trim() ? '#fff' : 'var(--dim)',
              padding: '9px 18px',
              borderRadius: 6,
              fontWeight: 500,
              fontSize: 12.5,
              flexShrink: 0,
            }}
          >
            {shellMode ? '执行' : '发送'}
          </button>
        </div>
      </div>
    </div>
  )
}

function StatusPill({ status }: { status: string }) {
  const map: Record<string, { c: string; t: string }> = {
    idle: { c: 'var(--dim)', t: '空闲' },
    running: { c: 'var(--amber)', t: '运行中' },
    done: { c: 'var(--green)', t: '已完成' },
    error: { c: 'var(--red)', t: '出错' },
  }
  const s = map[status] ?? map.idle
  return (
    <span
      style={{
        fontSize: 10.5,
        color: s.c,
        border: `1px solid ${s.c}`,
        borderRadius: 20,
        padding: '2px 9px 2px 7px',
        opacity: 0.85,
        flexShrink: 0,
      }}
    >
      ● {s.t}
    </span>
  )
}

function Bubble({
  role,
  content,
  color,
  live,
}: {
  role: string
  content: string
  color: string
  live?: boolean
}) {
  const isUser = role === 'user'
  return (
    <div
      className="fade-up"
      style={{
        marginBottom: 16,
        display: 'flex',
        flexDirection: 'column',
        alignItems: isUser ? 'flex-end' : 'flex-start',
      }}
    >
      <div
        style={{
          fontSize: 10,
          color: isUser ? 'var(--dim)' : color,
          marginBottom: 5,
          letterSpacing: 0.4,
          textTransform: 'uppercase',
        }}
      >
        {isUser ? '你' : 'Agent'}
        {live && <span className="blink" style={{ marginLeft: 6 }}>●</span>}
      </div>
      <div
        style={{
          maxWidth: '82%',
          background: isUser ? 'var(--panel-2)' : 'var(--panel)',
          border: `1px solid ${isUser ? 'var(--border)' : 'var(--border)'}`,
          borderLeft: isUser ? undefined : `2px solid ${color}`,
          borderRadius: 8,
          padding: '10px 13px',
          fontSize: 12.8,
          lineHeight: 1.75,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-word',
          color: isUser ? 'var(--text)' : '#d5dae5',
        }}
      >
        {content}
      </div>
    </div>
  )
}