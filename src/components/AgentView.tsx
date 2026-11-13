import { useEffect, useRef, useState } from 'react'
import ReactMarkdown from 'react-markdown'
import TextareaAutosize from 'react-textarea-autosize'
import { ContextMenu } from 'radix-ui'
import MentionPicker from './MentionPicker'
import remarkGfm from 'remark-gfm'
import { Sidecar, type AgentView as Agent, type Config, type Message } from '../mygo'
import { PROVIDERS, DEFAULT_BASE, DEFAULT_MODEL } from '../lib/presets'

type Props = {
  agent: Agent | null
  streaming: string
  error?: string
  workDir: string
  tokenUsage?: { in: number; out: number }
  onPrompt: (text: string) => void
  onStop: () => void
  onShell: (command: string) => void
  onToggleInspector: () => void
  inspectorOpen: boolean
  /** 单聊模式：禁用 @ 路由功能 */
  disableRoute?: boolean
  onRoute?: (targetName: string, text: string) => void
  agents?: Agent[]
  /** 会话级配置变更（工作目录 / 模型），只影响当前会话 */
  onConfig: (patch: { workDir?: string; llm?: Config }) => void
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

  const [showMentions, setShowMentions] = useState(false)
  const [mentionQuery, setMentionQuery] = useState('')
  const [selectedMentions, setSelectedMentions] = useState<string[]>([])

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
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
            创建多个 Agent，让它们并行工作、互相协作
          </div>
          <div style={{ display: 'flex', gap: 16, fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
            <span>💬 流式对话</span>
            <span>🔧 工具调用</span>
            <span>✉️ Agent 协作</span>
          </div>
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

  const agentsForPicker = (p.agents ?? []).map((a) => ({
    id: a.id,
    name: a.name,
    color: a.color,
    role: a.role,
  }))

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
          <SessionConfigChips
            agent={p.agent}
            fallbackWorkDir={p.workDir}
            onConfig={p.onConfig}
          />
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
          <Bubble
            key={i}
            m={m}
            color={p.agent!.color}
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

      {/* 右键菜单：radix ContextMenu 自己管理定位（贴着触发元素，自动边缘翻转）。
          不要用受控 open + 手写 left/top —— 会和 radix 的定位系统冲突。 */}
      {contextMenu && (
        <ContextMenu.Root
          open
          onOpenChange={(v) => {
            if (!v) closeContextMenu()
          }}
        >
          <ContextMenu.Trigger
            style={{
              position: 'fixed',
              left: contextMenu.x,
              top: contextMenu.y,
              width: 1,
              height: 1,
              pointerEvents: 'none',
            }}
          />
          <ContextMenu.Portal>
            <ContextMenu.Content
              className="fade-up"
              style={{
                minWidth: 180,
                background: 'var(--bg-elevated)',
                border: '1px solid var(--border-default)',
                borderRadius: 'var(--radius-md)',
                boxShadow: 'var(--shadow-lg)',
                padding: 4,
                zIndex: 1000,
              }}
            >
              <MenuItem onSelect={() => copyMessage(contextMenu.msg)}>📋 复制</MenuItem>
              {contextMenu.msg.role === 'assistant' && (
                <MenuItem onSelect={() => regenerateMessage(contextMenu.msg)}>
                  🔄 重新生成
                </MenuItem>
              )}
              {p.agents && p.agents.length > 1 && (
                <>
                  <ContextMenu.Separator
                    style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }}
                  />
                  <ContextMenu.Label
                    style={{
                      padding: '4px 10px',
                      fontSize: 'var(--text-xs)',
                      color: 'var(--text-disabled)',
                    }}
                  >
                    引用给…
                  </ContextMenu.Label>
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
            </ContextMenu.Content>
          </ContextMenu.Portal>
        </ContextMenu.Root>
      )}
    </div>
  )
}

function SessionConfigChips({
  agent,
  fallbackWorkDir,
  onConfig,
}: {
  agent: Agent
  fallbackWorkDir: string
  onConfig: (patch: { workDir?: string; llm?: Config }) => void
}) {
  const [open, setOpen] = useState<'none' | 'dir' | 'model'>('none')
  const [dirDraft, setDirDraft] = useState('')
  const [cfgDraft, setCfgDraft] = useState<Config>(
    agent.llm ?? { provider: 'mock', baseUrl: '', apiKey: '', model: '' },
  )
  const [models, setModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [modelHint, setModelHint] = useState('')

  const effectiveDir = agent.workDir || fallbackWorkDir
  const effectiveProvider = agent.llm?.provider || 'mock'
  const effectiveModel = agent.llm?.model || ''

  // 切换会话时重置草稿，避免把上个会话的配置带过去
  useEffect(() => {
    setOpen('none')
    setCfgDraft(agent.llm ?? { provider: 'mock', baseUrl: '', apiKey: '', model: '' })
    setModels([])
    setModelHint('')
  }, [agent.id, agent.llm])

  const loadModels = async () => {
    setLoadingModels(true)
    setModelHint('')
    try {
      const list = await Sidecar.listModelsFor(agent.id, cfgDraft)
      setModels(list)
      if (list.length === 0) setModelHint('未获取到模型列表')
    } catch (e) {
      // 内联提示，不弹 toast：ListModels 失败是常见的未配置态
      setModelHint(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoadingModels(false)
    }
  }

  return (
    <div style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: 4 }}>
      {/* 工作目录 chip */}
      <button
        className="mono"
        onClick={() => {
          setOpen(open === 'dir' ? 'none' : 'dir')
          setDirDraft(effectiveDir)
        }}
        title={`工作目录：${effectiveDir || '未设置'}`}
        style={chipStyle(open === 'dir')}
      >
        📁 {shortPath(effectiveDir)}
      </button>

      {/* 模型 chip */}
      <button
        className="mono"
        onClick={() => setOpen(open === 'model' ? 'none' : 'model')}
        title={`模型：${effectiveProvider}${effectiveModel ? ' / ' + effectiveModel : ''}`}
        style={chipStyle(open === 'model')}
      >
        ✦ {effectiveModel || effectiveProvider}
      </button>

      {open !== 'none' && (
        <>
          <div
            style={{ position: 'fixed', inset: 0, zIndex: 900 }}
            onClick={() => setOpen('none')}
          />
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
            {open === 'dir' ? (
              <>
                <div style={popoverLabel}>本会话工作目录</div>
                <div style={{ display: 'flex', gap: 6 }}>
                  <input
                    value={dirDraft}
                    onChange={(e) => setDirDraft(e.target.value)}
                    placeholder="/path/to/project"
                    className="mono"
                    style={inputStyle}
                  />
                  <button
                    className="c-btn c-btn--secondary c-btn--sm"
                    onClick={async () => {
                      const picked = await Sidecar.pickDirectory()
                      if (picked) setDirDraft(picked)
                    }}
                  >
                    浏览…
                  </button>
                </div>
                <div style={hintStyle}>只影响当前会话的 CLI 与 shell，不改其他 agent</div>
                <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
                  <button
                    className="c-btn c-btn--primary c-btn--sm"
                    style={{ flex: 1 }}
                    onClick={() => {
                      onConfig({ workDir: dirDraft })
                      setOpen('none')
                    }}
                  >
                    应用
                  </button>
                  <button
                    className="c-btn c-btn--ghost c-btn--sm"
                    onClick={() => {
                      onConfig({ workDir: '' })
                      setOpen('none')
                    }}
                    title="回落到全局默认目录"
                  >
                    跟随全局
                  </button>
                </div>
              </>
            ) : (
              <>
                <div style={popoverLabel}>本会话模型</div>
                <div style={{ display: 'grid', gap: 3, marginBottom: 8 }}>
                  {PROVIDERS.map((x) => {
                    const on = cfgDraft.provider === x.id
                    return (
                      <button
                        key={x.id}
                        onClick={() => {
                          setCfgDraft({
                            provider: x.id,
                            baseUrl: DEFAULT_BASE[x.id] ?? '',
                            apiKey: x.id === cfgDraft.provider ? cfgDraft.apiKey : '',
                            model: DEFAULT_MODEL[x.id] ?? '',
                          })
                          setModels([])
                          setModelHint('')
                        }}
                        style={{
                          textAlign: 'left',
                          padding: '6px 9px',
                          borderRadius: 'var(--radius-sm)',
                          border: `1px solid ${on ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
                          background: on ? 'var(--accent-dim)' : 'var(--bg-surface)',
                          color: on ? 'var(--accent-bright)' : 'var(--text-secondary)',
                          fontSize: 'var(--text-xs)',
                        }}
                      >
                        {x.label}
                      </button>
                    )
                  })}
                </div>

                {cfgDraft.provider !== 'mock' && (
                  <>
                    <input
                      value={cfgDraft.baseUrl}
                      onChange={(e) => setCfgDraft({ ...cfgDraft, baseUrl: e.target.value })}
                      placeholder={DEFAULT_BASE[cfgDraft.provider] ?? 'Base URL'}
                      className="mono"
                      style={{ ...inputStyle, marginBottom: 6 }}
                    />
                    <input
                      type="password"
                      value={cfgDraft.apiKey}
                      onChange={(e) => setCfgDraft({ ...cfgDraft, apiKey: e.target.value })}
                      placeholder="API Key（可留空）"
                      className="mono"
                      style={{ ...inputStyle, marginBottom: 6 }}
                    />
                    <div style={{ display: 'flex', gap: 6 }}>
                      <input
                        value={cfgDraft.model}
                        onChange={(e) => setCfgDraft({ ...cfgDraft, model: e.target.value })}
                        placeholder={DEFAULT_MODEL[cfgDraft.provider] ?? '模型名'}
                        className="mono"
                        style={inputStyle}
                      />
                      <button
                        className="c-btn c-btn--secondary c-btn--sm"
                        onClick={loadModels}
                        disabled={loadingModels}
                      >
                        {loadingModels ? '…' : '列表'}
                      </button>
                    </div>
                    {models.length > 0 && (
                      <select
                        value={cfgDraft.model}
                        onChange={(e) => setCfgDraft({ ...cfgDraft, model: e.target.value })}
                        style={{ ...inputStyle, marginTop: 6 }}
                      >
                        <option value="">选择模型…</option>
                        {models.map((m) => (
                          <option key={m} value={m}>
                            {m}
                          </option>
                        ))}
                      </select>
                    )}
                    {modelHint && <div style={hintStyle}>{modelHint}</div>}
                  </>
                )}

                <div style={{ display: 'flex', gap: 6, marginTop: 10 }}>
                  <button
                    className="c-btn c-btn--primary c-btn--sm"
                    style={{ flex: 1 }}
                    onClick={() => {
                      onConfig({ llm: cfgDraft })
                      setOpen('none')
                    }}
                  >
                    应用
                  </button>
                  <button
                    className="c-btn c-btn--ghost c-btn--sm"
                    onClick={() => {
                      onConfig({ llm: { provider: 'mock', baseUrl: '', apiKey: '', model: '' } })
                      setOpen('none')
                    }}
                    title="回落到全局默认模型"
                  >
                    跟随全局
                  </button>
                </div>
              </>
            )}
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

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 9px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-xs)',
  minWidth: 0,
}

const hintStyle: React.CSSProperties = {
  fontSize: 'var(--text-xs)',
  color: 'var(--text-disabled)',
  marginTop: 4,
  lineHeight: 'var(--leading-normal)',
}

function shortPath(dir: string): string {
  if (!dir) return '未设置'
  const parts = dir.split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : dir
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
    blocked: { cls: 'c-badge--danger', t: '等你处理' },
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

function Bubble({ m, color, live, tokens, onContextMenu }: { m: Message; color: string; live?: boolean; tokens?: { in: number; out: number }; onContextMenu?: (e: React.MouseEvent, msg: Message) => void }) {
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
            wordBreak: 'break-word',
            color: isUser ? 'var(--text-primary)' : isRouted ? 'var(--text-secondary)' : 'var(--text-secondary)',
            fontStyle: isRouted ? 'italic' : 'normal',
          }}
          className="markdown-body"
        >
          <ReactMarkdown remarkPlugins={[remarkGfm]}>
            {displayContent}
          </ReactMarkdown>
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
function MenuItem({ onSelect, children }: { onSelect: () => void; children: React.ReactNode }) {
  return (
    <ContextMenu.Item
      onSelect={onSelect}
      style={{
        display: 'flex',
        alignItems: 'center',
        padding: '8px 10px',
        fontSize: 'var(--text-sm)',
        color: 'var(--text-primary)',
        borderRadius: 'var(--radius-sm)',
        cursor: 'pointer',
        outline: 'none',
        userSelect: 'none',
      }}
      onMouseEnter={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
      onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
      onFocus={(e) => (e.currentTarget.style.background = 'var(--bg-surface-hover)')}
      onBlur={(e) => (e.currentTarget.style.background = 'transparent')}
    >
      {children}
    </ContextMenu.Item>
  )
}
