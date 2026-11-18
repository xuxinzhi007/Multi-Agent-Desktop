import { useEffect, useMemo, useRef, useState } from 'react'
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
 * 标签超过 12 个就收成一行，其余放进抽屉，避免把列表挤出首屏。
 * 笔记默认是 72px 的紧凑卡片，引用情况在右下角胶囊里。
 */
const TAG_CAP = 12
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
  const [tagsOpen, setTagsOpen] = useState(false)
  const tagBox = useRef<HTMLDivElement>(null)
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
  const crowded = allTags.length > TAG_CAP

  useEffect(() => {
    if (!tagsOpen) return
    const close = (e: MouseEvent) => {
      if (!tagBox.current?.contains(e.target as Node)) setTagsOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [tagsOpen])

  const pickTag = (next: string) => {
    setTag(next)
    setTagsOpen(false)
  }

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
          <div className="c-kb-tagbox" ref={tagBox}>
            <div className="c-kb-tags">
              <TagChip active={!tag} onClick={() => pickTag('')}>
                全部
              </TagChip>
              {crowded ? (
                <>
                  {tag && (
                    <TagChip active onClick={() => pickTag('')}>
                      #{tag}
                    </TagChip>
                  )}
                  <button
                    type="button"
                    className="c-kb-tags__more"
                    aria-expanded={tagsOpen}
                    onClick={() => setTagsOpen((v) => !v)}
                  >
                    {tagsOpen ? '收起' : `全部 ${allTags.length} 个标签`}
                  </button>
                </>
              ) : (
                allTags.map((t) => (
                  <TagChip key={t} active={tag === t} onClick={() => pickTag(tag === t ? '' : t)}>
                    #{t}
                  </TagChip>
                ))
              )}
            </div>
            {crowded && tagsOpen && (
              <div className="c-kb-tags__drawer">
                {allTags.map((t) => (
                  <TagChip key={t} active={tag === t} onClick={() => pickTag(tag === t ? '' : t)}>
                    #{t}
                  </TagChip>
                ))}
              </div>
            )}
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
  const cited =
    users.length === 0
      ? '未引用'
      : users.length <= 2
        ? users.map((a) => a.name).join(' · ')
        : `${users.length} 个 Agent`

  return (
    <div
      className={open ? 'c-compact-card c-compact-card--open' : 'c-compact-card'}
      style={users.length > 0 ? { borderColor: 'var(--border-default)' } : undefined}
      onClick={onToggle}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onToggle()
        }
      }}
      role="button"
      tabIndex={0}
      aria-expanded={open}
    >
      <div className="c-compact-card__title" title={note.title}>
        {note.title || '(无标题)'}
      </div>
      {body && !open && (
        <div className="c-compact-card__body" title={body}>
          {body.replace(/\s+/g, ' ')}
        </div>
      )}
      <button
        className="c-icon-btn c-icon-btn--danger c-compact-card__x"
        style={{ width: 18, height: 18, fontSize: 12 }}
        onClick={(e) => {
          e.stopPropagation()
          onDelete()
        }}
        title="删除笔记"
      >
        ×
      </button>
      <span
        className="c-compact-card__cap"
        title={users.length ? users.map((a) => a.name).join('、') : '暂无 agent 引用'}
        style={
          users.length > 0
            ? {
                color: users[0].color,
                borderColor: `color-mix(in srgb, ${users[0].color} 40%, transparent)`,
                background: `color-mix(in srgb, ${users[0].color} 10%, transparent)`,
              }
            : undefined
        }
      >
        {cited}
      </span>

      {open && (
        <div onClick={(e) => e.stopPropagation()} style={{ marginTop: 8 }}>
          {body && (
            <div
              style={{
                fontSize: 'var(--text-xs)',
                color: 'var(--text-secondary)',
                lineHeight: 1.65,
                whiteSpace: 'pre-wrap',
              }}
            >
              {body}
            </div>
          )}
          {(note.tags ?? []).length > 0 && (
            <div style={{ display: 'flex', gap: 4, marginTop: 6, flexWrap: 'wrap' }}>
              {(note.tags ?? []).map((t) => (
                <span key={t} className="c-kb-chip">
                  #{t}
                </span>
              ))}
            </div>
          )}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 4,
              marginTop: 8,
              flexWrap: 'wrap',
            }}
          >
            {users.map((a) => (
              <span
                key={a.id}
                title={onDetach ? `${a.name}，点击取消引用` : a.name}
                onClick={onDetach ? () => onDetach(note.id, a.id) : undefined}
                style={{
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
            ))}
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
      )}
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
      className={active ? 'c-kb-chip c-kb-chip--on' : 'c-kb-chip'}
      onClick={onClick}
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
