import { useEffect, useMemo, useRef, useState } from 'react'

export type ToolCall = {
  /** 前端列表行 key。后端 agent.tool 的 id 是会话 id，不能直接当列表 key。 */
  key: string
  id: string
  tool: string
  target: string
  status: 'running' | 'ok' | 'error' | 'waiting' | 'cancelled'
  detail?: string
  /** 有界工具输出，仅用于选中后的证据定位，不发额外文件请求。 */
  output?: string
  at: number
}

const TOOL_META = {
  running: { icon: '◐', color: 'var(--warning)', label: '执行中' },
  ok: { icon: '✓', color: 'var(--success)', label: '完成' },
  error: { icon: '✕', color: 'var(--danger)', label: '失败' },
  waiting: { icon: '!', color: 'var(--accent-primary)', label: '待批准' },
  cancelled: { icon: '⊘', color: 'var(--text-tertiary)', label: '已打断' },
} as const

type Tab = 'tools' | 'knowledge' | 'caps'
type ToolGroup = 'all' | 'read' | 'edit' | 'command' | 'collab' | 'other'
type StatusFilter = 'all' | ToolCall['status']

function groupOf(tool: string): Exclude<ToolGroup, 'all'> {
  if (['read_file', 'grep', 'glob', 'repo_map', 'list_dir', 'list_notes', 'search_memory', 'web'].includes(tool)) return 'read'
  if (['write_file', 'edit_file', 'apply_patch', 'worktree_merge', 'save_note'].includes(tool)) return 'edit'
  if (['bash', 'verify'].includes(tool)) return 'command'
  if (['ask_agent', 'create_agent', 'delegate', 'invite_to_channel', 'list_agents'].includes(tool)) return 'collab'
  return 'other'
}

function emptyResult(t: ToolCall): boolean {
  return t.status === 'ok' && /(\(no matches\)|\bno matches\b|\b0 matches\b)/i.test(`${t.detail || ''}\n${t.output || ''}`)
}

function fileRefOf(target: string): { label: string; line: number } | null {
  const hit = target.match(/((?:[A-Za-z]:[\\/]|\/|\.{1,2}[\\/])?[\w@./\\-]+\.[A-Za-z0-9]+):(\d+)/)
  return hit ? { label: hit[0], line: Number(hit[2]) } : null
}

export default function Inspector({
  tab,
  onTab,
  tools,
  onClose,
  knowledge,
  caps,
}: {
  tab: Tab
  onTab: (t: Tab) => void
  tools: ToolCall[]
  onClose: () => void
  knowledge?: { count: number; node: React.ReactNode }
  caps?: { node: React.ReactNode }
}) {
  return (
    <aside
      className="c-col c-inspector slide-in"
      style={{
        width: 'var(--inspector-w)',
        flexShrink: 0,
        background: 'var(--bg-glass)',
        backdropFilter: 'blur(var(--glass-blur))',
        borderLeft: '1px solid var(--border-subtle)',
      }}
    >
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
        <span className="c-section-label">HUD 检查器</span>
        <button className="c-icon-btn" style={{ marginLeft: 'auto' }} onClick={onClose} title="收起">
          ›
        </button>
      </div>

      <div className="c-inspector__tabs">
        <TabBtn label="工具" count={tools.length} active={tab === 'tools'} onClick={() => onTab('tools')} />
        {knowledge && (
          <TabBtn label="知识" count={knowledge.count} active={tab === 'knowledge'} onClick={() => onTab('knowledge')} />
        )}
        {caps && <TabBtn label="能力" count="" active={tab === 'caps'} onClick={() => onTab('caps')} />}
      </div>

      <div style={{ flex: 1, overflowY: 'auto', minHeight: 0 }}>
        {tab === 'tools' && <ToolList tools={tools} />}
        {tab === 'knowledge' && knowledge && knowledge.node}
        {tab === 'caps' && caps && caps.node}
      </div>
    </aside>
  )
}

function TabBtn({ label, count, active, onClick }: { label: string; count: number | string; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      style={{
        padding: '7px 11px',
        fontSize: 'var(--text-sm)',
        color: active ? 'var(--text-primary)' : 'var(--text-tertiary)',
        borderBottom: `2px solid ${active ? 'var(--accent-primary)' : 'transparent'}`,
        fontWeight: active ? 500 : 400,
        transition: 'color var(--dur-fast) var(--ease)',
        display: 'flex',
        alignItems: 'center',
        gap: 5,
      }}
    >
      {label}
      {!!count && <span className="c-inspector__count">{count}</span>}
    </button>
  )
}

