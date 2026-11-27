import { useEffect, useState } from 'react'
import { API, type AgentView as Agent, type Channel, type Room } from '../mygo'
import { agentTree, linkParents, type AgentNode } from '../lib/agent-tree'
import { previewMode } from '../lib/preview-runtime'
import type { MainView } from '../lib/nav'
import CreateAgentModal from './CreateAgentModal'
import SupervisorModal from './SupervisorModal'
import RuntimeBadge from './RuntimeBadge'

type Props = {
  agents: Agent[]
  channels: Channel[]
  feishuRooms: Room[]
  /** 当前选中：agent id / channel id / null */
  activeId: string | null
  activeKind: 'agent' | 'channel' | 'feishu' | null
  connected: boolean
  version: string
  provider: string
  onSelect: (kind: 'agent' | 'channel' | 'feishu', id: string) => void
  onCreate: (name: string, role: string, backend: string, workDir?: string) => void
  /** 自动拆任务：输入目标后由 Supervisor 建子会话并行执行 */
  onCreateSupervisor: (req: { name: string; role: string; goal: string; workDir: string }) => Promise<void>
  onDelete: (id: string) => void
  onCreateChannel: (name: string, members: string[]) => void
  onOpenSettings: () => void
  onBindFeishu?: (chatId: string, agentId: string) => void
  /** 新建 Agent 弹窗开关由上层持有，空状态与侧栏共用同一个入口 */
  createOpen: boolean
  onCreateOpenChange: (open: boolean) => void
  /** 主区域当前视图：舰队 / 对话 / 网关 / 记忆 / 计划 */
  view: MainView
  onViewChange: (v: MainView) => void
}

function ViewTab({
  active,
  onClick,
  title,
  children,
}: {
  active: boolean
  onClick: () => void
  title: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      style={{
        flex: 1,
        padding: '4px 6px',
        border: 'none',
        borderRadius: 'var(--radius-sm)',
        background: active ? 'var(--bg-surface)' : 'transparent',
        color: active ? 'var(--text-primary)' : 'var(--text-tertiary)',
        fontFamily: 'inherit',
        fontSize: 'var(--text-xs)',
        fontWeight: active ? 600 : 400,
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  )
}

function AgentLine({
  agent: a,
  active,
  nested,
  childCount,
  folded,
  onToggle,
  onSelect,
  onDelete,
}: {
  agent: Agent
  active: boolean
  nested: boolean
  childCount: number
  folded: boolean
  onToggle?: () => void
  onSelect: () => void
  onDelete: () => void
}) {
  return (
    <div
      role="button"
      tabIndex={0}
      aria-current={active ? 'true' : undefined}
      aria-label={`会话 ${a.name}`}
      onClick={onSelect}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onSelect()
        }
      }}
      style={{
        position: 'relative',
        display: 'flex',
        alignItems: 'center',
        gap: 'var(--space-2)',
        padding: nested ? '6px 8px 6px 8px' : '8px 10px 8px 11px',
        borderRadius: 'var(--radius-md)',
        cursor: 'pointer',
        background: active ? 'var(--bg-surface-active)' : 'transparent',
        transition: 'background var(--dur-fast) var(--ease)',
      }}
      onMouseEnter={(e) => {
        if (!active) e.currentTarget.style.background = 'var(--bg-surface)'
      }}
      onMouseLeave={(e) => {
        if (!active) e.currentTarget.style.background = 'transparent'
      }}
    >
      {active && (
        <span
          style={{
            position: 'absolute',
            left: 0,
            top: '50%',
            transform: 'translateY(-50%)',
            width: 2,
            height: 16,
            borderRadius: 'var(--radius-full)',
            background: a.color,
          }}
        />
      )}
      {childCount > 0 ? (
        <button
          type="button"
          className="c-icon-btn"
          aria-label={folded ? '展开子会话' : '收起子会话'}
          title={folded ? '展开子会话' : '收起子会话'}
          onClick={(e) => {
            e.stopPropagation()
            onToggle?.()
          }}
          style={{ width: 16, height: 16, fontSize: 'var(--text-xs)', flexShrink: 0 }}
        >
          {folded ? '▸' : '▾'}
        </button>
      ) : (
        nested && <span style={{ width: 8, flexShrink: 0 }} />
      )}
      <span
        className="agent-avatar"
        style={{
          width: nested ? 16 : 19,
          height: nested ? 16 : 19,
          borderRadius: 'var(--radius-sm)',
          background: a.color,
          color: 'var(--bg-base)',
          display: 'grid',
          placeItems: 'center',
          fontSize: nested ? 9 : 10,
          fontWeight: 700,
          flexShrink: 0,
          opacity: a.status === 'idle' ? 0.55 : 1,
          // @ts-ignore CSS 变量
          '--avatar-color': a.color,
        }}
      >
        {a.name.slice(0, 1).toUpperCase()}
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 5,
            fontSize: 'var(--text-base)',
            fontWeight: active ? 600 : 500,
            color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
          }}
        >
          <span
            style={{
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {a.name}
          </span>
          {childCount > 0 && (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', flexShrink: 0 }}>
              {childCount}
            </span>
          )}
          {a.worktree && (
            <span
              title={`独立工作树：${a.worktreeBranch || ''}`}
              style={{ fontSize: 'var(--text-xs)', color: 'var(--accent-bright)', flexShrink: 0 }}
            >
              ⎇
            </span>
          )}
          <RuntimeBadge agent={a} />
          <span
            className={`c-dot c-dot--${a.status} ${a.status === 'running' || a.status === 'queued' || a.status === 'blocked' ? 'blink' : ''}`}
            style={{ width: 5, height: 5 }}
          />
        </div>
        <div
          style={{
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
          }}
        >
          {a.busy
            ? `正在${a.busy}里回答`
            : a.status === 'blocked'
              ? `⚠ ${a.blockedReason || '等你处理'}`
              : a.status === 'running'
                ? '正在处理…'
                : a.status === 'queued'
                  ? `排队中 (${a.queueLen})`
                  : sessionLine(a)}
        </div>
      </div>
      <button
        className="c-icon-btn c-icon-btn--danger c-row-delete"
        onClick={(e) => {
          e.stopPropagation()
          if (confirm(`删除 agent「${a.name}」？\n\n历史消息将一并清除，不可恢复。`)) onDelete()
        }}
        title="删除"
        aria-label={`删除会话 ${a.name}`}
        style={{ width: 20, height: 20, fontSize: 'var(--text-md)' }}
      >
        ×
      </button>
    </div>
  )
}

