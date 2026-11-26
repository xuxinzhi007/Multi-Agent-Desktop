import { isWriteTool, toolLabel as cnLabel } from '../lib/tool-names'

export type ToolCall = {
  id: string
  callId?: string
  tool: string
  target: string
  status: 'running' | 'ok' | 'error' | 'waiting' | 'cancelled'
  detail?: string
  input?: string
  output?: string
  path?: string
  diff?: string
  truncated?: boolean
  exitCode?: number
  durationMs?: number
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

/** 工具名一律给中文名 + 英文原名，避免「Bash Bash」这种重复。 */
function toolTitle(name: string) {
  const cn = cnLabel(name)
  return cn === name ? name : `${cn} · ${name}`
}

/**
 * 列表里只放一行有用的话：优先路径，其次命令，再其次原始参数。
 * 之前整段 JSON 直接上屏，一条就占满三行，还看不出改的是哪个文件。
 */
function toolSummary(t: ToolCall) {
  if (t.path) return t.path
  const raw = (t.target || '').trim()
  if (!raw) return ''
  if (raw.startsWith('{')) {
    try {
      const v = JSON.parse(raw) as Record<string, unknown>
      for (const k of ['file_path', 'path', 'command', 'cmd', 'pattern', 'query']) {
        const s = v[k]
        if (typeof s === 'string' && s.trim()) return s
      }
    } catch {
      /* 截断过的 JSON 解析不了，退回原文 */
    }
  }
  return raw
}

type Tab = 'tools' | 'changes' | 'knowledge' | 'caps'

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
  // 结束事件里偶尔只有占位名 "tool"（老版本 CLI 不带 callId），
  // 拿不到路径也没有入参的这类行没有信息量，列表里直接不展示。
  const visible = tools.filter((t) => t.tool !== 'tool' || !!t.path || !!t.input || t.status === 'error')
  const changes = visible.filter((t) => isWriteTool(t.tool) && (t.path || t.diff))
  const changeCount = new Set(
    changes.flatMap((t) => (t.path || '未标注路径').split(',').map((p) => p.trim()).filter(Boolean)),
  ).size

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
          count={visible.length}
          active={tab === 'tools'}
          onClick={() => onTab('tools')}
        />
        <TabBtn
          label="改动"
          count={changeCount}
          active={tab === 'changes'}
          onClick={() => onTab('changes')}
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
        {tab === 'tools' && <ToolList tools={visible} />}
        {tab === 'changes' && <ChangeList tools={changes} />}
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
      data-tab-active={active ? 'true' : 'false'}
      aria-pressed={active}
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
      {tools.map((t, index) => {
        const meta = TOOL_META[t.status]
        const cancelled = t.status === 'cancelled'
        return (
          <div
            key={`${t.id}:${t.callId ?? t.tool}:${t.at}:${index}`}
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
                style={{ fontSize: 'var(--text-xs)', color: 'var(--accent-bright)', flexShrink: 0 }}
              >
                {toolTitle(t.tool)}
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
                title={toolSummary(t)}
              >
                {toolSummary(t)}
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
            {(t.exitCode !== undefined || t.durationMs !== undefined || t.truncated) && (
              <div style={{ marginTop: 5, fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
                {t.exitCode !== undefined && `exit ${t.exitCode}`}
                {t.durationMs !== undefined && ` · ${t.durationMs}ms`}
                {t.truncated && ' · 展示已截断'}
              </div>
            )}
            {(t.input || t.output || t.diff) && (
              <details style={{ marginTop: 7 }}>
                <summary style={{ cursor: 'pointer', fontSize: 'var(--text-xs)', color: 'var(--accent-bright)' }}>
                  查看调用与结果
                </summary>
                {t.input && <DetailBlock title="输入" text={t.input} />}
                {t.output && <DetailBlock title="输出" text={t.output} />}
                {t.diff && <DetailBlock title="文件 diff" text={t.diff} />}
              </details>
            )}
          </div>
        )
      })}
    </div>
  )
}

function DetailBlock({ title, text }: { title: string; text: string }) {
  return (
    <div style={{ marginTop: 7 }}>
      <div style={{ marginBottom: 3, fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>{title}</div>
      <pre
        className="mono"
        style={{
          margin: 0,
          padding: 8,
          maxHeight: 260,
          overflow: 'auto',
          whiteSpace: 'pre-wrap',
          overflowWrap: 'anywhere',
          border: '1px solid var(--border-subtle)',
          borderRadius: 'var(--radius-md)',
          background: 'var(--bg-inset)',
          color: 'var(--text-secondary)',
          fontSize: 'var(--text-xs)',
          lineHeight: 1.55,
        }}
      >
        {text}
      </pre>
    </div>
  )
}

function ChangeList({ tools }: { tools: ToolCall[] }) {
  if (tools.length === 0) {
    return (
      <div className="c-empty">
        还没有可见的文件改动
        <br />
        <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
          文件工具成功落盘后，路径和 diff 会出现在这里
        </span>
      </div>
    )
  }
  return (
    <div style={{ padding: 'var(--space-2)' }}>
      <div style={{ padding: '4px 4px 10px', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', lineHeight: 1.6 }}>
        按工具完成顺序记录。终端命令造成的改动不在此列表中，可在项目里用 git diff 复核。
      </div>
      {tools.map((t, index) => (
        <section
          key={`${t.id}:${t.callId ?? t.tool}:${t.at}:${index}`}
          data-file-change={t.path || 'unknown'}
          style={{
            marginBottom: 'var(--space-2)',
            padding: '9px 10px',
            border: '1px solid var(--border-subtle)',
            borderRadius: 'var(--radius-md)',
            background: 'var(--bg-surface)',
          }}
        >
          <div className="mono" style={{ color: 'var(--accent-bright)', fontSize: 'var(--text-xs)', overflowWrap: 'anywhere' }}>
            {t.path || '未标注路径'}
          </div>
          <div style={{ marginTop: 3, color: 'var(--text-disabled)', fontSize: 'var(--text-xs)' }}>
            {t.tool} · {t.status === 'ok' ? '已落盘' : t.status === 'error' ? '失败' : t.status}
          </div>
          {t.diff ? (
            <DetailBlock title="统一 diff" text={t.diff} />
          ) : (
            <div style={{ marginTop: 7, color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>
              这个运行时只报告了文件路径，没有提供 diff。
            </div>
          )}
        </section>
      ))}
    </div>
  )
}

export type InspectorTab = Tab
