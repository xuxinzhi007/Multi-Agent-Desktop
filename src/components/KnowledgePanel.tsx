import { useMemo, useState } from 'react'
import type { AgentView as Agent, Note } from '../mygo'

type Props = {
  notes: Note[]
  agents: Agent[]
  onUpsert: (n: Note) => void
  onDelete: (id: string) => void
  onAttach?: (noteId: string, agentId: string) => void
}

/**
 * KnowledgePanel 是多 agent 共享的「知识库」：
 * 人类编写的约定 / 术语 / 偏好，创建 agent 时自动注入 system prompt。
 */
export default function KnowledgePanel({ notes, agents, onUpsert, onDelete, onAttach }: Props) {
  const [query, setQuery] = useState('')
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [tags, setTags] = useState('')
  const [scope, setScope] = useState('')
  const [composing, setComposing] = useState(false)

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return notes
    return notes.filter((n) =>
      `${n.title}\n${n.body}\n${(n.tags ?? []).join(' ')}`.toLowerCase().includes(q),
    )
  }, [notes, query])

  const submit = () => {
    if (!title.trim() && !body.trim()) return
    onUpsert({
      id: '',
      title: title.trim(),
      body: body.trim(),
      tags: tags.split(',').map((t) => t.trim()).filter(Boolean),
      scope,
      at: 0,
    })
    setTitle('')
    setBody('')
    setTags('')
    setComposing(false)
  }

  return (
    <div style={{ padding: 'var(--space-3)' }}>
      <div style={{ display: 'flex', gap: 6, marginBottom: 'var(--space-3)' }}>
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="搜索笔记…"
          style={{
            flex: 1,
            padding: '6px 9px',
            borderRadius: 'var(--radius-sm)',
            border: '1px solid var(--border-default)',
            background: 'var(--bg-surface)',
            color: 'var(--text-primary)',
            fontSize: 'var(--text-xs)',
          }}
        />
        <button
          className="c-btn c-btn--secondary c-btn--sm"
          onClick={() => setComposing((v) => !v)}
        >
          {composing ? '取消' : '+ 新增'}
        </button>
      </div>

      {composing && (
        <div
          className="fade-up"
          style={{
            background: 'var(--bg-surface)',
            border: '1px solid var(--accent-border)',
            borderRadius: 'var(--radius-md)',
            padding: 10,
            marginBottom: 'var(--space-3)',
            display: 'grid',
            gap: 6,
          }}
        >
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder="标题，如：代码风格"
            style={fieldStyle}
          />
          <textarea
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder="内容，如：Go 用 %w 包装错误，禁止 panic"
            rows={3}
            style={{ ...fieldStyle, resize: 'vertical', lineHeight: 1.6 }}
          />
          <input
            value={tags}
            onChange={(e) => setTags(e.target.value)}
            placeholder="标签，逗号分隔：go, style"
            style={fieldStyle}
          />
          <select value={scope} onChange={(e) => setScope(e.target.value)} style={fieldStyle}>
            <option value="">全局（所有 Agent 可见）</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                仅 {a.name}
              </option>
            ))}
          </select>
          <button className="c-btn c-btn--primary c-btn--sm" onClick={submit}>
            保存笔记
          </button>
        </div>
      )}

      {filtered.length === 0 && (
        <div className="c-empty">
          知识库还是空的
          <br />
          <span style={{ fontSize: 'var(--text-xs)', marginTop: 6, display: 'inline-block' }}>
            写入团队约定，新 Agent 创建时自动带上这些上下文
          </span>
        </div>
      )}

      {filtered.map((n) => (
        <div
          key={n.id}
          style={{
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-subtle)',
            borderRadius: 'var(--radius-md)',
            padding: '9px 11px',
            marginBottom: 'var(--space-2)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600, color: 'var(--text-primary)' }}>
              {n.title || '(无标题)'}
            </span>
            <span
              className="c-badge"
              style={{ fontSize: 9, padding: '0 5px' }}
              title={n.scope ? '仅对该 Agent 生效' : '全局'}
            >
              {n.scope ? agents.find((a) => a.id === n.scope)?.name ?? '会话' : '全局'}
            </span>
            <button
              className="c-icon-btn c-icon-btn--danger"
              style={{ marginLeft: 'auto', width: 18, height: 18, fontSize: 12 }}
              onClick={() => onDelete(n.id)}
              title="删除笔记"
            >
              ×
            </button>
          </div>
          {n.body && (
            <div
              style={{
                marginTop: 4,
                fontSize: 'var(--text-xs)',
                color: 'var(--text-secondary)',
                lineHeight: 'var(--leading-normal)',
                whiteSpace: 'pre-wrap',
              }}
            >
              {n.body}
            </div>
          )}
          {(n.tags ?? []).length > 0 && (
            <div style={{ display: 'flex', gap: 4, marginTop: 6, flexWrap: 'wrap' }}>
              {(n.tags ?? []).map((t) => (
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
          {onAttach && agents.length > 0 && (
            <select
              value=""
              onChange={(e) => {
                if (e.target.value) onAttach(n.id, e.target.value)
                e.currentTarget.value = ''
              }}
              style={{ ...fieldStyle, marginTop: 6, fontSize: 10 }}
            >
              <option value="">注入到会话…</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          )}
        </div>
      ))}
    </div>
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
}
