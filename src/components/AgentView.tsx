import { Fragment, memo, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import TextareaAutosize from 'react-textarea-autosize'
import { ImageStrip, readImageFiles, type DraftImage } from './ImageDraft'
import ModelPicker from './ModelPicker'
import PermissionPicker from './PermissionPicker'
import { Popover } from 'radix-ui'
import MentionPicker from './MentionPicker'
import type { MentionTarget } from './MentionText'
import { CopyButton, ForwardMenu, MessageBody, StreamingMarkdown } from './MessageRow'
import { ReasoningCard, Thinking } from './ThinkRail'
import { API, type AgentRuntimeRequest, type AgentView as Agent, type Channel, type Config, type Message } from '../mygo'
import { runtimeBadge, runtimeStateOf, splitLines, type RuntimeKind } from '../lib/runtime'
import { indexIncoming, peerCallsOf, toolTextById, type Caller, type PeerCall } from '../lib/agent-calls'
import type { SessionError, ShellLog } from '../lib/useBackend'
import { parseMessage } from '../lib/message'
import { foldAssistantTranscript, groupShellLogs, type ShellGroup } from '../lib/transcriptPresent'
import {
  agentTone,
  elapsedBetween,
  extractArtifacts,
  prevUserStamp,
  workLabel,
} from '../lib/messagePresent'
import DirectoryPicker from './DirectoryPicker'
import { VirtualList } from './VirtualList'
import { contextMeter, estimateTokens } from '../lib/contextMeter'
import { previewMode } from '../lib/preview-runtime'

type Props = {
  agent: Agent | null
  streaming: string
  error?: SessionError
  workDir: string
  tokenUsage?: { in: number; out: number; lastIn?: number }
  onPrompt: (text: string, images?: { mime: string; data: string }[]) => void | Promise<unknown>
  onStop: () => void
  /** 还有更早的历史没加载。为真时在顶部显示「加载更早」。 */
  hasOlder?: boolean
  onLoadOlder?: () => void
  onShell: (command: string) => void
  /** 打开检查器的「改动」页：对话里的改动清单点击后跳到这里看 diff */
  onOpenChanges?: () => void
  /** 本轮改动过的文件（按路径去重，来自全部工具事件，不受 steps 截断影响） */
  changes?: { path: string; diff?: string }[]
  /** 本会话执行过的命令，直接渲染在对话流里 */
  shellLogs?: ShellLog[]
  /** 这一轮已经发生的工具步骤。思考中靠它显示在读什么、跑了什么。 */
  steps?: { tool: string; target: string; status: 'running' | 'ok' | 'error' | 'waiting' | 'cancelled'; detail?: string; path?: string; diff?: string }[]
  /** 模型这一轮的思考文本，还没写进消息时先显示在这里。 */
  reasoning?: string
  /** 这一轮请求的等待计时。first 是到首个推理或正文的毫秒。 */
  pace?: { since: number; first?: number; heard?: 'reason' | 'text' }
  onToggleInspector: () => void
  inspectorOpen: boolean
  /** 单聊模式：禁用 @ 路由功能 */
  disableRoute?: boolean
  onRoute?: (targetName: string, text: string) => void
  agents?: Agent[]
  /** 会话级配置变更（工作目录 / 模型），只影响当前会话 */
  onConfig: (patch: { workDir?: string; extraDirs?: string[]; llm?: Config }) => void
  /** 空状态下的一键创建：传角色预设，直接建出可用 agent */
  onQuickCreate?: (presetName: string, role: string) => void
  /** 空状态下打开「新建 Agent」弹窗 */
  onCreateRequest?: () => void
  /** 正文里的代码锚点：打开检查器并定位文件 */
  /** 知识库沉淀胶囊：打开检查器的知识库 */
  onOpenKnowledge?: () => void
  /** 改给人看的名字和简介 */
  onProfile?: (name: string, role: string) => void
  /** 设置里已确认的连接。选模型时凭据从这里带上，不在会话里再填一遍 Key。 */
  connection?: { provider: string; baseUrl: string; apiKey: string; model: string }
  /** 全部频道：群工作不进单聊历史，靠它在单聊里标出「他在哪些群干活」并跳转 */
  channels?: Channel[]
  onOpenChannel?: (id: string) => void
}

// 空状态的一键角色。只保留 3 个 —— 选择过多反而增加决策成本。
const quickRoles = [
  { emoji: '🧩', label: '前端', name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理与渲染性能' },
  { emoji: '⚙️', label: '后端', name: '后端', role: 'Go 专家，关注 API 设计、并发安全与查询效率' },
  { emoji: '🔍', label: '审查', name: 'Reviewer', role: '代码审查者，指出风险、代码异味与不符合规范的地方' },
] as const

type Shown = { m: Message; calls: PeerCall[] }

type TranscriptRow = { kind: 'msg'; m: Message; calls: PeerCall[]; idx: number } | { kind: 'err'; text: string }

function transcriptRows(visible: Shown[], errorAt: number, error?: string): TranscriptRow[] {
  const rows: TranscriptRow[] = []
  for (let i = 0; i < errorAt && i < visible.length; i++) {
    rows.push({ kind: 'msg', m: visible[i].m, calls: visible[i].calls, idx: i })
  }
  if (error) rows.push({ kind: 'err', text: error })
  for (let i = errorAt; i < visible.length; i++) {
    rows.push({ kind: 'msg', m: visible[i].m, calls: visible[i].calls, idx: i })
  }
  return rows
}

function presentTranscript(messages: Message[]): Shown[] {
  const byId = toolTextById(messages)
  const out: Shown[] = []
  for (const row of foldAssistantTranscript(messages)) {
    const calls = row.sources.flatMap((m) => (m.role === 'assistant' ? peerCallsOf(m, byId) : []))
    if (row.item.role === 'assistant' && !(row.item.content || '').trim() && !row.item.reasoning && calls.length === 0) continue
    out.push({ m: row.item, calls })
  }
  return out
}

/**
 * TurnChanges 把「这一轮到底动了哪些文件」摆在对话里。
 *
 * 之前这些信息只散在检查器的工具列表里，还混着 Bash 的原始 JSON；
 * 用户看完一段回答仍然不知道改了哪个文件、能不能点开看 diff。
 * 这里按路径去重，给一行可点击的清单，点击直接跳到检查器的 diff。
 */
function TurnChanges({
  files,
  onOpen,
}: {
  files: { path: string; diff?: string }[]
  onOpen?: () => void
}) {
  if (files.length === 0) return null
  const stat = (d?: string) => {
    if (!d) return ''
    const lines = d.split('\n')
    const add = lines.filter((l) => l.startsWith('+') && !l.startsWith('+++')).length
    const del = lines.filter((l) => l.startsWith('-') && !l.startsWith('---')).length
    if (add === 0 && del === 0) return ''
    return `+${add} −${del}`
  }
  return (
    <div
      style={{
        marginTop: 'var(--space-3)',
        border: '1px solid var(--border-subtle)',
        borderRadius: 'var(--radius-md)',
        background: 'var(--bg-surface)',
        padding: '9px 11px',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          本轮改了 {files.length} 个文件
        </span>
        {onOpen && (
          <button
            className="c-btn c-btn--ghost c-btn--sm"
            style={{ marginLeft: 'auto' }}
            onClick={onOpen}
          >
            在检查器里看 diff
          </button>
        )}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
        {files.map((f) => (
          <button
            key={f.path}
            type="button"
            onClick={onOpen}
            title={f.diff || f.path}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              textAlign: 'left',
              padding: '3px 6px',
              borderRadius: 'var(--radius-sm)',
              background: 'var(--bg-inset)',
              color: 'var(--text-secondary)',
              fontSize: 'var(--text-xs)',
              cursor: onOpen ? 'pointer' : 'default',
            }}
          >
            <span className="mono" style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {f.path}
            </span>
            {f.diff && <span style={{ color: 'var(--text-tertiary)' }}>{stat(f.diff)}</span>}
          </button>
        ))}
      </div>
    </div>
  )
}

// 更早的对话已经压缩成摘要时，在名字下面说一声：这些消息还在对话里，
// 只是不再原样发给模型。没压过就什么都不显示。
function CompactNote({ agent }: { agent: Agent }) {
  const covered = agent.ctxCovered ?? 0
  if (covered <= 0) return null
  return (
    <div className="c-learn">
      <div className="c-learn__text">{`更早的 ${covered} 条已压缩成摘要`}</div>
    </div>
  )
}
function ProfileHead({
  agent,
  onProfile,
}: {
  agent: Agent
  onProfile?: (name: string, role: string) => void
}) {
  const [name, setName] = useState(agent.name)
  const [role, setRole] = useState(agent.role)
  useEffect(() => {
    setName(agent.name)
    setRole(agent.role)
  }, [agent.id, agent.name, agent.role])
  const save = (nextName: string, nextRole: string) => {
    const n = nextName.trim()
    const r = nextRole.trim()
    if (!n || !r || (n === agent.name && r === agent.role)) return
    onProfile?.(n, r)
  }
  return (
    <div style={{ minWidth: 0 }}>
      <input
        aria-label="会话名字"
        value={name}
        onChange={(e) => setName(e.target.value)}
        onBlur={() => save(name, role)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
        }}
        style={{
          fontSize: 'var(--text-md)',
          fontWeight: 600,
          lineHeight: 1.3,
          border: 'none',
          background: 'transparent',
          color: 'inherit',
          padding: 0,
          width: 220,
        }}
      />
      <input
        aria-label="会话简介"
        value={agent.busy ? `正在${agent.busy}里回答` : role}
        readOnly={!!agent.busy}
        onChange={(e) => setRole(e.target.value)}
        onBlur={() => save(name, role)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
        }}
        style={{
          display: 'block',
          fontSize: 'var(--text-xs)',
          color: 'var(--text-tertiary)',
          border: 'none',
          background: 'transparent',
          padding: 0,
          width: 300,
        }}
      />
      <CompactNote agent={agent} />
      {agent.proposal && !agent.busy && (
        <button
          type="button"
          onClick={() => onProfile?.(agent.name, agent.proposal || '')}
          style={{
            border: 'none',
            background: 'transparent',
            color: 'var(--accent-bright)',
            fontSize: 'var(--text-xs)',
            padding: 0,
            cursor: 'pointer',
          }}
        >
          改成「{agent.proposal}」
        </button>
      )}
    </div>
  )
}

