import { Fragment, cloneElement, isValidElement, memo, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import TextareaAutosize from 'react-textarea-autosize'
import ModelPicker from './ModelPicker'
import { Popover } from 'radix-ui'
import MentionPicker from './MentionPicker'
import MentionText, { type MentionTarget } from './MentionText'
import remarkGfm from 'remark-gfm'
import { type AgentView as Agent, type Config, type Message } from '../mygo'
import type { SessionError, ShellLog } from '../lib/useBackend'
import { parseMessage } from '../lib/message'
import { groupShellLogs, redactCommand, type ShellGroup } from '../lib/transcriptPresent'
import {
  agentTone,
  elapsedBetween,
  extractArtifacts,
  prevUserStamp,
  splitAnchors,
  workLabel,
} from '../lib/messagePresent'
import DirectoryPicker from './DirectoryPicker'
import { VirtualList } from './VirtualList'
import { contextMeter } from '../lib/contextMeter'

type Props = {
  agent: Agent | null
  streaming: string
  error?: SessionError
  workDir: string
  tokenUsage?: { in: number; out: number; lastIn?: number }
  onPrompt: (text: string) => void
  onStop: () => void
  onShell: (command: string) => void
  /** 本会话执行过的命令，直接渲染在对话流里 */
  shellLogs?: ShellLog[]
  /** 这一轮已经发生的工具步骤。思考中靠它显示在读什么、跑了什么。 */
  steps?: { tool: string; target: string; status: 'running' | 'ok' | 'error' | 'waiting' }[]
  /** 模型这一轮的思考文本，还没写进消息时先显示在这里。 */
  reasoning?: string
  onToggleInspector: () => void
  inspectorOpen: boolean
  /** 单聊模式：禁用 @ 路由功能 */
  disableRoute?: boolean
  onRoute?: (targetName: string, text: string) => void
  agents?: Agent[]
  /** 会话级配置变更（工作目录 / 模型），只影响当前会话 */
  onConfig: (patch: { workDir?: string; llm?: Config }) => void
  /** 空状态下的一键创建：传角色预设，直接建出可用 agent */
  onQuickCreate?: (presetName: string, role: string) => void
  /** 空状态下打开「新建 Agent」弹窗 */
  onCreateRequest?: () => void
  /** 正文里的代码锚点：打开检查器并定位文件 */
  onOpenFile?: (path: string, line: number) => void
  /** 知识库沉淀胶囊：打开检查器的知识库 */
  onOpenKnowledge?: () => void
  /** 设置里已确认的连接。选模型时凭据从这里带上，不在会话里再填一遍 Key。 */
  connection?: { provider: string; baseUrl: string; apiKey: string; model: string }
}

// 空状态的一键角色。只保留 3 个 —— 选择过多反而增加决策成本。
const quickRoles = [
  { emoji: '🧩', label: '前端', name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理与渲染性能' },
  { emoji: '⚙️', label: '后端', name: '后端', role: 'Go 专家，关注 API 设计、并发安全与查询效率' },
  { emoji: '🔍', label: '审查', name: 'Reviewer', role: '代码审查者，指出风险、代码异味与不符合规范的地方' },
] as const

const TOOL_LABEL: Record<string, string> = {
  read_file: '读取',
  write_file: '写入',
  edit_file: '修改',
  bash: '执行',
}

function stepText(tool: string, target: string) {
  const label = TOOL_LABEL[tool] ?? tool
  const prefix = tool + ' '
  const detail = target.startsWith(prefix) ? target.slice(prefix.length) : target
  const line = detail.replace(/\s+/g, ' ').trim()
  if (!line || line === tool) return label
  return line.length > 80 ? `${label} ${line.slice(0, 80)}…` : `${label} ${line}`
}

function ThinkingSwitch({
  provider,
  thinking,
  onToggle,
}: {
  provider: string
  thinking?: string
  onToggle: (next: 'on' | 'off') => void
}) {
  if (provider !== 'deepseek') return null
  const on = thinking !== 'off'
  return (
    <button
      type="button"
      onClick={() => onToggle(on ? 'off' : 'on')}
      title={on ? '思考模式开着' : '思考模式关着'}
      style={{
        border: '1px solid var(--border)',
        background: on ? 'var(--accent-dim)' : 'transparent',
        color: on ? 'var(--accent-bright)' : 'var(--text-tertiary)',
        borderRadius: 'var(--radius-sm)',
        fontSize: 'var(--text-xs)',
        padding: '2px 8px',
        cursor: 'pointer',
        flexShrink: 0,
      }}
    >
      思考{on ? '开' : '关'}
    </button>
  )
}

type TranscriptRow = { kind: 'msg'; m: Message; idx: number } | { kind: 'err'; text: string }

function transcriptRows(visible: Message[], errorAt: number, error?: string): TranscriptRow[] {
  const rows: TranscriptRow[] = []
  for (let i = 0; i < errorAt && i < visible.length; i++) rows.push({ kind: 'msg', m: visible[i], idx: i })
  if (error) rows.push({ kind: 'err', text: error })
  for (let i = errorAt; i < visible.length; i++) rows.push({ kind: 'msg', m: visible[i], idx: i })
  return rows
}

function presentTranscript(messages: Message[]): Message[] {
  const out: Message[] = []
  let chain: string[] = []
  for (const m of messages) {
    if (m.role === 'system' || m.role === 'tool') continue
    if (m.role === 'assistant' && !(m.content || '').trim()) {
      if (m.reasoning) chain.push(m.reasoning)
      continue
    }
    if (m.role === 'assistant') {
      const parts = [...chain]
      if (m.reasoning) parts.push(m.reasoning)
      chain = []
      out.push(parts.length ? { ...m, reasoning: parts.join('\n\n') } : m)
      continue
    }
    chain = []
    out.push(m)
  }
  return out
}

function ThinkRail({
  live,
  title,
  children,
}: {
  live?: boolean
  title: string
  children?: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const [secs, setSecs] = useState(0)
  const bodyRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!live) return
    const t0 = Date.now()
    const id = window.setInterval(() => setSecs((Date.now() - t0) / 1000), 200)
    return () => window.clearInterval(id)
  }, [live])
  useEffect(() => {
    const el = bodyRef.current
    if (!el || !open) return
    el.scrollTop = el.scrollHeight
  }, [children, open])
  const hasBody = children != null && children !== false && children !== ''
  const label = live ? `思考了 ${secs.toFixed(1)}s` : title
  return (
    <div className="c-think">
      <button
        type="button"
        className="c-think__bar"
        onClick={() => hasBody && setOpen((v) => !v)}
        aria-expanded={hasBody ? open : undefined}
      >
        <span className={live ? 'c-think__pulse c-think__pulse--live' : 'c-think__pulse c-think__pulse--done'} />
        <span className="c-think__title">{label}</span>
        {hasBody && <span className="c-think__toggle">{open ? '收起' : '展开'}</span>}
      </button>
      {open && hasBody && (
        <div className="c-think__body" ref={bodyRef}>
          {children}
        </div>
      )}
    </div>
  )
}

