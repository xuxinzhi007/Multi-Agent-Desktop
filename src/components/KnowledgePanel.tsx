import { useMemo, useState } from 'react'
import type { AgentView as Agent, Note } from '../mygo'
import Select from './Select'

type Props = {
  notes: Note[]
  agents: Agent[]
  onUpsert: (n: Note) => void
  onDelete: (id: string) => void
  onAttach?: (noteId: string, agentId: string) => void
  onDetach?: (noteId: string, agentId: string) => void
}

/**
 * KnowledgePanel 是多 agent 共享的知识库：人类写的约定 / 术语 / 偏好。
 *
 * 「沉淀」的关键是能看见东西被谁用了 —— 所以每条笔记都显示
 * 引用它的 agent，而不是给每条笔记挂一个「注入到会话」下拉框
 * （那会变成一屏下拉，既吵又看不出状态）。
 */
export default function KnowledgePanel({
  notes,
  agents,
  onUpsert,
  onDelete,
  onAttach,
  onDetach,
}: Props) {
  const [query, setQuery] = useState('')
  const [tag, setTag] = useState('')
  const [composing, setComposing] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)

  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [tags, setTags] = useState('')

  // 哪些 agent 引用了哪条笔记：从 Agent.Knowledge 反查
  const usersByNote = useMemo(() => {
    const m = new Map<string, Agent[]>()
    for (const a of agents) {
      for (const id of a.knowledge ?? []) {
        const arr = m.get(id) ?? []
        arr.push(a)
        m.set(id, arr)
      }
    }
    return m
  }, [agents])

  const allTags = useMemo(() => {
    const s = new Set<string>()
    for (const n of notes) for (const t of n.tags ?? []) s.add(t)
    return [...s].sort()
  }, [notes])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    return notes.filter((n) => {
      if (tag && !(n.tags ?? []).includes(tag)) return false
      if (!q) return true
      return `${n.title}\n${n.body}\n${(n.tags ?? []).join(' ')}`.toLowerCase().includes(q)
    })
  }, [notes, query, tag])

  // 被引用的排在前面：它们是真正在起作用的
  const sorted = useMemo(() => {
    return [...filtered].sort((a, b) => {
      const ua = usersByNote.get(a.id)?.length ?? 0
      const ub = usersByNote.get(b.id)?.length ?? 0
      if (ua !== ub) return ub - ua
      return (b.updatedAt ?? b.at ?? 0) - (a.updatedAt ?? a.at ?? 0)
    })
  }, [filtered, usersByNote])

  const submit = () => {
    if (!title.trim() && !body.trim()) return
    onUpsert({
      id: '',
      title: title.trim(),
      body: body.trim(),
      tags: tags.split(',').map((t) => t.trim()).filter(Boolean),
      scope: '',
      at: 0,
    })
    setTitle('')
    setBody('')
    setTags('')
    setComposing(false)
  }

  const referenced = notes.filter((n) => (usersByNote.get(n.id) ?? []).length > 0).length

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      {/* 概览：沉淀得怎么样，一眼能看到 */}
      <div
        style={{
          padding: 'var(--space-3)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <div
          style={{
            display: 'flex',
            alignItems: 'baseline',
            gap: 'var(--space-2)',
            marginBottom: 'var(--space-2)',
          }}
        >
          <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{notes.length} 条笔记</span>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)' }}>
            其中 {referenced} 条正被引用
          </span>
          <button
            className="c-btn c-btn--primary c-btn--sm"
            style={{ marginLeft: 'auto' }}
            onClick={() => setComposing((v) => !v)}
          >
            {composing ? '取消' : '+ 新增'}
          </button>
        </div>

        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索标题、正文、标签…"
          style={fieldStyle}
        />

        {allTags.length > 0 && (
          <div style={{ display: 'flex', gap: 4, flexWrap: 'wrap', marginTop: 6 }}>
            <TagChip active={!tag} onClick={() => setTag('')}>
              全部
            </TagChip>
            {allTags.map((t) => (
              <TagChip key={t} active={tag === t} onClick={() => setTag(tag === t ? '' : t)}>
                #{t}
              </TagChip>
            ))}
          </div>
        )}
      </div>

      {/* 编辑区 */}
      {composing && (
        <div
          className="fade-up"
          style={{
            padding: 'var(--space-3)',
            borderBottom: '1px solid var(--border-subtle)',
            background: 'var(--bg-surface)',
            flexShrink: 0,
          }}
        >
          <input
            autoFocus
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="标题，例如：接口错误统一用 wrapError"
            style={{ ...fieldStyle, marginBottom: 6 }}
          />
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="具体内容。agent 会把它当作上下文读到。"
            rows={4}
            style={{ ...fieldStyle, resize: 'vertical', marginBottom: 6, lineHeight: 1.6 }}
          />
          <input
            value={tags}
            onChange={(e) => setTags(e.target.value)}
            placeholder="标签，逗号分隔，例如：约定, 后端"
            style={{ ...fieldStyle, marginBottom: 6 }}
          />
          <button className="c-btn c-btn--primary c-btn--sm" onClick={submit}>
            保存
          </button>
        </div>
      )}

      {/* 列表 */}
      <div style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-2)', minHeight: 0 }}>
        {sorted.length === 0 && (
          <div className="c-empty">
            {notes.length === 0 ? (
              <>
                还没有笔记
                <br />
                <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
                  写下团队约定、术语、踩过的坑 —— agent 会把它们读进上下文
                </span>
              </>
            ) : (
              '没有匹配的笔记'
            )}
          </div>
        )}

        {sorted.map((n) => {
          const users = usersByNote.get(n.id) ?? []
          const isOpen = expanded === n.id
          return (
            <NoteCard
              key={n.id}
              note={n}
              users={users}
              agents={agents}
              open={isOpen}
              onToggle={() => setExpanded(isOpen ? null : n.id)}
              onDelete={() => onDelete(n.id)}
              onAttach={onAttach}
              onDetach={onDetach}
            />
          )
        })}
      </div>
    </div>
  )
}

