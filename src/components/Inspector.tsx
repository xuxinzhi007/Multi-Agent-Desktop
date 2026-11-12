export type DiffLine = {
  kind: 'add' | 'del' | 'ctx'
  text: string
  oldNo?: number
  newNo?: number
}

export type FileChange = {
  path: string
  status: 'modified' | 'added' | 'deleted'
  added: number
  removed: number
  lines?: DiffLine[]
}

export type TodoItem = {
  id: string
  text: string
  status: 'pending' | 'active' | 'done'
}

export type ToolCall = {
  id: string
  tool: string
  target: string
  status: 'running' | 'ok' | 'error' | 'waiting'
  detail?: string
  at: number
}

const STATUS_META = {
  modified: { icon: 'M', cls: 'c-badge--warning', label: '改' },
  added: { icon: 'A', cls: 'c-badge--success', label: '增' },
  deleted: { icon: 'D', cls: 'c-badge--danger', label: '删' },
} as const

const TODO_META = {
  pending: { icon: '○', color: 'var(--text-disabled)' },
  active: { icon: '◐', color: 'var(--warning)' },
  done: { icon: '●', color: 'var(--success)' },
} as const

const TOOL_META = {
  running: { icon: '◐', color: 'var(--warning)', label: '执行中' },
  ok: { icon: '✓', color: 'var(--success)', label: '完成' },
  error: { icon: '✕', color: 'var(--danger)', label: '失败' },
  waiting: { icon: '!', color: 'var(--accent-primary)', label: '待批准' },
} as const

type Tab = 'changes' | 'todo' | 'tools' | 'knowledge'