function ReasoningCard({ text, live }: { text: string; live?: boolean }) {
  const tail = text.trim().split('\n').filter(Boolean).at(-1) ?? ''
  const title = tail ? `思考 · ${tail}` : '思考'
  return (
    <ThinkRail live={live} title={title}>
      {text}
    </ThinkRail>
  )
}

function Thinking({ steps, reasoning }: { steps: NonNullable<Props['steps']>; reasoning?: string }) {
  const current = [...steps].reverse().find((s) => s.status === 'running')
  const detail = current ? stepText(current.tool, current.target) : ''
  const has = !!reasoning || steps.length > 0
  return (
    <ThinkRail live title={detail || (has ? '思考' : '正在思考…')}>
      {has ? (
        <>
          {reasoning}
          {steps.map((s, i) => (
            <div key={`${s.tool}-${i}`} style={{ color: s.status === 'error' ? 'var(--danger)' : 'var(--text-tertiary)' }}>
              {s.status === 'running' ? '…' : s.status === 'error' ? '✕' : s.status === 'waiting' ? '!' : '✓'}{' '}
              {stepText(s.tool, s.target)}
            </div>
          ))}
        </>
      ) : undefined}
    </ThinkRail>
  )
}

export default function AgentView(p: Props) {
  const [input, setInput] = useState('')
  const [mode, setMode] = useState<'chat' | 'shell'>('chat')
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; msg: Message } | null>(null)
  const [toolGrants, setToolGrants] = useState<{ always: string[]; done: string[] }>({ always: [], done: [] })
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const stickToBottom = useRef(true)
  const programmaticScroll = useRef(false)

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
      p.onRoute(target.id, msg.content.slice(0, 200))
    }
    closeContextMenu()
  }

  // 只有用户自己把滚动条拉开，才停止跟随。程序把视口推到底时不算。
  const handleScroll = () => {
    if (programmaticScroll.current) return
    const el = scrollRef.current
    if (!el) return
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  const followBottom = () => {
    const el = scrollRef.current
    if (!el || !stickToBottom.current) return
    programmaticScroll.current = true
    el.scrollTop = el.scrollHeight
    requestAnimationFrame(() => {
      programmaticScroll.current = false
    })
  }

  useEffect(() => {
    const el = scrollRef.current
    const content = el?.firstElementChild
    if (!el || !content) return
    const ro = new ResizeObserver(() => followBottom())
    ro.observe(content)
    return () => ro.disconnect()
  }, [p.agent?.id])

  const seenAgent = useRef(p.agent?.id)
  useEffect(() => {
    if (seenAgent.current !== p.agent?.id) {
      seenAgent.current = p.agent?.id
      stickToBottom.current = true
    }
    const last = p.agent?.messages?.[p.agent.messages.length - 1]
    if (last?.role === 'user') stickToBottom.current = true
    followBottom()
  }, [p.agent?.id, p.agent?.messages.length, p.streaming, p.reasoning, p.error?.text, p.error?.after])

  useEffect(() => {
    taRef.current?.focus()
  }, [p.agent?.id, mode])

  useEffect(() => {
    setToolGrants({ always: [], done: [] })
  }, [p.agent?.id])

  const [showMentions, setShowMentions] = useState(false)
  const [mentionQuery, setMentionQuery] = useState('')

  // 传给 Bubble 做 @ 上色的名字/颜色表。内容变化才换引用，
  // 否则 memo 化的 Bubble 会每秒重渲染全部历史消息。
  const mentionKey = (p.agents ?? []).map((a) => `${a.id}:${a.name}:${a.color}`).join('|')
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const agentsForPicker = useMemo(
    () =>
      (p.agents ?? []).map((a) => ({
        id: a.id,
        name: a.name,
        color: a.color,
        role: a.role,
      })),
    [mentionKey],
  )
  const peers = useMemo(
    () => agentsForPicker.filter((a) => a.id !== p.agent?.id),
    [agentsForPicker, p.agent?.id],
  )


  if (!p.agent) {
    return (
      <div className="c-col" style={{ flex: 1 }}>
        <div className="c-empty" style={{ flex: 1 }}>
          <div
            style={{
              width: 64,
              height: 64,
              borderRadius: 'var(--radius-lg)',
              background: 'linear-gradient(135deg, var(--accent-dim) 0%, transparent 100%)',
              display: 'grid',
              placeItems: 'center',
              marginBottom: 'var(--space-4)',
              fontSize: 28,
            }}
          >
            🤖
          </div>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>
            开始你的多 Agent 协作
          </div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 20 }}>
            选一个角色，立刻开始。之后可以随时加更多 agent 并行工作。
          </div>

          {/* 一键创建：不用填表，点一下就有一个能对话的 agent */}
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, justifyContent: 'center', marginBottom: 18 }}>
            {quickRoles.map((r) => (
              <button
                key={r.name}
                type="button"
                className="c-empty-cta"
                onClick={() => p.onQuickCreate?.(r.name, r.role)}
              >
                <span aria-hidden>{r.emoji}</span>
                {r.label}
              </button>
            ))}
          </div>

          <button
            type="button"
            className="c-hint-chip"
            onClick={() => p.onCreateRequest?.()}
          >
            自定义名称与角色 →
          </button>
        </div>
      </div>
    )
  }

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
      // 单聊模式：禁用 @ 路由，直接作为普通消息发送
      if (!p.disableRoute && targets.length > 0 && p.onRoute && p.agents) {
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

  const agent = p.agent
  const running = agent.status === 'running'
  const turnLive = running || agent.status === 'queued' || agent.status === 'blocked' || !!p.streaming
  const hasDraft = !!input.trim()
  const rawMessages = p.agent.messages ?? []
  const visible = presentTranscript(rawMessages)
  // 错误记的是出错时的原始条数。按同一套折叠规则数出可见气泡，红条跟在那一截后面。
  // 之后又多出来的气泡（新一轮）排在红条之下，红条不会贴到最新一句后面。
  const errorAt = p.error
    ? presentTranscript(rawMessages.slice(0, p.error.after)).length
    : visible.length
  const tone = agentTone((p.agents ?? []).findIndex((a) => a.id === agent.id))
  const modelName = agent.llm?.model || ''
  const place = workLabel(agent.workDir)
  const head = {
    tone,
    name: agent.name,
    model: modelName,
    workLabel: place,
    peers,
  }
  const pendingTools = (p.steps ?? []).filter((s) => {
    if (s.status !== 'waiting') return false
    if (toolGrants.always.includes(s.tool)) return false
    return !toolGrants.done.includes(`${s.tool}\0${s.target}`)
  })
  const settleTool = (tool: string, target: string, kind: 'allow' | 'deny' | 'always') => {
    setToolGrants((g) => ({
      always: kind === 'always' ? [...g.always, tool] : g.always,
      done: [...g.done, `${tool}\0${target}`],
    }))
  }
  const renderMsg = (m: Message, idx: number) => (
    <Bubble
      key={idx}
      m={m}
      {...head}
      elapsed={m.role === 'assistant' ? elapsedBetween(prevUserStamp(visible, idx), m.timestamp) : ''}
      agents={agentsForPicker}
      tokens={idx === visible.length - 1 && m.role === 'assistant' ? p.tokenUsage : undefined}
      onContextMenu={handleContextMenu}
      onCopy={(text) => navigator.clipboard.writeText(text)}
      onRegenerate={(msg) => p.onPrompt(msg.content)}
      onRoute={p.onRoute}
      onOpenFile={p.onOpenFile}
      onOpenKnowledge={p.onOpenKnowledge}
    />
  )

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
            {p.agent.busy ? `正在${p.agent.busy}里回答` : p.agent.role}
          </div>
        </div>

        <div style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6 }}>
          {running && (
            <button className="c-btn c-btn--danger c-btn--sm" onClick={p.onStop}>
              停止
            </button>
          )}
          <StatusBadge
            status={p.agent.status}
            queueLen={p.agent.queueLen}
            since={p.agent.queuedAt ? p.agent.queuedAt * 1000 : undefined}
          />
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
        <div className="c-transcript">
        {visible.length === 0 && !p.streaming && (
          <EmptyChat
            color={p.agent.color}
            name={p.agent.name}
            onPick={(text) => {
              setInput(text)
              requestAnimationFrame(() => taRef.current?.focus())
            }}
          />
        )}

        <VirtualList
          items={transcriptRows(visible, errorAt, p.error?.text)}
          scrollRef={scrollRef}
          resetKey={agent.id}
          render={(row) =>
            row.kind === 'err' ? (
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
                {row.text}
              </div>
            ) : (
              renderMsg(row.m, row.idx)
            )
          }
        />

        {p.streaming && !p.agent.busy && (
          <Bubble
            m={{ role: 'assistant', content: p.streaming }}
            {...head}
            live
            onCopy={(text) => navigator.clipboard.writeText(text)}
            onOpenFile={p.onOpenFile}
            onOpenKnowledge={p.onOpenKnowledge}
          />
        )}

        {running && !p.agent.busy && !p.streaming && <Thinking steps={p.steps ?? []} reasoning={p.reasoning} />}
        {!!p.streaming && !p.agent.busy && (p.steps?.length ?? 0) > 0 && <Thinking steps={p.steps ?? []} />}

        {pendingTools.map((s, i) => (
          <ApprovalCard
            key={`${s.tool}-${i}`}
            tool={s.tool}
            target={s.target}
            onDecide={(kind) => settleTool(s.tool, s.target, kind)}
          />
        ))}

        {(p.shellLogs?.length ?? 0) > 0 && <ShellCard logs={p.shellLogs ?? []} />}
        </div>
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
            /* 聚焦时不做整框高亮：光标已经在输入框里，用户不需要额外确认。
               刻意只留一个很轻的边框变化，避免「点一下就变蓝框」的噪音。 */
            transition: 'border-color var(--dur-fast) var(--ease)',
          }}
        >
          <TextareaAutosize
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
                : turnLive
                  ? `这一轮还在进行。写下一句会排到后面，或点停止打断`
                  : `给 ${p.agent.name} 发消息…`
            }
            minRows={1}
            maxRows={6}
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
          {mode === 'chat' && (
            <MentionPicker
              open={showMentions}
              query={mentionQuery}
              agents={agentsForPicker}
              onPick={insertMention}
            />
          )}

          <div
            className="composer-bar"
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 4,
              padding: '5px 8px 7px',
              // 底栏元素不允许被压扁换行：模式按钮变成竖排文字
              // 是宽度不够时最难看的一种失败方式。
              flexWrap: 'nowrap',
            }}
          >
            <ModeBtn active={mode === 'chat'} onClick={() => setMode('chat')}>
              对话
            </ModeBtn>
            <ModeBtn active={mode === 'shell'} onClick={() => setMode('shell')}>
              终端
            </ModeBtn>

            {/* 会话级配置：工作目录 + 模型。放在输入框里，
                跟它影响的「这一轮对话」在同一个视觉区，用户不必去头部找。 */}
            <div
              style={{
                marginLeft: 'auto',
                display: 'flex',
                alignItems: 'center',
                gap: 4,
                // 窄窗口下这一组可以收缩，但按钮本身不许被压
                minWidth: 0,
                flexShrink: 1,
              }}
            >
              {/* 模型选择直接长在输入框底栏：用户换模型时不用离开对话。
                  工作目录仍然走弹层（它不常改，且需要输入路径）。 */}
              <ModelPicker
                agentId={p.agent.id}
                model={p.agent.llm?.model || ''}
                provider={p.agent.llm?.provider}
                onPick={(m) => {
                  const conn = p.connection
                  const same = !!conn && conn.provider === m.provider
                  const thinking = p.agent?.llm?.thinking
                  p.onConfig({
                    llm: {
                      provider: m.provider,
                      baseUrl: m.baseUrl || (same ? conn.baseUrl : ''),
                      apiKey: same ? conn.apiKey : '',
                      model: m.model,
                      ...(thinking ? { thinking } : {}),
                    },
                  })
                }}
              />
              <ThinkingSwitch
                provider={p.agent.llm?.provider || p.connection?.provider || ''}
                thinking={p.agent.llm?.thinking}
                onToggle={(next) => {
                  const llm = p.agent?.llm
                  const conn = p.connection
                  p.onConfig({
                    llm: {
                      provider: llm?.provider || conn?.provider || '',
                      baseUrl: llm?.baseUrl || conn?.baseUrl || '',
                      apiKey: llm?.apiKey || conn?.apiKey || '',
                      model: llm?.model || conn?.model || '',
                      thinking: next,
                    },
                  })
                }}
              />
              <div className="composer-bar__dir" style={{ minWidth: 0, flexShrink: 1 }}>
                <WorkDirChip agent={p.agent} onConfig={p.onConfig} />
              </div>
            </div>

            {turnLive && (
              <button className="c-btn c-btn--danger c-btn--sm" onClick={p.onStop}>
                停止
              </button>
            )}
            <button
              className="c-btn c-btn--primary c-btn--sm"
              onClick={submit}
              disabled={!hasDraft}
            >
              {mode === 'shell' ? '执行' : turnLive ? '排队' : '发送'}
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
            justifyContent: 'space-between',
            alignItems: 'baseline',
          }}
        >
          <span>
            {turnLive
              ? '这一轮还在进行。停止会打断它，并清掉还没开始的排队；写好再点排队，会等这一轮结束。'
              : 'Enter 发送 · Shift+Enter 换行'}
          </span>
          <ContextMeter
            messages={rawMessages}
            streaming={p.streaming}
            usage={p.tokenUsage}
            provider={p.agent.llm?.provider || p.connection?.provider || ''}
            contextWindow={p.agent.llm?.contextWindow}
          />
        </div>
      </div>

      {/* 右键菜单：用 Popover + 虚拟锚点定位到鼠标坐标。
          radix 的 ContextMenu 是绑定 Trigger 元素的，用 1px fixed div 当 trigger
          会被碰撞检测推到视口边缘 —— 菜单就跑到了最左边。 */}
      {contextMenu && (
        <Popover.Root open modal={false} onOpenChange={(v) => !v && closeContextMenu()}>
          <Popover.Anchor asChild>
            <span
              style={{
                position: 'fixed',
                left: contextMenu.x,
                top: contextMenu.y,
                width: 0,
                height: 0,
                pointerEvents: 'none',
              }}
            />
          </Popover.Anchor>
          <Popover.Portal>
            <Popover.Content
              side="bottom"
              align="start"
              sideOffset={2}
              collisionPadding={8}
              onOpenAutoFocus={(e) => e.preventDefault()}
              className="c-ctx-menu"
              style={{
                minWidth: 196,
                background: 'var(--bg-elevated)',
                border: '1px solid var(--border-default)',
                borderRadius: 'var(--radius-md)',
                boxShadow: 'var(--shadow-lg)',
                padding: 4,
                zIndex: 1000,
              }}
            >
              <MenuItem onSelect={() => copyMessage(contextMenu.msg)} icon="📋" shortcut="⌘C">
                复制
              </MenuItem>
              {contextMenu.msg.role === 'assistant' && (
                <MenuItem
                  onSelect={() => regenerateMessage(contextMenu.msg)}
                  icon="🔄"
                  shortcut="⌘R"
                >
                  重新生成
                </MenuItem>
              )}
              {p.agents && p.agents.length > 1 && (
                <>
                  <MenuSeparator />
                  <MenuLabel>引用给…</MenuLabel>
                  {p.agents
                    .filter((a) => a.id !== p.agent?.id)
                    .map((a) => (
                      <MenuItem key={a.id} onSelect={() => quoteToAgent(contextMenu.msg, a.id)}>
                        <span
                          style={{
                            width: 14,
                            height: 14,
                            borderRadius: 'var(--radius-sm)',
                            background: a.color,
                            display: 'inline-grid',
                            placeItems: 'center',
                            fontSize: 8,
                            fontWeight: 700,
                            color: 'var(--bg-base)',
                            marginRight: 6,
                          }}
                        >
                          {a.name.slice(0, 1).toUpperCase()}
                        </span>
                        {a.name}
                      </MenuItem>
                    ))}
                </>
              )}
            </Popover.Content>
          </Popover.Portal>
        </Popover.Root>
      )}
    </div>
  )
}