function NoteCard({
  note,
  users,
  agents,
  open,
  onToggle,
  onDelete,
  onAttach,
  onDetach,
}: {
  note: Note
  users: Agent[]
  agents: Agent[]
  open: boolean
  onToggle: () => void
  onDelete: () => void
  onAttach?: (noteId: string, agentId: string) => void
  onDetach?: (noteId: string, agentId: string) => void
}) {
  const body = note.body ?? ''
  const long = body.length > 160

  return (
    <div
      style={{
        background: 'var(--bg-surface)',
        border: `1px solid ${users.length > 0 ? 'var(--border-default)' : 'var(--border-subtle)'}`,
        borderRadius: 'var(--radius-md)',
        padding: '9px 11px',
        marginBottom: 'var(--space-2)',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <span
          style={{
            fontSize: 'var(--text-sm)',
            fontWeight: 600,
            color: 'var(--text-primary)',
            minWidth: 0,
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
          title={note.title}
        >
          {note.title || '(无标题)'}
        </span>
        <button
          className="c-icon-btn c-icon-btn--danger"
          style={{ marginLeft: 'auto', width: 18, height: 18, fontSize: 12, flexShrink: 0 }}
          onClick={onDelete}
          title="删除笔记"
        >
          ×
        </button>
      </div>

      {body && (
        <div
          style={{
            marginTop: 4,
            fontSize: 'var(--text-xs)',
            color: 'var(--text-secondary)',
            lineHeight: 1.65,
            whiteSpace: 'pre-wrap',
            ...(long && !open
              ? {
                  display: '-webkit-box',
                  WebkitLineClamp: 3,
                  WebkitBoxOrient: 'vertical' as const,
                  overflow: 'hidden',
                }
              : {}),
          }}
        >
          {body}
        </div>
      )}
      {long && (
        <button
          className="c-link-btn"
          onClick={onToggle}
          style={{ fontSize: 'var(--text-xs)', marginTop: 2 }}
        >
          {open ? '收起' : '展开全文'}
        </button>
      )}

      {(note.tags ?? []).length > 0 && (
        <div style={{ display: 'flex', gap: 4, marginTop: 6, flexWrap: 'wrap' }}>
          {(note.tags ?? []).map((t) => (
            <span
              key={t}
              className="mono"
              style={{
                fontSize: 9,
                color: 'var(--text-disabled)',
                border: '1px solid var(--border-subtle)',
                borderRadius: 'var(--radius-full)',
                padding: '0 6px',
              }}
            >
              #{t}
            </span>
          ))}
        </div>
      )}

      {/* 引用情况：这才是「沉淀」的可见证据 */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 4,
          marginTop: 7,
          flexWrap: 'wrap',
          paddingTop: 6,
          borderTop: '1px solid var(--border-subtle)',
        }}
      >
        {users.length === 0 ? (
          <span style={{ fontSize: 10, color: 'var(--text-disabled)' }}>暂无 agent 引用</span>
        ) : (
          users.map((a) => (
            <span
              key={a.id}
              title={`${a.name} 正在引用${onDetach ? '，点击取消' : ''}`}
              onClick={onDetach ? () => onDetach(note.id, a.id) : undefined}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 3,
                fontSize: 10,
                color: a.color,
                border: `1px solid color-mix(in srgb, ${a.color} 40%, transparent)`,
                background: `color-mix(in srgb, ${a.color} 10%, transparent)`,
                borderRadius: 'var(--radius-full)',
                padding: '0 6px',
                cursor: onDetach ? 'pointer' : 'default',
              }}
            >
              {a.name}
            </span>
          ))
        )}

        {onAttach && agents.length > users.length && (
          <div style={{ marginLeft: 'auto', width: 96 }}>
            <Select
              value=""
              placeholder="+ 引用"
              options={agents
                .filter((a) => !users.some((u) => u.id === a.id))
                .map((a) => ({ value: a.id, label: a.name }))}
              onChange={(id) => id && onAttach(note.id, id)}
            />
          </div>
        )}
      </div>
    </div>
  )
}

function TagChip({
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
      type="button"
      onClick={onClick}
      style={{
        fontFamily: 'inherit',
        fontSize: 10,
        padding: '1px 7px',
        borderRadius: 'var(--radius-full)',
        border: `1px solid ${active ? 'var(--accent-border)' : 'var(--border-subtle)'}`,
        background: active ? 'var(--accent-dim)' : 'transparent',
        color: active ? 'var(--text-primary)' : 'var(--text-tertiary)',
        cursor: 'pointer',
      }}
    >
      {children}
    </button>
  )
}

const fieldStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 9px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-xs)',
  fontFamily: 'inherit',
}