function ToolList({ tools }: { tools: ToolCall[] }) {
  const [group, setGroup] = useState<ToolGroup>('all')
  const [status, setStatus] = useState<StatusFilter>('all')
  const [showEmpty, setShowEmpty] = useState(false)
  const [selectedKey, setSelectedKey] = useState('')
  const selectedRef = useRef<HTMLDivElement>(null)
  const followLatest = useRef(true)

  const candidates = useMemo(
    () => tools.filter((t) => (group === 'all' || groupOf(t.tool) === group) && (status === 'all' || t.status === status)),
    [tools, group, status],
  )
  const emptyCount = useMemo(() => candidates.filter(emptyResult).length, [candidates])
  const visible = useMemo(() => (showEmpty ? candidates : candidates.filter((t) => !emptyResult(t))), [candidates, showEmpty])
  const sequence = useMemo(() => new Map(tools.map((t, i) => [t.key, i + 1])), [tools])
  const activeCount = tools.filter((t) => t.status === 'running' || t.status === 'waiting').length
  const errorCount = tools.filter((t) => t.status === 'error').length
  const selectedIndex = visible.findIndex((t) => t.key === selectedKey)

  useEffect(() => {
    if (visible.length === 0) {
      if (selectedKey) setSelectedKey('')
      return
    }
    if (followLatest.current || !visible.some((t) => t.key === selectedKey)) {
      setSelectedKey(visible[visible.length - 1].key)
    }
  }, [visible, selectedKey])

  const selectTool = (key: string) => {
    followLatest.current = false
    setSelectedKey(key)
  }

  useEffect(() => {
    selectedRef.current?.scrollIntoView({ block: 'nearest' })
  }, [selectedKey])

  if (tools.length === 0) {
    return (
      <div className="c-empty">
        还没有工具调用
        <br />
        <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
          agent 调用工具时会实时出现在这里
        </span>
      </div>
    )
  }

  return (
    <div className="c-tool-hud">
      <div className="c-tool-hud__controls">
        <div className="c-tool-hud__summary">
          <span className={activeCount > 0 ? 'is-live' : ''}>{activeCount} 活跃</span>
          <span className={errorCount > 0 ? 'is-error' : ''}>{errorCount} 失败</span>
          <span>{visible.length}/{tools.length} 显示</span>
        </div>
        <div className="c-tool-hud__filters">
          <select aria-label="按工具类型筛选" value={group} onChange={(e) => setGroup(e.target.value as ToolGroup)}>
            <option value="all">全部工具</option>
            <option value="read">读取 / 检索</option>
            <option value="edit">文件 / 变更</option>
            <option value="command">命令 / 验证</option>
            <option value="collab">协作</option>
            <option value="other">其他</option>
          </select>
          <select aria-label="按结果状态筛选" value={status} onChange={(e) => setStatus(e.target.value as StatusFilter)}>
            <option value="all">全部状态</option>
            <option value="running">执行中</option>
            <option value="waiting">待批准</option>
            <option value="ok">完成</option>
            <option value="error">失败</option>
            <option value="cancelled">已打断</option>
          </select>
        </div>
        {emptyCount > 0 && (
          <button type="button" className="c-tool-hud__noise" aria-pressed={showEmpty} onClick={() => setShowEmpty((v) => !v)}>
            {showEmpty ? '折叠空结果' : `展开 ${emptyCount} 条空结果`}
          </button>
        )}
        {visible.length > 0 && (
          <div className="c-tool-timeline">
            <span>序列</span>
            <input
              aria-label="工具执行时间轴"
              type="range"
              min={1}
              max={visible.length}
              value={selectedIndex >= 0 ? selectedIndex + 1 : visible.length}
              onChange={(e) => selectTool(visible[Number(e.target.value) - 1].key)}
            />
            <span className="mono">{selectedIndex >= 0 ? selectedIndex + 1 : visible.length}/{visible.length}</span>
          </div>
        )}
      </div>

      {visible.length === 0 ? (
        <div className="c-tool-hud__empty">
          当前筛选没有结果
          {emptyCount > 0 && !showEmpty ? `，${emptyCount} 条空结果已折叠` : ''}
        </div>
      ) : (
        <div className="c-tool-hud__list">
          {visible.map((t) => (
            <ToolCard
              key={t.key}
              tool={t}
              sequence={sequence.get(t.key) ?? 0}
              selected={t.key === selectedKey}
              selectedRef={t.key === selectedKey ? selectedRef : undefined}
              onSelect={() => selectTool(t.key)}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function ToolCard({
  tool: t,
  sequence,
  selected,
  selectedRef,
  onSelect,
}: {
  tool: ToolCall
  sequence: number
  selected: boolean
  selectedRef?: React.RefObject<HTMLDivElement | null>
  onSelect: () => void
}) {
  const meta = TOOL_META[t.status]
  const fileRef = fileRefOf(t.target)
  const cancelled = t.status === 'cancelled'
  return (
    <div
      ref={selectedRef}
      data-tool-status={t.status}
      data-tool-selected={selected ? 'true' : 'false'}
      className={`c-tool-card ${selected ? 'is-selected' : ''} ${t.status === 'running' ? 'is-running' : ''}`}
      style={{
        background: cancelled ? 'var(--bg-glass)' : 'var(--bg-surface)',
        border: cancelled ? '1px dashed var(--border-subtle)' : undefined,
      }}
    >
      <button type="button" className="c-tool-card__head" aria-pressed={selected} onClick={onSelect}>
        <span className="c-tool-card__seq mono">#{String(sequence).padStart(2, '0')}</span>
        <span style={{ color: meta.color, flexShrink: 0 }}>{meta.icon}</span>
        <span className="mono c-tool-card__name">{t.tool}</span>
        <span className="c-tool-card__status">{meta.label}</span>
      </button>
      <div className="mono c-tool-card__target" title={t.target}>{t.target || '—'}</div>
      {fileRef && (
        <button type="button" className="c-tool-card__file mono" onClick={onSelect} title={`定位第 ${fileRef.line} 行的工具结果证据`}>
          {fileRef.label}
        </button>
      )}
      {t.detail && !(selected && t.output) && <div className="c-tool-card__detail">{t.detail}</div>}
      {selected && (t.output || t.detail) && (
        <pre className="c-tool-card__evidence" data-tool-evidence>
          {t.output || t.detail}
        </pre>
      )}
    </div>
  )
}

export type InspectorTab = Tab
