export type ToolCall = {
  id: string
  tool: string
  target: string
  status: 'running' | 'ok' | 'error' | 'waiting' | 'cancelled'
  detail?: string
  at: number
}

const TOOL_META = {
  running: { icon: '◐', color: 'var(--warning)', label: '执行中' },
  ok: { icon: '✓', color: 'var(--success)', label: '完成' },
  error: { icon: '✕', color: 'var(--danger)', label: '失败' },
  waiting: { icon: '!', color: 'var(--accent-primary)', label: '待批准' },
  // 被用户插话打断：不是失败，中性灰展示，不进错误统计。
  cancelled: { icon: '⊘', color: 'var(--text-tertiary)', label: '已打断' },
} as const

type Tab = 'tools' | 'knowledge' | 'caps'

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
  /** 知识库面板内容；不传则不显示该 Tab */
  knowledge?: { count: number; node: React.ReactNode }
  /** 能力面板：这个会话有哪些工具与技能，哪些现在用不了 */
  caps?: { node: React.ReactNode }
}) {
  return (
    <aside
      className="c-col slide-in"
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
        <span className="c-section-label">检查器</span>
        <button
          className="c-icon-btn"
          style={{ marginLeft: 'auto' }}
          onClick={onClose}
          title="收起"
        >
          ›
        </button>
      </div>

      <div
        style={{
          display: 'flex',
          padding: 'var(--space-2) var(--space-3) 0',
          gap: 2,
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <TabBtn
          label="工具"
          count={tools.length}
          active={tab === 'tools'}
          onClick={() => onTab('tools')}
        />
        {knowledge && (
          <TabBtn
            label="知识"
            count={knowledge.count}
            active={tab === 'knowledge'}
            onClick={() => onTab('knowledge')}
          />
        )}
        {caps && (
          <TabBtn label="能力" count="" active={tab === 'caps'} onClick={() => onTab('caps')} />
        )}
      </div>

      <div style={{ flex: 1, overflowY: 'auto' }}>
        {tab === 'tools' && <ToolList tools={tools} />}
        {tab === 'knowledge' && knowledge && knowledge.node}
        {tab === 'caps' && caps && caps.node}
      </div>
    </aside>
  )
}

function TabBtn({
  label,
  count,
  active,
  onClick,
}: {
  label: string
  count: number | string
  active: boolean
  onClick: () => void
}) {
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
      {!!count && (
        <span
          style={{
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
            background: 'var(--bg-surface)',
            padding: '0 5px',
            borderRadius: 'var(--radius-full)',
            minWidth: 16,
            textAlign: 'center',
          }}
        >
          {count}
        </span>
      )}
    </button>
  )
}

function ToolList({ tools }: { tools: ToolCall[] }) {
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
    <div style={{ padding: 'var(--space-2)' }}>
      {tools.map((t) => {
        const meta = TOOL_META[t.status]
        const cancelled = t.status === 'cancelled'
        return (
          <div
            key={t.id}
            data-tool-status={t.status}
            style={{
              background: cancelled ? 'var(--bg-glass)' : 'var(--bg-surface)',
              border: cancelled ? '1px dashed var(--border-subtle)' : '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)',
              padding: '8px 10px',
              marginBottom: 'var(--space-2)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--space-2)' }}>
              <span
                style={{ color: meta.color, fontSize: 'var(--text-xs)', flexShrink: 0 }}
                className={t.status === 'running' ? 'blink' : ''}
              >
                {meta.icon}
              </span>
              <span
                className="mono"
                style={{ fontSize: 'var(--text-xs)', color: 'var(--accent-bright)' }}
              >
                {t.tool}
              </span>
              <span
                className="mono"
                style={{
                  flex: 1,
                  fontSize: 'var(--text-xs)',
                  color: 'var(--text-secondary)',
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                }}
                title={t.target}
              >
                {t.target}
              </span>
            </div>
            {t.detail && (
              <div
                style={{
                  marginTop: 5,
                  fontSize: 'var(--text-xs)',
                  color: 'var(--text-tertiary)',
                  lineHeight: 'var(--leading-normal)',
                }}
              >
                {t.detail}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

export type InspectorTab = Tab