export default function Inspector({
  tab,
  onTab,
  changes,
  todos,
  tools,
  onClose,
  onApprove,
  onReject,
  knowledge,
}: {
  tab: Tab
  onTab: (t: Tab) => void
  changes: FileChange[]
  todos: TodoItem[]
  tools: ToolCall[]
  onClose: () => void
  onApprove?: (id: string) => void
  onReject?: (id: string) => void
  /** 知识库面板内容；不传则不显示该 Tab */
  knowledge?: { count: number; node: React.ReactNode }
}) {
  const totalAdd = changes.reduce((n, c) => n + c.added, 0)
  const totalDel = changes.reduce((n, c) => n + c.removed, 0)
  const doneTodos = todos.filter((t) => t.status === 'done').length

  return (
    <aside
      className="c-col slide-in"
      style={{
        width: 'var(--inspector-w)',
        flexShrink: 0,
        background: 'var(--bg-chrome)',
        borderLeft: '1px solid var(--border-subtle)',
      }}
    >
      {/* 标题栏 */}
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

      {/* Tab 切换 */}
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
          label="改动"
          count={changes.length}
          active={tab === 'changes'}
          onClick={() => onTab('changes')}
        />
        <TabBtn
          label="任务"
          count={todos.length ? `${doneTodos}/${todos.length}` : 0}
          active={tab === 'todo'}
          onClick={() => onTab('todo')}
        />
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
      </div>

      {/* 汇总 */}
      {tab === 'changes' && changes.length > 0 && (
        <div
          className="mono"
          style={{
            padding: 'var(--space-2) var(--space-3)',
            fontSize: 'var(--text-xs)',
            color: 'var(--text-tertiary)',
            borderBottom: '1px solid var(--border-subtle)',
            flexShrink: 0,
          }}
        >
          {changes.length} 个文件{' '}
          <span style={{ color: 'var(--diff-add-text)' }}>+{totalAdd}</span>{' '}
          <span style={{ color: 'var(--diff-del-text)' }}>−{totalDel}</span>
        </div>
      )}

      {/* 内容 */}
      <div style={{ flex: 1, overflowY: 'auto' }}>
        {tab === 'changes' && <ChangesList changes={changes} />}
        {tab === 'todo' && <TodoList todos={todos} />}
        {tab === 'tools' && <ToolList tools={tools} />}
        {tab === 'knowledge' && knowledge && knowledge.node}
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

function ChangesList({ changes }: { changes: FileChange[] }) {
  if (changes.length === 0) {
    return (
      <div className="c-empty">
        agent 还没有改动文件
        <br />
        <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
          W3 接入文件读写工具后，这里会显示 diff
        </span>
      </div>
    )
  }

  return (
    <div style={{ padding: 'var(--space-2)' }}>
      {changes.map((c, ci) => {
        const meta = STATUS_META[c.status]
        return (
          <details
            key={c.path}
            open={ci === 0}
            style={{
              background: 'var(--bg-surface)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-md)',
              marginBottom: 'var(--space-2)',
              overflow: 'hidden',
            }}
          >
            <summary
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 'var(--space-2)',
                padding: '8px 10px',
                cursor: 'pointer',
                listStyle: 'none',
              }}
            >
              <span className={`c-badge ${meta.cls}`} style={{ padding: '0 5px', fontSize: 9 }}>
                {meta.icon}
              </span>
              <span
                className="mono"
                style={{
                  flex: 1,
                  fontSize: 'var(--text-xs)',
                  whiteSpace: 'nowrap',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  direction: 'rtl',
                  textAlign: 'left',
                }}
                title={c.path}
              >
                {c.path}
              </span>
              <span
                className="mono"
                style={{ fontSize: 9.5, color: 'var(--diff-add-text)', flexShrink: 0 }}
              >
                +{c.added}
              </span>
              <span
                className="mono"
                style={{ fontSize: 9.5, color: 'var(--diff-del-text)', flexShrink: 0 }}
              >
                −{c.removed}
              </span>
            </summary>

            {c.lines && c.lines.length > 0 && (
              <div
                className="mono"
                style={{
                  borderTop: '1px solid var(--border-subtle)',
                  fontSize: 10.5,
                  lineHeight: 1.7,
                  overflowX: 'auto',
                }}
              >
                {c.lines.map((l, i) => (
                  <div
                    key={i}
                    style={{
                      display: 'flex',
                      background:
                        l.kind === 'add'
                          ? 'var(--diff-add-bg)'
                          : l.kind === 'del'
                            ? 'var(--diff-del-bg)'
                            : 'transparent',
                    }}
                  >
                    <span
                      style={{
                        width: 34,
                        flexShrink: 0,
                        textAlign: 'right',
                        paddingRight: 6,
                        color: 'var(--text-disabled)',
                        userSelect: 'none',
                        fontSize: 9.5,
                      }}
                    >
                      {l.oldNo ?? ''}
                    </span>
                    <span
                      style={{
                        width: 34,
                        flexShrink: 0,
                        textAlign: 'right',
                        paddingRight: 8,
                        color: 'var(--text-disabled)',
                        userSelect: 'none',
                        fontSize: 9.5,
                      }}
                    >
                      {l.newNo ?? ''}
                    </span>
                    <span
                      style={{
                        flex: 1,
                        paddingRight: 10,
                        whiteSpace: 'pre',
                        color:
                          l.kind === 'add'
                            ? 'var(--diff-add-text)'
                            : l.kind === 'del'
                              ? 'var(--diff-del-text)'
                              : 'var(--text-secondary)',
                      }}
                    >
                      {l.text}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </details>
        )
      })}
    </div>
  )
}

function TodoList({ todos }: { todos: TodoItem[] }) {
  if (todos.length === 0) {
    return (
      <div className="c-empty">
        还没有任务清单
        <br />
        <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
          Supervisor 拆解任务后会出现在这里
        </span>
      </div>
    )
  }

  const done = todos.filter((t) => t.status === 'done').length
  const pct = Math.round((done / todos.length) * 100)

  return (
    <div style={{ padding: 'var(--space-3)' }}>
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          marginBottom: 6,
        }}
      >
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          进度 {done}/{todos.length}
        </span>
        <span className="mono" style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          {pct}%
        </span>
      </div>
      <div className="c-progress" style={{ marginBottom: 'var(--space-4)' }}>
        <div className="c-progress__fill" style={{ width: `${pct}%` }} />
      </div>

      {todos.map((t) => {
        const meta = TODO_META[t.status]
        return (
          <div
            key={t.id}
            style={{
              display: 'flex',
              alignItems: 'flex-start',
              gap: 'var(--space-2)',
              padding: '7px 0',
              borderBottom: '1px solid var(--border-subtle)',
            }}
          >
            <span
              style={{ color: meta.color, fontSize: 11, lineHeight: 1.6, flexShrink: 0 }}
              className={t.status === 'active' ? 'blink' : ''}
            >
              {meta.icon}
            </span>
            <span
              style={{
                fontSize: 'var(--text-sm)',
                lineHeight: 'var(--leading-normal)',
                color: t.status === 'done' ? 'var(--text-disabled)' : 'var(--text-primary)',
                textDecoration: t.status === 'done' ? 'line-through' : 'none',
              }}
            >
              {t.text}
            </span>
          </div>
        )
      })}
    </div>
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
        return (
          <div
            key={t.id}
            style={{
              background: 'var(--bg-surface)',
              border: `1px solid ${t.status === 'waiting' ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
              borderRadius: 'var(--radius-md)',
              padding: '8px 10px',
              marginBottom: 'var(--space-2)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 'var(--space-2)' }}>
              <span
                style={{ color: meta.color, fontSize: 10, flexShrink: 0 }}
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
            {t.status === 'waiting' && (
              <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
                <button
                  className="c-btn c-btn--success c-btn--sm"
                  onClick={() => onApprove?.(t.id)}
                >
                  允许
                </button>
                <button
                  className="c-btn c-btn--danger c-btn--sm"
                  onClick={() => onReject?.(t.id)}
                >
                  拒绝
                </button>
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

export type InspectorTab = Tab