export default function AgentView(p: Props) {
  const [input, setInput] = useState('')
  const [images, setImages] = useState<DraftImage[]>([])
  const [imageNote, setImageNote] = useState('')
  const [mode, setMode] = useState<'chat' | 'shell'>('chat')
  const [contextMenu, setContextMenu] = useState<{ x: number; y: number; msg: Message } | null>(null)
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<HTMLTextAreaElement>(null)
  const stickToBottom = useRef(true)
  const programmaticScroll = useRef(false)
  const gesturing = useRef(false)
  const gestureTimer = useRef(0)

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

  const markGesture = () => {
    gesturing.current = true
    window.clearTimeout(gestureTimer.current)
    gestureTimer.current = window.setTimeout(() => {
      gesturing.current = false
    }, 180)
  }

  // 只有用户自己把滚动条拉开，才停止跟随。程序把视口推到底时不算。
  // 手指还按在滚动上时不改 scrollTop，否则和回弹抢位置，底部会抖。
  const handleScroll = () => {
    if (programmaticScroll.current) return
    const el = scrollRef.current
    if (!el) return
    stickToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80
  }

  const followBottom = () => {
    const el = scrollRef.current
    if (!el || !stickToBottom.current || gesturing.current) return
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight
    if (gap <= 1) return
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
  const [incoming, setIncoming] = useState<Record<string, Caller>>({})
  useEffect(() => {
    const self = p.agent?.id
    if (!self) return
    let dead = false
    const others = (p.agents ?? []).filter((a) => a.id !== self)
    Promise.all(
      others.map((a) =>
        API.agentMessages(a.id, 80)
          .then((messages) => ({ id: a.id, name: a.name, color: a.color, messages: messages ?? [] }))
          .catch(() => ({ id: a.id, name: a.name, color: a.color, messages: [] })),
      ),
    ).then((rows) => {
      if (!dead) setIncoming(indexIncoming(rows))
    })
    return () => {
      dead = true
    }
  }, [p.agent?.id, mentionKey])


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

  const addImages = async (files: File[]) => {
    const got = await readImageFiles(files)
    if (got.error) setImageNote(got.error)
    if (got.images.length === 0) return
    setImages((prev) => [...prev, ...got.images].slice(0, 8))
  }

  const submit = () => {
    const text = input.trim()
    if (mode === 'shell') {
      if (!text) return
      p.onShell(text)
    } else {
      if (!text && images.length === 0) return
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
        const sent = p.onPrompt(text, images.map((img) => ({ mime: img.mime, data: img.data })))
        Promise.resolve(sent)
          .then(() => {
            setInput('')
            setImages([])
            setImageNote('')
          })
          .catch((err: unknown) => {
            const msg = err instanceof Error ? err.message : String(err)
            setImageNote(msg || '发送失败')
          })
        return
      }
    }
    setInput('')
    setImages([])
    setImageNote('')
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
  const hasDraft = !!input.trim() || (mode === 'chat' && images.length > 0)
  const rawMessages = p.agent.messages ?? []
  const visible = presentTranscript(rawMessages)
  const visibleMsgs = visible.map((row) => row.m)
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
    speakerColor: agent.color,
  }
  const renderMsg = (m: Message, idx: number, calls: PeerCall[]) => (
    <Bubble
      key={idx}
      m={m}
      calls={calls}
      fromAgent={m.role === 'user' ? incoming[(m.content || '').trim()] : undefined}
      {...head}
      elapsed={m.role === 'assistant' ? elapsedBetween(prevUserStamp(visibleMsgs, idx), m.timestamp) : ''}
      agents={agentsForPicker}
      tokens={idx === visible.length - 1 && m.role === 'assistant' ? p.tokenUsage : undefined}
      onContextMenu={handleContextMenu}
      onCopy={(text) => navigator.clipboard.writeText(text)}
      onRegenerate={(msg) => p.onPrompt(msg.content)}
      onRoute={p.onRoute}
      onOpenKnowledge={p.onOpenKnowledge}
    />
  )

  return (
    <div className="c-col" style={{ flex: 1 }}>
      {/* 头部 */}
      <header
        style={{
          minHeight: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '8px var(--space-4)',
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
            fontSize: 'var(--text-sm)',
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          {p.agent.name.slice(0, 1).toUpperCase()}
        </span>
          <ProfileHead agent={p.agent} onProfile={p.onProfile} />

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
            aria-label={p.inspectorOpen ? '收起检查器' : '展开检查器'}
          >
            ◫
          </button>
        </div>
      </header>

      <div className="c-session-telemetry" data-session-telemetry>
        <div className="c-session-telemetry__env">
          <RuntimeChip agent={p.agent} />
          <WorkDirChip agent={p.agent} onConfig={p.onConfig} />
        </div>
        <ContextMeter
          messages={rawMessages}
          streaming={p.streaming}
          usage={p.tokenUsage}
          provider={p.agent.llm?.provider || p.connection?.provider || ''}
          model={p.agent.llm?.model || p.connection?.model || ''}
          contextWindow={p.agent.llm?.contextWindow}
        />
      </div>

      {/* 频道动态：群聊回合不写进单聊历史，这里给出标识和入口，避免两边工作分不清 */}
      <ChannelStrip
        agent={p.agent}
        channels={p.channels ?? []}
        onOpenChannel={p.onOpenChannel}
      />

      {/* 消息流 */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        onWheel={markGesture}
        onTouchStart={markGesture}
        onPointerDown={markGesture}
        style={{
          flex: 1,
          overflowY: 'auto',
          overscrollBehavior: 'none',
          padding: 'var(--space-4) var(--space-6) var(--space-2)',
        }}
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

        {p.hasOlder && p.onLoadOlder && (
          <div style={{ display: 'flex', justifyContent: 'center', padding: 'var(--space-2) 0' }}>
            <button className="c-btn c-btn--ghost c-btn--sm" onClick={p.onLoadOlder}>
              加载更早的对话
            </button>
          </div>
        )}

        <VirtualList
          items={transcriptRows(visible, errorAt, p.error?.text)}
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
              renderMsg(row.m, row.idx, row.calls)
            )
          }
        />

        {p.streaming && !p.agent.busy && (
          <Bubble
            m={{ role: 'assistant', content: p.streaming }}
            {...head}
            live
            onCopy={(text) => navigator.clipboard.writeText(text)}
            onOpenKnowledge={p.onOpenKnowledge}
          />
        )}

        {running && !p.agent.busy && !p.streaming && (
          <Thinking steps={p.steps ?? []} reasoning={p.reasoning} pace={p.pace} />
        )}
        {!!p.streaming && !p.agent.busy && ((p.steps?.length ?? 0) > 0 || !!p.reasoning) && (
          <Thinking steps={p.steps ?? []} reasoning={p.reasoning} pace={p.pace} />
        )}

        {!running && <TurnChanges files={p.changes ?? []} onOpen={p.onOpenChanges} />}

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
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            if (mode !== 'chat') return
            void addImages(Array.from(e.dataTransfer.files))
          }}
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
          {mode === 'chat' && (
            <ImageStrip
              images={images}
              onRemove={(i) => setImages((prev) => prev.filter((_, at) => at !== i))}
            />
          )}
          <TextareaAutosize
            ref={taRef}
            value={input}
            onChange={(e) => handleInputChange(e.target.value)}
            onPaste={(e) => {
              const files = Array.from(e.clipboardData.files)
              if (files.length === 0 || mode !== 'chat') return
              e.preventDefault()
              void addImages(files)
            }}
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
              // 窄窗口按组换行，不把模型与发送按钮挤成竖排或裁出可视区。
              flexWrap: 'wrap',
              rowGap: 4,
            }}
          >
            {mode === 'chat' && (
              <label className="c-icon-btn" title="选择图片" style={{ cursor: 'pointer' }}>
                图
                <input
                  type="file"
                  accept="image/png,image/jpeg,image/gif,image/webp"
                  hidden
                  onChange={(e) => {
                    void addImages(Array.from(e.target.files ?? []))
                    e.target.value = ''
                  }}
                />
              </label>
            )}
            <ModeBtn active={mode === 'chat'} onClick={() => setMode('chat')}>
              对话
            </ModeBtn>
            <ModeBtn active={mode === 'shell'} onClick={() => setMode('shell')}>
              终端
            </ModeBtn>

            {/* 模型与权限影响下一轮对话，留在输入栏；低频路径与运行时收敛到顶部。 */}
            <div
              style={{
                marginLeft: 'auto',
                display: 'flex',
                alignItems: 'center',
                gap: 4,
                // 组内也允许换行，权限、模型、发送各自保持完整点击目标。
                minWidth: 0,
                maxWidth: '100%',
                flexShrink: 1,
                flexWrap: 'wrap',
              }}
            >
              {/* 模型选择直接长在输入框底栏：用户换模型时不用离开对话。
                  工作目录仍然走弹层（它不常改，且需要输入路径）。 */}
              <PermissionPicker agentId={p.agent.id} />
              <ModelPicker
                agentId={p.agent.id}
                model={p.agent.llm?.model || ''}
                provider={p.agent.llm?.provider}
                thinking={p.agent.llm?.thinking}
                onPick={(m, nextThinking) => {
                  const conn = p.connection
                  const same = !!conn && conn.provider === m.provider
                  const thinking = nextThinking ?? p.agent?.llm?.thinking
                  const keep = thinking && (m.efforts ?? []).includes(thinking) ? thinking : undefined
                  p.onConfig({
                    llm: {
                      provider: m.provider,
                      baseUrl: m.baseUrl || (same ? conn.baseUrl : ''),
                      apiKey: same ? conn.apiKey : '',
                      model: m.model,
                      ...(keep ? { thinking: keep } : {}),
                    },
                  })
                }}
                onThinking={(next) => {
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
              {mode === 'shell' ? '执行' : turnLive ? '插入' : '发送'}
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
            {imageNote
                ? imageNote
                : turnLive
                  ? '这一轮还在进行。发出去会在下一步之前插进当前任务。停止会打断它，并清掉还没插进去的消息。'
                  : 'Enter 发送 · Shift+Enter 换行'}
          </span>
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
  onConfig: (patch: { workDir?: string; extraDirs?: string[]; llm?: Config }) => void
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState('')
  const [wtBusy, setWtBusy] = useState(false)
  const [wtErr, setWtErr] = useState('')
  const effective = agent.workDir

  // 独立工作树：开/关都只影响这个会话。
  // 状态走 API 返回的事件（agentConfig）刷新，这里不乐观地改本地状态。
  const toggleWorktree = async () => {
    setWtBusy(true)
    setWtErr('')
    try {
      await API.setAgentWorktree(agent.id, !agent.worktree)
      setOpen(false)
    } catch (e) {
      setWtErr(e instanceof Error && e.message ? e.message : '操作失败，稍后再试')
    } finally {
      setWtBusy(false)
    }
  }

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
        title={
          agent.worktree
            ? `工作树：${agent.worktree}（分支 ${agent.worktreeBranch}，仅影响本会话）`
            : `工作目录：${effective || '未设置'}（点击修改，仅影响本会话）`
        }
        style={chipStyle(open)}
      >
        {agent.worktree ? `⎇ ${agent.worktreeBranch || 'worktree'}` : `📁 ${shortPath(effective)}`}
        <span style={{ opacity: 0.5, marginLeft: 3 }}>▾</span>
      </button>

      {open && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 900 }} onClick={() => setOpen(false)} />
          <div
            className="fade-up"
            style={{
              position: 'absolute',
              top: 'calc(100% + 6px)',
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
            <div style={hintStyle}>只给这个会话用。命令在这个目录里跑。</div>

            <ExtraDirs agent={agent} onConfig={onConfig} />

            <div
              style={{
                marginTop: 10,
                paddingTop: 8,
                borderTop: '1px solid var(--border-subtle)',
                display: 'flex',
                gap: 8,
                alignItems: 'flex-start',
              }}
            >
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>独立 git worktree</div>
                <div style={hintStyle}>
                  {agent.worktree
                    ? `分支 ${agent.worktreeBranch}，改动只落在这份检出里。关闭后下一轮回到原目录。`
                    : '开启后立即建好工作树并切过去；正在跑的那一轮不受影响，下一轮生效。'}
                </div>
                {agent.worktree && (
                  <div
                    className="mono"
                    style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 3, wordBreak: 'break-all' }}
                  >
                    {agent.worktree}
                  </div>
                )}
                {wtErr && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)', marginTop: 4 }}>{wtErr}</div>
                )}
              </div>
              <button
                type="button"
                className="c-btn c-btn--ghost c-btn--sm"
                disabled={wtBusy || !effective}
                onClick={() => void toggleWorktree()}
              >
                {wtBusy ? '处理中…' : agent.worktree ? '关闭' : '开启'}
              </button>
            </div>

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