/**
 * WorkDirChip —— 会话级工作目录。
 *
 * 模型选择已经从弹层里搬出去了（它变成输入框底栏的 ModelPicker），
 * 这里只留工作目录：它不常改，且需要输入路径，适合用弹层。
 *
 * 关键区别：改动只影响当前会话，不动其他 agent。所以标题里写明
 * 「仅影响本会话」，避免用户以为改了全局。
 */
function WorkDirChip({
  agent,
  onConfig,
}: {
  agent: Agent
  onConfig: (patch: { workDir?: string; llm?: Config }) => void
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState('')
  const effective = agent.workDir

  // 切换会话时收起，避免弹层残留在新会话上
  useEffect(() => {
    setOpen(false)
  }, [agent.id])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  return (
    <div style={{ position: 'relative' }}>
      <button
        className="mono"
        onClick={() => {
          setOpen((v) => !v)
          setDraft(effective)
        }}
        title={`工作目录：${effective || '未设置'}（点击修改，仅影响本会话）`}
        style={chipStyle(open)}
      >
        📁 {shortPath(effective)}
        <span style={{ opacity: 0.5, marginLeft: 3 }}>▾</span>
      </button>

      {open && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 900 }} onClick={() => setOpen(false)} />
          <div
            className="fade-up"
            style={{
              position: 'absolute',
              bottom: 'calc(100% + 6px)',
              right: 0,
              width: 320,
              zIndex: 901,
              background: 'var(--bg-elevated)',
              border: '1px solid var(--border-default)',
              borderRadius: 'var(--radius-md)',
              boxShadow: 'var(--shadow-lg)',
              padding: 12,
            }}
          >
            <div style={popoverLabel}>这个会话的文件夹</div>
            <DirectoryPicker value={draft} onChange={setDraft} />
            <div style={hintStyle}>只给这个会话用。读代码、跑命令都在这个目录里。</div>
            <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
              <button
                className="c-btn c-btn--primary c-btn--sm"
                style={{ flex: 1 }}
                disabled={!draft}
                onClick={() => {
                  onConfig({ workDir: draft })
                  setOpen(false)
                }}
              >
                应用
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}


function chipStyle(active: boolean): React.CSSProperties {
  return {
    maxWidth: 150,
    padding: '3px 8px',
    borderRadius: 'var(--radius-full)',
    border: `1px solid ${active ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
    background: active ? 'var(--accent-dim)' : 'var(--bg-surface)',
    color: active ? 'var(--accent-bright)' : 'var(--text-tertiary)',
    fontSize: 'var(--text-xs)',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    cursor: 'pointer',
    flexShrink: 0,
  }
}

const popoverLabel: React.CSSProperties = {
  fontSize: 'var(--text-xs)',
  color: 'var(--text-tertiary)',
  marginBottom: 6,
  fontWeight: 500,
}

const hintStyle: React.CSSProperties = {
  fontSize: 'var(--text-xs)',
  color: 'var(--text-disabled)',
  marginTop: 4,
  lineHeight: 'var(--leading-normal)',
}

/**
 * shortPath 把绝对路径压成「能认出来是哪个项目」的最短形式。
 * 只取最后一级时，/a/b/src 和 /x/y/src 长得一样，用户分不清。
 */
function shortPath(dir: string): string {
  if (!dir) return '未设置'
  const parts = dir.split('/').filter(Boolean)
  if (parts.length === 0) return dir
  return parts.length === 1 ? parts[0] : parts.slice(-2).join('/')
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
        // 不许被压成竖排：「对话」两个字竖着排是纯视觉故障
        whiteSpace: 'nowrap',
        flexShrink: 0,
      }}
    >
      {children}
    </button>
  )
}

const STATUS_MAP: Record<string, { cls: string; t: string }> = {
  idle: { cls: '', t: '空闲' },
  blocked: { cls: 'c-badge--danger', t: '等你处理' },
  running: { cls: 'c-badge--warning', t: '运行中' },
  queued: { cls: 'c-badge--accent', t: '排队' },
  done: { cls: 'c-badge--success', t: '完成' },
  error: { cls: 'c-badge--danger', t: '出错' },
}

/** 一个 agent 卡在排队多久算「卡住了」。 */
const STUCK_MS = 20_000

function StatusBadge({
  status,
  queueLen,
  since,
}: {
  status: string
  queueLen?: number
  /** 进入排队的时刻（ms）。用来区分「刚排队」和「卡住了」。 */
  since?: number
}) {
  // 「排队」和「卡住」在界面上长得一样，但处置方式完全不同：
  // 前者等着就好，后者必须重启。
  //
  // 判断交给 useStuckSinceHook 里的定时器，不在渲染时算 Date.now()：
  // 流式输出时这个组件每帧都渲染，读时钟既不产生新信息，
  // 又让组件无法被 memo 跳过。
  const stuck = useStuck(since)
  if (stuck) {
    return (
      <span className="c-badge c-badge--danger" title="长时间没有推进，后端可能已停止处理队列">
        卡住了
      </span>
    )
  }
  const s = STATUS_MAP[status] ?? STATUS_MAP.idle
  const label = status === 'queued' && queueLen ? `排队 ${queueLen}` : s.t
  return <span className={`c-badge ${s.cls}`}>{label}</span>
}

/**
 * useStuck 判断「进入 since 时刻后是否已经超过阈值」。
 *
 * 只在排队时开一个低频定时器（每 5 秒探一次），而不是每帧读时钟。
 * 排队是有尽头的，所以定时器会在状态变化时自动停掉。
 */
function useStuck(since?: number): boolean {
  const [stuck, setStuck] = useState(false)

  useEffect(() => {
    if (!since) {
      setStuck(false)
      return
    }
    const check = () => setStuck(Date.now() - since > STUCK_MS)
    check()
    // 5 秒一次足够：阈值是 20 秒，界面不需要秒级精度
    const id = setInterval(check, 5000)
    return () => clearInterval(id)
  }, [since])

  return stuck
}

function ShellCard({ logs }: { logs: ShellLog[] }) {
  const groups = groupShellLogs(logs)
  const pending = logs.some((l) => !l.result)
  const [open, setOpen] = useState(pending)
  const masked = groups.filter((g) => g.kind === 'cmd' && g.masked).length
  const repeats = groups.reduce((n, g) => n + (g.kind === 'cd' && g.count > 1 ? g.count - 1 : 0), 0)

  useEffect(() => {
    if (pending) setOpen(true)
  }, [pending])

  return (
    <div className="c-think">
      <button type="button" className="c-think__bar" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <span className={pending ? 'c-think__pulse c-think__pulse--live' : 'c-think__pulse c-think__pulse--done'} />
        <span className="c-think__title">
          工具 {logs.length} 条
          {repeats > 0 ? ` · 重复 cd ${repeats}` : ''}
          {masked > 0 ? ` · 已脱敏 ${masked}` : ''}
        </span>
        <span className="c-think__toggle">{open ? '收起' : '展开'}</span>
      </button>
      {open && (
        <div className="c-think__body">
          {groups.map((g) => (
            <ShellGroupRow key={g.kind === 'cd' ? g.ids[0] : g.id} group={g} />
          ))}
        </div>
      )}
    </div>
  )
}

function ShellGroupRow({ group }: { group: ShellGroup }) {
  if (group.kind === 'cd') {
    return (
      <div
        className="shell-card__row mono"
        style={{
          display: 'flex',
          alignItems: 'baseline',
          gap: 8,
          padding: '8px 12px',
          fontSize: 'var(--text-xs)',
          color: 'var(--text-secondary)',
        }}
      >
        <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>cd {group.path}</span>
        <span style={{ marginLeft: 'auto', flexShrink: 0, color: 'var(--text-disabled)' }}>× {group.count}</span>
      </div>
    )
  }
  const ok = group.exitCode === undefined || group.exitCode === 0
  return (
    <div className="shell-card__row" style={{ padding: '8px 12px' }}>
      <div
        className="mono"
        style={{
          display: 'flex',
          alignItems: 'baseline',
          gap: 8,
          fontSize: 'var(--text-xs)',
          color: 'var(--text-secondary)',
        }}
      >
        <span style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
          <SecretText text={group.shown} />
        </span>
        {group.pending && (
          <span className="blink" style={{ flexShrink: 0, color: 'var(--warning)' }}>
            执行中…
          </span>
        )}
        {group.exitCode !== undefined && (
          <span style={{ marginLeft: 'auto', flexShrink: 0, color: ok ? 'var(--success)' : 'var(--danger)' }}>
            exit {group.exitCode}
          </span>
        )}
      </div>
      {group.output && (
        <pre
          className="mono"
          style={{
            margin: '6px 0 0',
            padding: '8px 10px',
            borderRadius: 'var(--radius-md)',
            border: '1px solid var(--border-subtle)',
            background: 'var(--bg-inset)',
            fontSize: 'var(--text-xs)',
            lineHeight: 1.6,
            color: ok ? 'var(--text-secondary)' : 'var(--danger)',
            maxHeight: 160,
            overflow: 'auto',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
          }}
        >
          <SecretText text={group.output} />
        </pre>
      )}
    </div>
  )
}

function EmptyChat({ color, name, onPick }: { color: string; name: string; onPick: (text: string) => void }) {
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
        这个会话是独立的，不影响其他 agent。点下面的例子直接开始：
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
        {hints.map((h) => (
          <button
            key={h}
            type="button"
            onClick={() => onPick(h)}
            className="c-hint-chip"
          >
            {h}
          </button>
        ))}
      </div>
    </div>
  )
}

/**
 * Bubble 是单条消息的展示。
 *
 * 用 memo 包起来的原因：历史消息内容不会变，但流式输出期间父组件每秒
 * 重渲染几十次。不 memo 的话每次都要重新解析一遍所有历史消息的 Markdown，
 * 长对话下会明显卡顿。比较只看真正影响渲染的字段。
 */
const Bubble = memo(
  BubbleImpl,
  (a, b) =>
    a.m.role === b.m.role &&
    a.m.content === b.m.content &&
    a.m.reasoning === b.m.reasoning &&
    a.m.timestamp === b.m.timestamp &&
    a.tone === b.tone &&
    a.name === b.name &&
    a.model === b.model &&
    a.workLabel === b.workLabel &&
    a.elapsed === b.elapsed &&
    a.live === b.live &&
    a.tokens?.in === b.tokens?.in &&
    a.tokens?.out === b.tokens?.out &&
    a.agents === b.agents &&
    a.peers === b.peers,
)

type BubbleProps = {
  m: Message
  tone: number
  name: string
  model: string
  workLabel: string
  elapsed?: string
  live?: boolean
  tokens?: { in: number; out: number }
  agents?: MentionTarget[]
  peers?: { id: string; name: string }[]
  onContextMenu?: (e: React.MouseEvent, msg: Message) => void
  onCopy?: (text: string) => void
  onRegenerate?: (msg: Message) => void
  onRoute?: (targetId: string, text: string) => void
  onOpenFile?: (path: string, line: number) => void
  onOpenKnowledge?: () => void
}

function BubbleImpl({
  m,
  tone,
  name,
  model,
  workLabel: place,
  elapsed,
  live,
  tokens,
  agents,
  peers,
  onContextMenu,
  onCopy,
  onRegenerate,
  onRoute,
  onOpenFile,
  onOpenKnowledge,
}: BubbleProps) {
  const [routeOpen, setRouteOpen] = useState(false)
  const isUser = m.role === 'user'
  const parsed = parseMessage(m.content)
  const isRouted = parsed.isRouted
  const routeInfo = parsed.from ? `来自 ${parsed.from}` : parsed.to ? `发给 ${parsed.to}` : ''
  const displayContent = parsed.body
  const artifacts = isUser ? [] : extractArtifacts(displayContent)
  const toneStyle = { '--tone': `var(--agent-${tone})` } as CSSProperties
  const md = useMemo(() => markdownComponents(onOpenFile), [onOpenFile])
  const clock = m.timestamp
    ? new Date(m.timestamp * 1000).toLocaleTimeString('zh-CN', {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      })
    : ''

  if (isUser) {
    return (
      <div
        className="c-msg c-msg--user fade-up"
        style={{ marginBottom: 'var(--space-4)' }}
        onContextMenu={onContextMenu ? (e) => onContextMenu(e, m) : undefined}
      >
        <div className="c-user-bubble">
          <div style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            <MentionText text={displayContent} agents={agents} selfColor={`var(--agent-${tone})`} />
          </div>
          {clock && (
            <time style={{ display: 'block', marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
              {clock}
            </time>
          )}
        </div>
      </div>
    )
  }

  return (
    <div
      className="c-msg c-msg--agent fade-up"
      style={{ marginBottom: 'var(--space-4)', opacity: isRouted ? 0.85 : 1 }}
      onContextMenu={onContextMenu ? (e) => onContextMenu(e, m) : undefined}
    >
      <div className="c-agent-head" style={toneStyle}>
        <span className="c-agent-avatar" aria-hidden>
          {isRouted ? '✉' : name.slice(0, 1).toUpperCase()}
        </span>
        <span className="c-agent-name">
          <i className="c-agent-dot" />
          {isRouted ? routeInfo : name}
        </span>
        {model && <span className="c-pill">{model}</span>}
        {place && <span className="c-pill mono">{place}</span>}
        <span className="c-agent-meta">
          {elapsed && <span>{elapsed}</span>}
          {tokens && (tokens.in > 0 || tokens.out > 0) && (
            <span>
              ↑ {formatTokens(tokens.in)} ↓ {formatTokens(tokens.out)}
            </span>
          )}
          {clock && <time>{clock}</time>}
          {live && <span className="c-think__pulse c-think__pulse--live" />}
        </span>
        <div className="c-msg-actions">
          <button type="button" className="c-icon-btn" title="复制 Markdown" onClick={() => onCopy?.(displayContent)}>
            复制
          </button>
          {onRegenerate && (
            <button type="button" className="c-icon-btn" title="重新生成" onClick={() => onRegenerate(m)}>
              重新生成
            </button>
          )}
          {onRoute && peers && peers.length > 0 && (
            <span className="c-route">
              <button type="button" className="c-icon-btn" title="Route 派发给其他 Agent" onClick={() => setRouteOpen((v) => !v)}>
                Route
              </button>
              {routeOpen && (
                <span className="c-route__menu">
                  {peers.map((peer) => (
                    <button
                      key={peer.id}
                      type="button"
                      className="c-ctx-item"
                      onClick={() => {
                        onRoute(peer.id, displayContent.slice(0, 200))
                        setRouteOpen(false)
                      }}
                    >
                      {peer.name}
                    </button>
                  ))}
                </span>
              )}
            </span>
          )}
        </div>
      </div>
      <div className="markdown-body" style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, wordBreak: 'break-word', color: 'var(--text-secondary)' }}>
        {m.reasoning && <ReasoningCard text={m.reasoning} live={live} />}
        <ReactMarkdown remarkPlugins={[remarkGfm]} components={md}>
          {displayContent}
        </ReactMarkdown>
      </div>
      {artifacts.length > 0 && (
        <div className="c-artifacts">
          {artifacts.map((a) =>
            a.kind === 'note' ? (
              <button key={a.label} type="button" className="c-artifact" onClick={() => onOpenKnowledge?.()}>
                {a.label}
              </button>
            ) : (
              <button key={a.path} type="button" className="c-artifact" onClick={() => onOpenFile?.(a.path, 1)}>
                {a.label}
              </button>
            ),
          )}
        </div>
      )}
    </div>
  )
}

function markdownComponents(onOpen?: (path: string, line: number) => void) {
  const wrap = (Tag: 'p' | 'li' | 'td' | 'blockquote' | 'h1' | 'h2' | 'h3') =>
    function Wrapped(props: { children?: ReactNode }) {
      return <Tag>{walk(props.children, onOpen)}</Tag>
    }
  return {
    p: wrap('p'),
    li: wrap('li'),
    td: wrap('td'),
    blockquote: wrap('blockquote'),
    h1: wrap('h1'),
    h2: wrap('h2'),
    h3: wrap('h3'),
  }
}

function walk(node: ReactNode, onOpen?: (path: string, line: number) => void): ReactNode {
  if (typeof node === 'string') return renderAnchors(node, onOpen)
  if (typeof node === 'number') return node
  if (Array.isArray(node)) {
    return node.map((child, i) => <Fragment key={i}>{walk(child, onOpen)}</Fragment>)
  }
  if (isValidElement<{ children?: ReactNode }>(node)) {
    if (node.type === 'pre') return node
    return cloneElement(node, node.props, walk(node.props.children, onOpen))
  }
  return node
}

function renderAnchors(text: string, onOpen?: (path: string, line: number) => void) {
  const parts = splitAnchors(text)
  if (parts.length === 1 && parts[0].kind === 'text') return text
  return parts.map((part, i) =>
    part.kind === 'text' ? (
      <Fragment key={i}>{part.text}</Fragment>
    ) : (
      <button key={i} type="button" className="c-code-anchor mono" onClick={() => onOpen?.(part.path, part.line)}>
        {part.text}
      </button>
    ),
  )
}

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return `${n}`
}

function ContextMeter({
  messages,
  streaming,
  usage,
  provider,
  contextWindow,
}: {
  messages: { content?: string }[]
  streaming?: string
  usage?: { in: number; out: number; lastIn?: number }
  provider: string
  contextWindow?: number
}) {
  const meter = contextMeter({ messages, streaming, usage, provider, contextWindow })
  const ratio = meter.limit > 0 ? meter.used / meter.limit : 0
  const hot = ratio >= 0.85
  return (
    <span
      title={
        meter.estimated
          ? '模型还没返回用量，这是按文本粗估的上下文。消耗会在接口带回 token 后出现。'
          : '上下文是最近一次请求装进窗口的量。消耗是累计输入加输出。'
      }
      style={{ color: hot ? 'var(--warning)' : 'var(--text-tertiary)', whiteSpace: 'nowrap' }}
    >
      上下文 {meter.estimated ? '约 ' : ''}
      {formatTokens(meter.used)} / {formatTokens(meter.limit)}
      {meter.spent > 0 ? ` · 消耗 ${formatTokens(meter.spent)}` : ''}
    </span>
  )
}

function SecretText({ text }: { text: string }) {
  const parts = text.split(/(Bearer ••••\S+|sk-••••\S+|pt-••••\S+|api_key=••••\S+)/g)
  return (
    <>
      {parts.map((part, i) =>
        /^(?:Bearer ••••|sk-••••|pt-••••|api_key=••••)\S+$/.test(part) ? (
          <span key={i} className="c-secret">
            {part}
          </span>
        ) : (
          <Fragment key={i}>{part}</Fragment>
        ),
      )}
    </>
  )
}

function ApprovalCard({
  tool,
  target,
  onDecide,
}: {
  tool: string
  target: string
  onDecide: (kind: 'allow' | 'deny' | 'always') => void
}) {
  const shown = redactCommand(target || tool)
  return (
    <div className="c-approval">
      <div className="c-approval__title">待审批 · {tool}</div>
      {shown && (
        <div className="c-approval__cmd">
          <SecretText text={shown} />
        </div>
      )}
      <div className="c-approval__actions">
        <button type="button" className="c-btn c-btn--sm" onClick={() => onDecide('allow')}>
          允许执行
        </button>
        <button type="button" className="c-btn c-btn--ghost c-btn--sm" onClick={() => onDecide('deny')}>
          拒绝
        </button>
        <button type="button" className="c-btn c-btn--ghost c-btn--sm" onClick={() => onDecide('always')}>
          本会话始终允许
        </button>
      </div>
    </div>
  )
}
/** 右键菜单项：统一 hover / 焦点样式。 */
function MenuItem({
  onSelect,
  icon,
  shortcut,
  children,
}: {
  onSelect: () => void
  icon?: React.ReactNode
  shortcut?: string
  children: React.ReactNode
}) {
  return (
    <button type="button" role="menuitem" className="c-ctx-item" onClick={onSelect}>
      {icon && (
        <span aria-hidden className="c-ctx-item__icon">
          {icon}
        </span>
      )}
      <span style={{ flex: 1, minWidth: 0 }}>{children}</span>
      {shortcut && <span className="c-ctx-item__key">{shortcut}</span>}
    </button>
  )
}

function MenuSeparator() {
  return <div className="c-ctx-sep" />
}

function MenuLabel({ children }: { children: React.ReactNode }) {
  return <div className="c-ctx-label">{children}</div>
}
