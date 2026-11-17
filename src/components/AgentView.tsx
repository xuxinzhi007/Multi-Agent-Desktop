import { memo, useEffect, useMemo, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import TextareaAutosize from 'react-textarea-autosize'
import ModelPicker from './ModelPicker'
import { Popover } from 'radix-ui'
import MentionPicker from './MentionPicker'
import MentionText, { type MentionTarget } from './MentionText'
import remarkGfm from 'remark-gfm'
import { type AgentView as Agent, type Config, type Message } from '../mygo'
import type { ShellLog } from '../lib/useBackend'
import { parseMessage } from '../lib/message'
import DirectoryPicker from './DirectoryPicker'

type Props = {
  agent: Agent | null
  streaming: string
  error?: string
  workDir: string
  tokenUsage?: { in: number; out: number }
  onPrompt: (text: string) => void
  onStop: () => void
  onShell: (command: string) => void
  /** 本会话执行过的命令，直接渲染在对话流里 */
  shellLogs?: ShellLog[]
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
  /** 全局配置的 provider，会话级未设置时用它 */
  globalProvider?: string
  /** 全局配置的模型，会话级未设置时显示它 */
  globalModel?: string
  /** 清掉会话级模型设置，回到跟随全局 */
  onClearLLM?: () => void
}

// 空状态的一键角色。只保留 3 个 —— 选择过多反而增加决策成本。
const quickRoles = [
  { emoji: '🧩', label: '前端', name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理与渲染性能' },
  { emoji: '⚙️', label: '后端', name: '后端', role: 'Go 专家，关注 API 设计、并发安全与查询效率' },
  { emoji: '🔍', label: '审查', name: 'Reviewer', role: '代码审查者，指出风险、代码异味与不符合规范的地方' },
] as const

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

        {visible.map((m, i) => (
          <Bubble
            key={i}
            m={m}
            color={p.agent!.color}
            agents={agentsForPicker}
            tokens={i === visible.length - 1 && m.role === 'assistant' ? p.tokenUsage : undefined}
            onContextMenu={handleContextMenu}
          />
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

        {(p.shellLogs ?? []).map((log) => (
          <ShellEntry key={log.id} log={log} />
        ))}

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
                current={p.agent.llm?.model || p.globalModel || ''}
                onPick={(m) =>
                  p.onConfig({
                    llm: {
                      provider: m.provider,
                      baseUrl: m.baseUrl ?? '',
                      // 同一 provider 内换模型要保留原 Key，
                      // 否则用户切个模型就被要求重新填一遍凭据
                      apiKey: p.agent!.llm?.provider === m.provider ? (p.agent!.llm?.apiKey ?? '') : '',
                      model: m.model,
                    },
                  })
                }
              />
              <div className="composer-bar__dir" style={{ minWidth: 0, flexShrink: 1 }}>
                <WorkDirChip agent={p.agent} fallbackWorkDir={p.workDir} onConfig={p.onConfig} />
              </div>
            </div>

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
  fallbackWorkDir,
  onConfig,
}: {
  agent: Agent
  fallbackWorkDir: string
  onConfig: (patch: { workDir?: string; llm?: Config }) => void
}) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState('')
  const effective = agent.workDir || fallbackWorkDir

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
            <div style={popoverLabel}>本会话工作目录</div>
            <DirectoryPicker value={draft} onChange={setDraft} />
            <div style={hintStyle}>只影响当前会话的 CLI 与 shell，不改其他 agent</div>
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
              <button
                className="c-btn c-btn--ghost c-btn--sm"
                onClick={() => {
                  onConfig({ workDir: '' })
                  setOpen(false)
                }}
                title="回落到全局默认目录"
              >
                跟随全局
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

/**
 * ShellEntry 是一条命令执行记录，直接内联在对话流里。
 *
 * 之前命令结果放在一个独立侧栏面板里，用户敲完命令后对话区没反应，
 * 得去别处找输出。现在「我敲了什么 → 输出了什么」在同一个流里。
 */
function ShellEntry({ log }: { log: ShellLog }) {
  const r = log.result
  const ok = r ? r.exitCode === 0 : true
  const output = r ? [r.stdout, r.stderr].filter(Boolean).join('\n').trimEnd() : ''

  return (
    <div style={{ marginBottom: 'var(--space-3)' }}>
      <div
        className="mono"
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          fontSize: 'var(--text-xs)',
          color: 'var(--text-tertiary)',
          marginBottom: 3,
        }}
      >
        <span style={{ color: 'var(--text-disabled)' }}>$</span>
        <span style={{ color: 'var(--text-secondary)' }}>{log.command}</span>
        {!r && (
          <span className="blink" style={{ color: 'var(--warning)' }}>
            执行中…
          </span>
        )}
        {r && (
          <span style={{ marginLeft: 'auto', color: ok ? 'var(--success)' : 'var(--danger)' }}>
            exit {r.exitCode}
          </span>
        )}
      </div>
      {r && (
        <pre
          className="mono c-shell-out"
          style={{
            margin: 0,
            padding: '8px 10px',
            borderRadius: 'var(--radius-md)',
            border: '1px solid var(--border-subtle)',
            background: 'var(--bg-inset)',
            fontSize: 'var(--text-xs)',
            lineHeight: 1.6,
            color: ok ? 'var(--text-secondary)' : 'var(--danger)',
            maxHeight: 320,
            overflow: 'auto',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
          }}
        >
          {output || '（无输出）'}
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
    a.m.timestamp === b.m.timestamp &&
    a.color === b.color &&
    a.live === b.live &&
    a.tokens?.in === b.tokens?.in &&
    a.tokens?.out === b.tokens?.out &&
    a.agents === b.agents,
)


function BubbleImpl({
  m,
  color,
  live,
  tokens,
  agents,
  onContextMenu,
}: {
  m: Message
  color: string
  live?: boolean
  tokens?: { in: number; out: number }
  agents?: MentionTarget[]
  onContextMenu?: (e: React.MouseEvent, msg: Message) => void
}) {
  const isUser = m.role === 'user'

  // 检测路由消息：[from xxx] 或 [to xxx]
  const parsed = parseMessage(m.content)
  const isRouted = parsed.isRouted
  const routeInfo = parsed.from ? `来自 ${parsed.from}` : parsed.to ? `发给 ${parsed.to}` : ''
  const displayContent = parsed.body

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
            wordBreak: 'break-word',
            color: isUser ? 'var(--text-primary)' : 'var(--text-secondary)',
            fontStyle: isRouted ? 'italic' : 'normal',
            whiteSpace: isUser ? 'pre-wrap' : undefined,
          }}
          className={isUser ? undefined : 'markdown-body'}
        >
          {isUser ? (
            <MentionText text={displayContent} agents={agents} selfColor={color} />
          ) : (
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{displayContent}</ReactMarkdown>
          )}
        </div>
        {tokens && (tokens.in > 0 || tokens.out > 0) && (
          <div
            style={{
              marginTop: 4,
              fontSize: 'var(--text-xs)',
              color: 'var(--text-disabled)',
              display: 'flex',
              gap: 8,
            }}
          >
            <span>↑ {formatTokens(tokens.in)}</span>
            <span>↓ {formatTokens(tokens.out)}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`
  return `${n}`
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