/**
 * RuntimeChip —— 本会话运行时（native / 外部 ACP），范式与 WorkDirChip 一致：
 * chip 按钮 + 绝对定位弹层 + Escape/点遮罩关闭 + 后端错误原文展示。
 *
 * 回填读 agent.acp；保存走 API.setAgentRuntime；成功后以 agent.config 事件
 * 刷新为准，这里不乐观地改本地状态。
 */
function RuntimeChip({ agent }: { agent: Agent }) {
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [draftRuntime, setDraftRuntime] = useState<RuntimeKind>(agent.runtime === 'acp' ? 'acp' : 'native')
  const [draftName, setDraftName] = useState(agent.acp?.name ?? '')
  const [draftCommand, setDraftCommand] = useState(agent.acp?.command ?? '')
  const [argsText, setArgsText] = useState((agent.acp?.args ?? []).join('\n'))
  const [envText, setEnvText] = useState((agent.acp?.env ?? []).join('\n'))

  // 切换会话时收起，并按当前 agent 的配置回填
  useEffect(() => {
    setOpen(false)
    setBusy(false)
    setErr('')
    setDraftRuntime(agent.runtime === 'acp' ? 'acp' : 'native')
    setDraftName(agent.acp?.name ?? '')
    setDraftCommand(agent.acp?.command ?? '')
    setArgsText((agent.acp?.args ?? []).join('\n'))
    setEnvText((agent.acp?.env ?? []).join('\n'))
  }, [agent.id, agent.runtime, agent.acp])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const badge = runtimeBadge(agent)
  const state = runtimeStateOf(agent.runtimeState, agent.runtimeError)
  const commandMissing = draftRuntime === 'acp' && draftCommand.trim() === ''

  const save = async () => {
    if (commandMissing || busy) return
    setBusy(true)
    setErr('')
    try {
      const req: AgentRuntimeRequest = { agentId: agent.id, runtime: draftRuntime }
      if (draftRuntime === 'acp') {
        req.name = draftName.trim()
        req.command = draftCommand.trim()
        req.args = splitLines(argsText)
        req.env = splitLines(envText)
      }
      await API.setAgentRuntime(req)
      setOpen(false)
    } catch (e) {
      setErr(e instanceof Error && e.message ? e.message : '保存失败，稍后再试')
    } finally {
      setBusy(false)
    }
  }

  const chipIcon = agent.runtime === 'acp' ? '🔌' : '🖥'

  return (
    <div style={{ position: 'relative' }}>
      <button
        className="mono"
        onClick={() => setOpen((v) => !v)}
        title={`${badge.label}${state.label ? ` · ${state.label}` : ''}${
          agent.runtimeError ? `\n${agent.runtimeError}` : ''
        }（点击修改，仅影响本会话）`}
        style={chipStyle(open)}
      >
        {chipIcon} {badge.label}
        <span style={{ opacity: 0.5, marginLeft: 3 }}>▾</span>
      </button>

      {open && (
        <>
          <div style={{ position: 'fixed', inset: 0, zIndex: 900 }} onClick={() => setOpen(false)} />
          <div
            className="fade-up"
            style={{
              position: 'absolute',
              top: 'calc(100% + 6px)',
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
            <div style={popoverLabel}>这个会话的运行时</div>
            <div style={{ display: 'flex', gap: 6 }}>
              <RuntimeSeg active={draftRuntime === 'native'} onClick={() => setDraftRuntime('native')}>
                🖥 原生
              </RuntimeSeg>
              <RuntimeSeg active={draftRuntime === 'acp'} onClick={() => setDraftRuntime('acp')}>
                🔌 外部 ACP
              </RuntimeSeg>
            </div>

            {draftRuntime === 'acp' && (
              <div style={{ display: 'grid', gap: 10, marginTop: 10 }}>
                <div>
                  <label style={LABEL_INLINE}>展示名（可选）</label>
                  <input
                    value={draftName}
                    onChange={(e) => setDraftName(e.target.value)}
                    placeholder="例如：grok；留空用命令名"
                    style={RUNTIME_FIELD}
                  />
                </div>
                <div>
                  <label style={LABEL_INLINE}>启动命令（必填）</label>
                  <input
                    value={draftCommand}
                    onChange={(e) => setDraftCommand(e.target.value)}
                    placeholder="例如：grok / codex-acp"
                    aria-invalid={commandMissing}
                    style={{ ...RUNTIME_FIELD, borderColor: commandMissing ? 'var(--danger)' : undefined }}
                  />
                  {commandMissing && (
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)', marginTop: 4 }}>
                      选了外部 ACP Agent，必须填写启动命令
                    </div>
                  )}
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
                  <div>
                    <label style={LABEL_INLINE}>参数（每行一个）</label>
                    <textarea
                      rows={3}
                      value={argsText}
                      onChange={(e) => setArgsText(e.target.value)}
                      placeholder={'acp\n--xxx'}
                      style={{ ...RUNTIME_FIELD, resize: 'vertical', lineHeight: 1.6 }}
                    />
                  </div>
                  <div>
                    <label style={LABEL_INLINE}>环境变量（每行一个）</label>
                    <textarea
                      rows={3}
                      value={envText}
                      onChange={(e) => setEnvText(e.target.value)}
                      placeholder={'KEY=VALUE'}
                      style={{ ...RUNTIME_FIELD, resize: 'vertical', lineHeight: 1.6 }}
                    />
                  </div>
                </div>
              </div>
            )}

            {state.tone !== 'none' && (
              <div style={{ marginTop: 8, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                状态：{state.label}
              </div>
            )}
            {err && (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)', marginTop: 6, wordBreak: 'break-all' }}>
                {err}
              </div>
            )}

            <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
              <button
                className="c-btn c-btn--ghost c-btn--sm"
                style={{ flex: 1 }}
                disabled={busy}
                onClick={() => setOpen(false)}
              >
                取消
              </button>
              <button
                className="c-btn c-btn--primary c-btn--sm"
                style={{ flex: 1 }}
                disabled={busy || commandMissing}
                onClick={() => void save()}
              >
                {busy ? '保存中…' : '应用'}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}

function RuntimeSeg({
  active,
  onClick,
  children,
}: {
  active: boolean
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      style={{
        flex: 1,
        padding: '6px 8px',
        borderRadius: 'var(--radius-sm)',
        border: `1px solid ${active ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
        background: active ? 'var(--accent-dim)' : 'var(--bg-surface)',
        color: active ? 'var(--accent-bright)' : 'var(--text-tertiary)',
        fontSize: 'var(--text-xs)',
        fontFamily: 'inherit',
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  )
}

const RUNTIME_FIELD: CSSProperties = {
  width: '100%',
  padding: '7px 10px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-sm)',
  fontFamily: 'inherit',
}

const LABEL_INLINE: CSSProperties = {
  display: 'block',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-secondary)',
  marginBottom: 6,
}

/**
 * ExtraDirs —— 工作区根：主目录之外声明的附加根。
 *
 * 单根时「改这个仓库、同时读隔壁那个仓库」只能报越界，
 * 用户得换会话或搬目录。参照 Grok Bot 的 workspacePaths 与
 * Claude Code 的 --add-dir，工作区是一组根：文件读写、跨根检索覆盖整组，
 * 命令默认在主根跑，也可切到某个已声明根内。
 */
function ExtraDirs({
  agent,
  onConfig,
}: {
  agent: Agent
  onConfig: (patch: { extraDirs?: string[] }) => void
}) {
  const list = agent.extraDirs ?? []
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState('')
  const empty = list.length === 0

  const add = () => {
    const dir = draft.trim()
    if (!dir) return
    if (list.includes(dir)) {
      setErr('这个目录已经在列表里')
      return
    }
    setErr('')
    onConfig({ extraDirs: [...list, dir] })
    setDraft('')
  }

  return (
    <div className="c-extra">
      <div className="c-extra__label">工作区根</div>
      <div style={hintStyle}>
        除主目录外可读写、可检索、可作为命令目录的文件夹。适合「改这个仓库、同时读隔壁那个」。
      </div>
      {empty && <div className="c-extra__empty">还没有其他工作区根，可以再加一个。</div>}
      {list.map((d) => (
        <div key={d} className="c-extra__row">
          <span className="mono c-extra__path" title={d}>
            {d}
          </span>
          <button
            type="button"
            className="c-icon-btn c-icon-btn--danger"
            aria-label={`移除工作区根 ${d}`}
            onClick={() => onConfig({ extraDirs: list.filter((x) => x !== d) })}
          >
            ×
          </button>
        </div>
      ))}
      <div className="c-extra__add">
        <DirectoryPicker value={draft} onChange={setDraft} compact placeholder="选择要加入的文件夹" />
        <button type="button" className="c-btn c-btn--ghost c-btn--sm" disabled={!draft.trim()} onClick={add}>
          再加一个工作区根
        </button>
      </div>
      {err && <div className="c-extra__err">{err}</div>}
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
  const timedOut = !!group.timedOut || (group.exitCode !== undefined && group.exitCode < 0)
  const ok = !timedOut && (group.exitCode === undefined || group.exitCode === 0)
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
        {timedOut && (
          <span style={{ marginLeft: 'auto', flexShrink: 0, color: 'var(--warning)' }}>超时</span>
        )}
        {!timedOut && group.exitCode !== undefined && (
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
        {previewMode()
          ? '这是预览。下面的会话是假的，不会真的跑起来。'
          : '这个会话是独立的，不影响其他 agent。点下面的例子直接开始：'}
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

function AgentFace({ name, color, note }: { name: string; color: string; note?: string }) {
  return (
    <div className="c-agent-head" style={{ '--tone': color } as CSSProperties}>
      <span className="c-agent-avatar" aria-hidden>
        {name.slice(0, 1).toUpperCase()}
      </span>
      <span className="c-agent-name">
        <i className="c-agent-dot" />
        {name}
      </span>
      {note && <span className="c-pill">{note}</span>}
    </div>
  )
}

function PeerCallList({ calls, agents }: { calls?: PeerCall[]; agents?: MentionTarget[] }) {
  if (!calls?.length) return null
  return (
    <div style={{ display: 'grid', gap: 8, marginTop: 10 }}>
      {calls.map((call, i) => {
        const hit = agents?.find((a) => a.name === call.name)
        const color = hit?.color || 'var(--text-tertiary)'
        const reply = call.reply.replace(/\s+/g, ' ').trim()
        const short = reply.length > 180 ? `${reply.slice(0, 180)}…` : reply
        return (
          <div key={`${call.name}-${i}`} className="c-peer-call">
            <AgentFace name={call.name} color={color} note={call.ok ? '回复' : '未完成'} />
            {short && <div className="c-peer-call__text">{short}</div>}
          </div>
        )
      })}
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
    a.m.blocks === b.m.blocks &&
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
    a.peers === b.peers &&
    a.speakerColor === b.speakerColor &&
    a.fromAgent?.id === b.fromAgent?.id &&
    (a.calls ?? []).map((c) => `${c.name}:${c.ok}:${c.reply}`).join('\n') ===
      (b.calls ?? []).map((c) => `${c.name}:${c.ok}:${c.reply}`).join('\n'),
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
  calls?: PeerCall[]
  fromAgent?: Caller
  speakerColor?: string
  onContextMenu?: (e: React.MouseEvent, msg: Message) => void
  onCopy?: (text: string) => void
  onRegenerate?: (msg: Message) => void
  onRoute?: (targetId: string, text: string) => void
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
  calls,
  fromAgent,
  speakerColor,
  onContextMenu,
  onCopy,
  onRegenerate,
  onRoute,
  onOpenKnowledge,
}: BubbleProps) {
  if (m.role === 'system' && (m.content || '').startsWith('运行错误：')) {
    return (
      <div
        style={{
          marginTop: 'var(--space-3)',
          marginBottom: 'var(--space-4)',
          background: 'var(--danger-dim)',
          border: '1px solid var(--danger-border)',
          borderRadius: 'var(--radius-md)',
          padding: '9px 12px',
          fontSize: 'var(--text-sm)',
          color: 'var(--danger)',
          lineHeight: 'var(--leading-normal)',
        }}
      >
        {m.content.slice('运行错误：'.length)}
      </div>
    )
  }

  const isUser = m.role === 'user'
  const parsed = parseMessage(m.content)
  const isRouted = parsed.isRouted
  const routeInfo = parsed.from ? `来自 ${parsed.from}` : parsed.to ? `发给 ${parsed.to}` : ''
  const displayContent = parsed.body
  const artifacts = isUser ? [] : extractArtifacts(displayContent)
  const toneStyle = { '--tone': speakerColor || `var(--agent-${tone})` } as CSSProperties
  const clock = m.timestamp
    ? new Date(m.timestamp * 1000).toLocaleTimeString('zh-CN', {
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
        hour12: false,
      })
    : ''

  if (isUser && fromAgent) {
    return (
      <div
        data-msg-row="agent"
        className="c-msg c-msg--agent fade-up"
        style={{ marginBottom: 'var(--space-4)' }}
        onContextMenu={onContextMenu ? (e) => onContextMenu(e, m) : undefined}
      >
        <AgentFace name={fromAgent.name} color={fromAgent.color} note="发来的任务" />
        <div data-msg-body className="c-msg-card" style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, whiteSpace: 'pre-wrap', wordBreak: 'break-word', color: 'var(--text-secondary)' }}>
          {displayContent}
        </div>
      </div>
    )
  }

  if (isUser) {
    return (
      <div
        data-msg-row="user"
        className="c-msg c-msg--user fade-up"
        style={{ marginBottom: 'var(--space-4)' }}
        onContextMenu={onContextMenu ? (e) => onContextMenu(e, m) : undefined}
      >
        <div className="c-user-bubble" data-msg-body>
          {(m.blocks?.length ?? 0) > 0 ? (
            <MessageBody blocks={m.blocks} text={displayContent} mentionTargets={agents} selfColor={`var(--agent-${tone})`} variant="user" />
          ) : (
            <div style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
              <MessageBody blocks={m.blocks} text={displayContent} mentionTargets={agents} selfColor={`var(--agent-${tone})`} variant="user" />
            </div>
          )}
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
      data-msg-row="agent"
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
          <CopyButton text={displayContent} variant="icon" onCopy={() => onCopy?.(displayContent)} />
          {onRegenerate && (
            <button type="button" className="c-icon-btn" title="重新生成" onClick={() => onRegenerate(m)}>
              重新生成
            </button>
          )}
          {onRoute && peers && peers.length > 0 && (
            <ForwardMenu
              label="Route"
              title="Route 派发给其他 Agent"
              items={peers.map((peer) => ({ id: peer.id, label: peer.name }))}
              triggerClassName="c-icon-btn"
              wrapperClassName="c-route"
              menuClassName="c-route__menu"
              itemClassName="c-ctx-item"
              onPick={(id) => onRoute(id, displayContent.slice(0, 200))}
            />
          )}
        </div>
      </div>
      <div data-msg-body className={live ? 'markdown-body c-msg-card c-msg-card--live' : 'markdown-body c-msg-card'} style={{ fontSize: 'var(--text-md)', lineHeight: 1.7, wordBreak: 'break-word', color: 'var(--text-secondary)' }}>
        {m.reasoning && <ReasoningCard text={m.reasoning} live={live} />}
        <StreamingMarkdown text={displayContent} live={!!live} />
        <PeerCallList calls={calls} agents={agents} />
      </div>
      {artifacts.length > 0 && (
        <div className="c-artifacts">
          {artifacts.map((a) =>
            a.kind === 'note' ? (
              <button key={a.label} type="button" className="c-artifact" onClick={() => onOpenKnowledge?.()}>
                {a.label}
              </button>
            ) : (
              <span key={a.path} className="c-artifact">
                {a.label}
              </span>
            ),
          )}
        </div>
      )}
    </div>
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
  model,
  contextWindow,
}: {
  messages: { content?: string }[]
  streaming?: string
  usage?: { in: number; out: number; lastIn?: number }
  provider: string
  model?: string
  contextWindow?: number
}) {
  const base = useMemo(
    () => contextMeter({ messages, streaming: '', usage, provider, model, contextWindow }),
    [messages, usage, provider, model, contextWindow],
  )
  const meter = useMemo(
    () => (base.estimated && streaming ? { ...base, used: base.used + estimateTokens(streaming) } : base),
    [base, streaming],
  )
  const ratio = meter.limit > 0 ? meter.used / meter.limit : 0
  const level = ratio >= 1 ? 'critical' : ratio >= 0.85 ? 'warning' : ratio >= 0.7 ? 'watch' : 'normal'
  const percent = Math.max(0, Math.round(ratio * 100))
  const value = `上下文 ${meter.estimated ? '约 ' : ''}${formatTokens(meter.used)} / ${formatTokens(meter.limit)}`
  return (
    <div
      className={`c-context-meter c-context-meter--${level}`}
      data-level={level}
      role="meter"
      aria-label="上下文占用"
      aria-valuemin={0}
      aria-valuemax={meter.limit}
      aria-valuenow={Math.min(meter.used, meter.limit)}
      aria-valuetext={`${value}，${percent}%`}
      title={
        meter.estimated
          ? '模型还没返回用量，这是按文本粗估的上下文。消耗会在接口带回 token 后出现。'
          : '上下文是最近一次请求装进窗口的量。消耗是累计输入加输出。85% 起提示接近窗口上限。'
      }
    >
      <span className="c-context-meter__value">{value}</span>
      {meter.spent > 0 && <span className="c-context-meter__spent">消耗 {formatTokens(meter.spent)}</span>}
      <span className="c-context-meter__track" aria-hidden>
        <span className="c-context-meter__fill" style={{ transform: `scaleX(${Math.min(1, Math.max(0, ratio))})` }} />
      </span>
      <span className="c-context-meter__percent mono">{percent}%</span>
    </div>
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

/**
 * ChannelStrip —— 单聊里的频道标识。
 * 群聊回合不写入会话历史（loop_backend 在 channelID 非空时跳过回写），
 * 单聊里看不到群工作。这里标出他正在/曾在哪些群干活，并给跳转入口。
 */
function ChannelStrip({
  agent,
  channels,
  onOpenChannel,
}: {
  agent: Agent
  channels: Channel[]
  onOpenChannel?: (id: string) => void
}) {
  if (agent.busy) {
    const busyCh = channels.find((c) => c.name === agent.busy)
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          padding: '7px var(--space-6)',
          background: 'var(--accent-dim)',
          borderBottom: '1px solid var(--border-subtle)',
          fontSize: 'var(--text-sm)',
          color: 'var(--accent-bright)',
          flexShrink: 0,
        }}
      >
        <span className="blink">●</span>
        <span>他正在 #{agent.busy} 里回答，对话内容记录在频道里</span>
        {busyCh && onOpenChannel && (
          <button
            className="c-btn c-btn--sm"
            style={{ marginLeft: 'auto' }}
            onClick={() => onOpenChannel(busyCh.id)}
          >
            去频道看 →
          </button>
        )}
      </div>
    )
  }

  const joined = channels
    .filter((c) => (c.members ?? []).includes(agent.id))
    .map((c) => ({
      ...c,
      mine: (c.messages ?? []).filter((m) => m.fromId === agent.id).length,
    }))
  if (joined.length === 0) return null

  return (
    <div
      style={{
        display: 'flex',
        alignItems: 'center',
        flexWrap: 'wrap',
        gap: 6,
        padding: '5px var(--space-6)',
        borderBottom: '1px solid var(--border-subtle)',
        fontSize: 'var(--text-xs)',
        color: 'var(--text-tertiary)',
        flexShrink: 0,
      }}
    >
      <span>频道动态（群聊内容记录在频道，不进下面的单聊）：</span>
      {joined.map((c) => (
        <button
          key={c.id}
          className="c-btn c-btn--sm"
          title={`他在 #${c.name} 有 ${c.mine} 条发言，点击去频道`}
          onClick={() => onOpenChannel?.(c.id)}
        >
          # {c.name} · {c.mine} 条
        </button>
      ))}
    </div>
  )
}