function AgentBranch({
  node,
  depth,
  activeId,
  folded,
  onToggle,
  onSelect,
  onDelete,
}: {
  node: AgentNode<Agent>
  depth: number
  activeId: string | null
  folded: Record<string, boolean>
  onToggle: (id: string) => void
  onSelect: (id: string) => void
  onDelete: (id: string) => void
}) {
  const a = node.agent
  const kids = node.children
  const shut = kids.length > 0 && !!folded[a.id]
  const group = kids.length > 0 && depth === 0
  return (
    <div
      style={{
        marginBottom: group ? 6 : 1,
        borderRadius: 'var(--radius-md)',
        background: group ? 'var(--bg-surface)' : 'transparent',
        padding: group ? 2 : 0,
      }}
    >
      <AgentLine
        agent={a}
        active={a.id === activeId}
        nested={depth > 0}
        childCount={kids.length}
        folded={shut}
        onToggle={() => onToggle(a.id)}
        onSelect={() => onSelect(a.id)}
        onDelete={() => onDelete(a.id)}
      />
      {kids.length > 0 && !shut && (
        <div
          style={{
            margin: '0 4px 2px 18px',
            paddingLeft: 8,
            borderLeft: `2px solid ${a.color}`,
          }}
        >
          {kids.map((child) => (
            <AgentBranch
              key={child.agent.id}
              node={child}
              depth={depth + 1}
              activeId={activeId}
              folded={folded}
              onToggle={onToggle}
              onSelect={onSelect}
              onDelete={onDelete}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function sessionLine(a: Agent): string {
  const model = a.llm?.provider === 'mock' ? '' : a.llm?.model || ''
  const folder = a.workDir ? a.workDir.split(/[/\\]/).filter(Boolean).slice(-1)[0] : ''
  if (!model && !folder) return '未选模型和文件夹'
  if (!model) return `未选模型 · ${folder}`
  if (!folder) return `${model} · 未选文件夹`
  return `${model} · ${folder}`
}

/** 分组折叠状态记在本机：下次打开还是上次收起的样子。 */
const GROUP_KEY = 'mad.sidebar.groups'

function readGroups(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(GROUP_KEY) || '{}') as Record<string, boolean>
  } catch {
    return {}
  }
}

/** GroupHead 是可折叠的分组标题：名字 + 条数 + 折叠箭头。 */
function GroupHead({
  label,
  count,
  folded,
  onToggle,
  action,
}: {
  label: string
  count: number
  folded: boolean
  onToggle: () => void
  action?: React.ReactNode
}) {
  return (
    <div className="c-group-head">
      <button
        type="button"
        className="c-group-head__toggle"
        onClick={onToggle}
        aria-expanded={!folded}
        aria-label={`${folded ? '展开' : '收起'}${label}`}
      >
        <span className={`c-group-head__caret ${folded ? 'is-folded' : ''}`} aria-hidden>
          ▾
        </span>
        {label}
        <span className="c-group-head__count">{count}</span>
      </button>
      {action}
    </div>
  )
}

/** 分组标题（工作区 / 助手 / 设置），不可折叠，只做视觉分区。 */
function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="c-section-label" style={{ padding: '10px 4px 4px' }}>
      {children}
    </div>
  )
}

/** 助手 / 设置分组下的导航行：点击切换主区域页面。 */
function NavRow({
  icon,
  label,
  active,
  onClick,
  badge,
}: {
  icon: string
  label: string
  active?: boolean
  onClick: () => void
  badge?: React.ReactNode
}) {
  return (
    <button
      type="button"
      className={`c-nav-row ${active ? 'is-active' : ''}`}
      aria-current={active ? 'true' : undefined}
      onClick={onClick}
    >
      <span className="c-nav-row__icon" aria-hidden>
        {icon}
      </span>
      <span className="c-nav-row__label">{label}</span>
      {badge}
    </button>
  )
}

export default function Sidebar(p: Props) {
  const [groups, setGroups] = useState<Record<string, boolean>>(readGroups)
  const toggleGroup = (key: string) =>
    setGroups((s) => {
      const next = { ...s, [key]: !s[key] }
      try {
        localStorage.setItem(GROUP_KEY, JSON.stringify(next))
      } catch {
        /* 隐私模式下写不进去，不影响本次使用 */
      }
      return next
    })
  const [showChannelModal, setShowChannelModal] = useState(false)
  const [supOpen, setSupOpen] = useState(false)
  const [folded, setFolded] = useState<Record<string, boolean>>({})
  const [recovered, setRecovered] = useState<Record<string, string>>({})
  const hasParent = p.agents.some((a) => !!a.parentId)

  useEffect(() => {
    if (previewMode() || p.agents.length === 0 || hasParent) return
    let dead = false
    // 列表接口会把历史拿掉，父子关系只能从各会话最近的消息里认。
    Promise.all(
      p.agents.map((a) =>
        API.agentMessages(a.id, 80)
          .then((messages) => ({ id: a.id, name: a.name, parentId: a.parentId, messages: messages ?? [] }))
          .catch(() => ({ id: a.id, name: a.name, parentId: a.parentId, messages: [] })),
      ),
    ).then((list) => {
      if (dead) return
      const next: Record<string, string> = {}
      for (const a of linkParents(list)) {
        if (a.parentId) next[a.id] = a.parentId
      }
      setRecovered(next)
    })
    return () => {
      dead = true
    }
  }, [p.agents.length, hasParent])

  const linked = linkParents(
    p.agents.map((a) => (a.parentId || !recovered[a.id] ? a : { ...a, parentId: recovered[a.id] })),
  )
  const tree = agentTree(linked)

  return (
    <aside
      className="c-col"
      style={{
        width: 'var(--sidebar-w)',
        flexShrink: 0,
        background: 'var(--bg-glass)',
        backdropFilter: 'blur(var(--glass-blur))',
        borderRight: '1px solid var(--border-subtle)',
      }}
    >
      {/* 顶部 */}
      <div
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '0 var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <img
          src="/icon.png"
          alt=""
          width={22}
          height={22}
          style={{ borderRadius: 'var(--radius-sm)', flexShrink: 0 }}
        />
        <span
          className={`c-dot ${p.connected ? 'c-dot--connected' : 'c-dot--error'} ${p.connected ? '' : 'blink'}`}
        />
        <span style={{ fontSize: 'var(--text-md)', fontWeight: 600, letterSpacing: '-0.01em' }}>
          Agent
        </span>
        <span
          className="mono"
          title={`v${p.version || '—'}`}
          style={{
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
            marginTop: 1,
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {p.version ? `v${p.version}` : ''}
        </span>
        <button
          className="c-icon-btn"
          style={{ marginLeft: 'auto' }}
          onClick={p.onOpenSettings}
          title="设置"
          aria-label="设置"
        >
          ⚙
        </button>
      </div>

      {/* 统计条 */}
      <div
        style={{
          display: 'flex',
          gap: 'var(--space-3)',
          padding: '9px var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          fontSize: 'var(--text-xs)',
          color: 'var(--text-tertiary)',
          flexShrink: 0,
        }}
      >
        <span>
          共 <b style={{ color: 'var(--text-secondary)', fontWeight: 600 }}>{p.agents.length}</b>
        </span>
        <span style={{ marginLeft: 'auto', color: 'var(--text-disabled)' }}>
          {(() => {
            const n = p.agents.filter((a) => a.llm?.model && a.llm.provider !== 'mock').length
            return n === 0 ? '未配模型' : `${n} 个已配模型`
          })()}
        </span>
      </div>

      {/* 列表 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-2)' }}>
        <SectionLabel>工作区</SectionLabel>

        {/* 会话分组 */}
        {p.agents.length > 0 && (
          <GroupHead
            label="会话"
            count={p.agents.length}
            folded={!!groups.agent}
            onToggle={() => toggleGroup('agent')}
          />
        )}

        {/* 视图切换：舰队总览（并排监控） / 单个对话 */}
        {p.agents.length > 0 && (
          <div
            style={{
              display: 'flex',
              gap: 2,
              padding: 2,
              margin: '4px 0 6px',
              background: 'var(--bg-inset)',
              borderRadius: 'var(--radius-md)',
            }}
          >
            <ViewTab
              active={p.view === 'fleet'}
              onClick={() => p.onViewChange('fleet')}
              title="并排查看所有 agent 的进度"
            >
              ▦ 舰队
            </ViewTab>
            <ViewTab
              active={p.view === 'chat'}
              onClick={() => p.onViewChange('chat')}
              title="查看当前 agent 的完整对话"
            >
              💬 对话
            </ViewTab>
          </div>
        )}

        {p.agents.length === 0 && !p.createOpen && (
          <div className="c-empty" style={{ padding: 'var(--space-6) var(--space-3)' }}>
            {previewMode() ? '这是预览' : '还没有 agent'}
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 8, color: 'var(--text-disabled)' }}>
              {previewMode() ? '浏览器里的会话是假的。' : '点下面新建，可同时建多个并行跑'}
            </div>
          </div>
        )}

        {!groups.agent && tree.map((node) => (
          <AgentBranch
            key={node.agent.id}
            node={node}
            depth={0}
            activeId={p.activeKind === 'agent' ? p.activeId : null}
            folded={folded}
            onToggle={(id) => setFolded((s) => ({ ...s, [id]: !s[id] }))}
            onSelect={(id) => p.onSelect('agent', id)}
            onDelete={p.onDelete}
          />
        ))}

        {/* 频道分组 */}
        <GroupHead
          label="频道"
          count={p.channels.length}
          folded={!!groups.channel}
          onToggle={() => toggleGroup('channel')}
          action={
            <button
              className="c-icon-btn"
              style={{ width: 18, height: 18, fontSize: 'var(--text-sm)' }}
              onClick={() => setShowChannelModal(true)}
              title="新建频道"
              aria-label="新建频道"
            >
              +
            </button>
          }
        />
        {!groups.channel && p.channels.map((ch) => {
          const active = p.activeKind === 'channel' && ch.id === p.activeId
          return (
            <div
              key={ch.id}
              role="button"
              tabIndex={0}
              aria-current={active ? 'true' : undefined}
              aria-label={`频道 ${ch.name}`}
              onClick={() => p.onSelect('channel', ch.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  p.onSelect('channel', ch.id)
                }
              }}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 'var(--space-2)',
                padding: '6px 10px',
                borderRadius: 'var(--radius-md)',
                cursor: 'pointer',
                marginBottom: 1,
                background: active ? 'var(--bg-surface-active)' : 'transparent',
              }}
              onMouseEnter={(e) => {
                if (!active) e.currentTarget.style.background = 'var(--bg-surface)'
              }}
              onMouseLeave={(e) => {
                if (!active) e.currentTarget.style.background = 'transparent'
              }}
            >
              <span
                style={{
                  width: 19,
                  height: 19,
                  borderRadius: 'var(--radius-sm)',
                  background: active ? 'var(--accent-dim)' : 'var(--bg-surface)',
                  color: active ? 'var(--accent-bright)' : 'var(--text-tertiary)',
                  display: 'grid',
                  placeItems: 'center',
                  fontSize: 'var(--text-xs)',
                  fontWeight: 700,
                  flexShrink: 0,
                }}
              >
                #
              </span>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 'var(--text-base)',
                    fontWeight: active ? 600 : 500,
                    color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {ch.name}
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                  {(ch.members ?? []).length} 成员 · {(ch.messages ?? []).length} 消息
                </div>
              </div>
            </div>
          )
        })}

        {p.feishuRooms.length > 0 && (
          <GroupHead
            label="飞书"
            count={p.feishuRooms.length}
            folded={!!groups.feishu}
            onToggle={() => toggleGroup('feishu')}
          />
        )}
        {!groups.feishu && p.feishuRooms.map((room) => {
          const active = p.activeKind === 'feishu' && room.id === p.activeId
          return (
            <div
              key={room.id}
              className={`c-feishu-room ${active ? 'is-active' : ''}`}
              role="button"
              tabIndex={0}
              aria-current={active ? 'true' : undefined}
              aria-label={`飞书会话 ${room.name || '未命名'}`}
              onClick={() => p.onSelect('feishu', room.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  p.onSelect('feishu', room.id)
                }
              }}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 'var(--space-2)',
                padding: '6px 10px',
                borderRadius: 'var(--radius-md)',
                cursor: 'pointer',
                marginBottom: 1,
                background: active ? 'var(--bg-surface-active)' : 'transparent',
              }}
            >
              <div style={{ minWidth: 0 }}>
                <div
                  style={{
                    fontSize: 'var(--text-sm)',
                    color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {room.name || '飞书会话'}
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                  由{room.agentName || '本机会话'}回答
                </div>
                {p.onBindFeishu && p.agents.length > 0 && (
                  <select
                    aria-label={`${room.name}由谁回答`}
                    className="c-feishu-room__bind"
                    value={room.agentId || ''}
                    onClick={(e) => e.stopPropagation()}
                    onChange={(e) => {
                      e.stopPropagation()
                      if (e.target.value) p.onBindFeishu?.(room.id, e.target.value)
                    }}
                    style={{ marginTop: 2, maxWidth: 160, fontSize: 'var(--text-xs)' }}
                  >
                    {!room.agentId && <option value="">选择会话</option>}
                    {p.agents.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.name}
                      </option>
                    ))}
                  </select>
                )}
              </div>
            </div>
          )
        })}

        <SectionLabel>助手</SectionLabel>
        <NavRow
          icon="🌐"
          label="网关"
          active={p.view === 'gateway'}
          onClick={() => p.onViewChange('gateway')}
        />
        <NavRow
          icon="🧠"
          label="记忆"
          active={p.view === 'memory'}
          onClick={() => p.onViewChange('memory')}
        />
        <NavRow
          icon="🗓"
          label="计划"
          active={p.view === 'plan'}
          onClick={() => p.onViewChange('plan')}
        />

      </div>

      {/* 底部 */}
      <div style={{ padding: 'var(--space-2)', borderTop: '1px solid var(--border-subtle)', display: 'grid', gap: 6 }}>
        <button
          className="c-btn c-btn--secondary c-btn--full"
          onClick={() => p.onCreateOpenChange(true)}
        >
          <span style={{ fontSize: 'var(--text-base)', lineHeight: 1 }}>+</span> 新建 Agent
        </button>
        <button
          className="c-btn c-btn--ghost c-btn--full"
          title="输入目标，自动拆成子任务并行执行"
          onClick={() => setSupOpen(true)}
        >
          <span style={{ fontSize: 'var(--text-sm)', lineHeight: 1 }}>⑂</span> 自动拆任务
        </button>
      </div>

      {/* 新建 Agent 弹窗 */}
      <CreateAgentModal
        open={p.createOpen}
        onClose={() => p.onCreateOpenChange(false)}
        onCreate={p.onCreate}
        existingCount={p.agents.length}
      />

      <SupervisorModal
        open={supOpen}
        onClose={() => setSupOpen(false)}
        onCreate={p.onCreateSupervisor}
      />

      <CreateChannelModal
        open={showChannelModal}
        agents={p.agents}
        onClose={() => setShowChannelModal(false)}
        onCreate={(name, members) => {
          p.onCreateChannel(name, members)
          setShowChannelModal(false)
        }}
      />
    </aside>
  )
}
function CreateChannelModal({
  open,
  agents,
  onClose,
  onCreate,
}: {
  open: boolean
  agents: Agent[]
  onClose: () => void
  onCreate: (name: string, members: string[]) => void
}) {
  const [name, setName] = useState('')
  const [members, setMembers] = useState<string[]>([])

  if (!open) return null

  const toggle = (id: string) =>
    setMembers((m) => (m.includes(id) ? m.filter((x) => x !== id) : [...m, id]))

  const submit = () => {
    const n = name.trim()
    if (!n) return
    onCreate(n, members)
    setName('')
    setMembers([])
  }

  return (
    <div
      onClick={onClose}
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(0,0,0,0.6)',
        display: 'grid',
        placeItems: 'center',
        zIndex: 100,
        backdropFilter: 'blur(4px)',
      }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        className="fade-up"
        style={{
          width: 380,
          background: 'var(--bg-elevated)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius-lg)',
          boxShadow: 'var(--shadow-lg)',
          overflow: 'hidden',
        }}
      >
        <div
          style={{
            padding: '16px 20px',
            borderBottom: '1px solid var(--border-subtle)',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}
        >
          <span style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>新建频道</span>
          <button className="c-icon-btn" onClick={onClose}>
            ×
          </button>
        </div>

        <div style={{ padding: 20, display: 'grid', gap: 14 }}>
          <div>
            <label style={modalLabel}>频道名</label>
            <input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') submit()
                if (e.key === 'Escape') onClose()
              }}
              placeholder="例如：实现讨论"
              style={modalInput}
            />
          </div>
          <div>
            <label style={modalLabel}>成员（可多选）</label>
            {agents.length === 0 ? (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                还没有 agent，先创建一个
              </div>
            ) : (
              <div style={{ display: 'grid', gap: 4, maxHeight: 220, overflowY: 'auto' }}>
                {agents.map((a) => {
                  const on = members.includes(a.id)
                  return (
                    <button
                      key={a.id}
                      onClick={() => toggle(a.id)}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: 8,
                        padding: '7px 10px',
                        borderRadius: 'var(--radius-sm)',
                        border: `1px solid ${on ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
                        background: on ? 'var(--accent-dim)' : 'var(--bg-surface)',
                        color: on ? 'var(--accent-bright)' : 'var(--text-secondary)',
                        fontSize: 'var(--text-sm)',
                        textAlign: 'left',
                        cursor: 'pointer',
                      }}
                    >
                      <span
                        style={{
                          width: 16,
                          height: 16,
                          borderRadius: 'var(--radius-sm)',
                          background: a.color,
                          color: 'var(--bg-base)',
                          display: 'grid',
                          placeItems: 'center',
                          fontSize: 'var(--text-xs)',
                          fontWeight: 700,
                        }}
                      >
                        {a.name.slice(0, 1).toUpperCase()}
                      </span>
                      {a.name}
                      {on && <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)' }}>✓</span>}
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        </div>

        <div
          style={{
            padding: '16px 20px',
            borderTop: '1px solid var(--border-subtle)',
            display: 'flex',
            gap: 10,
          }}
        >
          <button className="c-btn c-btn--secondary" style={{ flex: 1 }} onClick={onClose}>
            取消
          </button>
          <button
            className="c-btn c-btn--primary"
            style={{ flex: 1 }}
            onClick={submit}
            disabled={!name.trim()}
          >
            创建
          </button>
        </div>
      </div>
    </div>
  )
}

const modalLabel: React.CSSProperties = {
  display: 'block',
  fontSize: 'var(--text-xs)',
  color: 'var(--text-secondary)',
  marginBottom: 6,
}

const modalInput: React.CSSProperties = {
  width: '100%',
  padding: '8px 12px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-sm)',
}
