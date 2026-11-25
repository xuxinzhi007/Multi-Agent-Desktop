import { useCallback, useEffect, useState } from 'react'
import {
  DisabledState,
  EmptyState,
  ErrorState,
  PageBody,
  PageHeader,
  SkeletonRows,
} from './pageKit'
import Select from '../Select'
import {
  deleteMemoryNote,
  fetchMemoryNotes,
  fetchMemoryRecent,
  formatBytes,
  searchMemory,
  splitTags,
  upsertMemoryNote,
  useMemoryStatus,
  type MemoryItemView,
  type MemoryNoteView,
} from '../../lib/memory'
import type { AgentView as Agent } from '../../mygo'

/**
 * 记忆页（M1 / internal/memory）。
 *
 * 数据全部来自记忆 IPC：状态总览、最近记忆、跨会话搜索、手写笔记增删。
 * 记忆是跨会话自动沉淀的消息 / 事实 / 笔记，与现有知识库（人工整理、
 * 可挂载到会话的资料）是两套数据；本页只保留一个跳知识库的入口，
 * 不把知识库列表伪装成本页记忆。
 *
 * 四态齐全：加载 / 读取错误（可重试）/ 未启用（未装配、初始化失败，或不在桌面运行时内）/ 已接入。
 * 错误原文上屏，不编数据；空结果明确提示没有找到。
 */

const PANEL_STYLE: React.CSSProperties = {
  border: '1px solid var(--border-subtle)',
  borderRadius: 'var(--radius-md)',
  padding: 'var(--space-3)',
  display: 'grid',
  gap: 10,
}

const SECTION_TITLE: React.CSSProperties = {
  fontSize: 'var(--text-sm)',
  fontWeight: 600,
}

const META_STYLE: React.CSSProperties = {
  fontSize: 'var(--text-xs)',
  color: 'var(--text-tertiary)',
}

const FIELD_STYLE: React.CSSProperties = {
  width: '100%',
  padding: '6px 9px',
  borderRadius: 'var(--radius-sm)',
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontSize: 'var(--text-xs)',
  fontFamily: 'inherit',
}

function errText(e: unknown): string {
  return e instanceof Error && e.message ? e.message : String(e)
}

function formatTime(sec: number): string {
  if (!sec || sec <= 0) return ''
  return new Date(sec * 1000).toLocaleString()
}

function kindLabel(kind: string): string {
  switch (kind) {
    case 'note':
      return '笔记'
    case 'fact':
      return '事实'
    default:
      return '消息'
  }
}

const KIND_BADGE: Record<string, string> = {
  message: 'c-badge',
  note: 'c-badge c-badge--accent',
  fact: 'c-badge c-badge--success',
}

export default function MemoryPage({
  agents,
  onOpenKnowledge,
}: {
  agents: Agent[]
  onOpenKnowledge: () => void
}) {
  const { status, loading, error, unavailable, refresh } = useMemoryStatus(5000)

  const [recent, setRecent] = useState<MemoryItemView[]>([])
  const [notes, setNotes] = useState<MemoryNoteView[]>([])
  const [loadError, setLoadError] = useState('')

  const [query, setQuery] = useState('')
  const [agentFilter, setAgentFilter] = useState('')
  const [results, setResults] = useState<MemoryItemView[] | null>(null)
  const [searching, setSearching] = useState(false)
  const [searchError, setSearchError] = useState('')

  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')
  const [tags, setTags] = useState('')
  const [saving, setSaving] = useState(false)
  const [noteError, setNoteError] = useState('')

  // 明细（最近 / 笔记）在记忆库可用后拉一次；不可用保持空列表，不发请求。
  useEffect(() => {
    if (!status.available) {
      setRecent([])
      setNotes([])
      return
    }
    let stop = false
    Promise.all([fetchMemoryRecent(20), fetchMemoryNotes(50)])
      .then(([r, n]) => {
        if (stop) return
        setRecent(r)
        setNotes(n)
        setLoadError('')
      })
      .catch((e) => {
        if (!stop) setLoadError(errText(e))
      })
    return () => {
      stop = true
    }
  }, [status.available])

  const runSearch = useCallback(async () => {
    setSearching(true)
    setSearchError('')
    try {
      const list = await searchMemory(query.trim(), agentFilter, 20)
      setResults(list)
    } catch (e) {
      setSearchError(errText(e))
      setResults([])
    } finally {
      setSearching(false)
    }
  }, [query, agentFilter])

  const saveNote = async () => {
    if (!title.trim() || !body.trim()) {
      setNoteError('标题和正文不能为空。')
      return
    }
    setSaving(true)
    setNoteError('')
    try {
      await upsertMemoryNote(title.trim(), body.trim(), tags.trim())
      setTitle('')
      setBody('')
      setTags('')
      const list = await fetchMemoryNotes(50)
      setNotes(list)
    } catch (e) {
      setNoteError(errText(e))
    } finally {
      setSaving(false)
    }
  }

  const removeNote = async (id: number) => {
    setNoteError('')
    try {
      await deleteMemoryNote(id)
      setNotes((prev) => prev.filter((n) => n.id !== id))
    } catch (e) {
      setNoteError(errText(e))
    }
  }

  if (loading) {
    return (
      <div className="c-col" data-testid="memory-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="记忆" />
        <PageBody>
          <SkeletonRows rows={5} />
        </PageBody>
      </div>
    )
  }

  if (error && !unavailable) {
    return (
      <div className="c-col" data-testid="memory-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="记忆" />
        <PageBody>
          <ErrorState message={error} onRetry={() => void refresh()} />
        </PageBody>
      </div>
    )
  }

  if (unavailable || !status.available) {
    return (
      <div className="c-col" data-testid="memory-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="记忆" badge={<span className="c-badge c-badge--disabled">未接入</span>} />
        <PageBody>
          <DisabledState
            icon="🧠"
            title="记忆后端尚未接入"
            description="这里沉淀跨会话的消息、事实与手写笔记，按 Agent 归属并支持全文检索；它们和人工整理的知识库资料分开管理。当前记忆库未装配或初始化失败，本页不展示任何记忆条目。"
            note={status.error || '现有的人工资料请使用知识库（检查器 → 知识），二者不是同一套数据。'}
          >
            <button type="button" className="c-btn c-btn--secondary c-btn--sm" onClick={onOpenKnowledge}>
              打开知识库
            </button>
          </DisabledState>
        </PageBody>
      </div>
    )
  }

  const badge = status.error ? (
    <span className="c-badge c-badge--warning">部分异常</span>
  ) : (
    <span className="c-badge c-badge--success">已接入</span>
  )

  return (
    <div className="c-col" data-testid="memory-page" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader
        title="记忆"
        badge={badge}
        right={
          <span style={META_STYLE}>
            {status.lastWriteAt ? `最近写入 ${formatTime(status.lastWriteAt)}` : '暂无写入'}
          </span>
        }
      />
      <PageBody>
        <div style={{ display: 'grid', gap: 'var(--space-3)' }}>
          {/* 状态总览 */}
          <section style={PANEL_STYLE} aria-label="记忆状态">
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, alignItems: 'center' }}>
              <span style={META_STYLE}>
                事件 <strong>{status.events}</strong>
              </span>
              <span style={META_STYLE}>
                笔记 <strong>{status.notes}</strong>
              </span>
              <span style={META_STYLE}>
                事实 <strong>{status.facts}</strong>
              </span>
              <span style={META_STYLE}>库大小 {formatBytes(status.dbBytes)}</span>
              {status.path && (
                <span className="mono" style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  {status.path}
                </span>
              )}
            </div>
            {status.error && (
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                {status.error}
              </div>
            )}
            {loadError && (
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                明细读取失败：{loadError}
              </div>
            )}
          </section>

          {/* 搜索 */}
          <section style={PANEL_STYLE} aria-label="记忆搜索" data-testid="memory-search">
            <div style={SECTION_TITLE}>搜索记忆</div>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
              <input
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') void runSearch()
                }}
                placeholder="搜索记忆内容（关键词）…"
                style={{ ...FIELD_STYLE, flex: 1, minWidth: 160 }}
              />
              <div style={{ width: 160 }}>
                <Select
                  value={agentFilter}
                  placeholder="全部 Agent"
                  options={agents.map((a) => ({ value: a.id, label: a.name || a.id }))}
                  onChange={setAgentFilter}
                />
              </div>
              <button
                type="button"
                className="c-btn c-btn--primary c-btn--sm"
                disabled={searching}
                onClick={() => void runSearch()}
              >
                {searching ? '搜索中…' : '搜索'}
              </button>
            </div>

            {searchError && (
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                {searchError}
              </div>
            )}

            <div data-testid="memory-results">
              {results === null ? (
                <div style={META_STYLE}>输入关键词按 Agent 过滤，查跨会话沉淀的消息、事实与笔记；不是知识库资料。</div>
              ) : results.length === 0 ? (
                <EmptyState
                  icon="🔍"
                  title="没有找到相关记忆。"
                  hint="换个关键词，或清空 Agent 过滤后再试；空结果不会编造内容。"
                />
              ) : (
                <ItemList items={results} />
              )}
            </div>
          </section>

          {/* 最近记忆 */}
          <section style={PANEL_STYLE} aria-label="最近记忆" data-testid="memory-recent">
            <div style={SECTION_TITLE}>最近记忆（{recent.length}）</div>
            {recent.length === 0 ? (
              <div style={META_STYLE}>还没有沉淀记忆。会话里的用户 / 助手文本会按 Agent 自动归档，工具流与空内容不入记忆。</div>
            ) : (
              <ItemList items={recent} />
            )}
          </section>

          {/* 手写笔记 */}
          <section style={PANEL_STYLE} aria-label="记忆笔记" data-testid="memory-notes">
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
              <div style={SECTION_TITLE}>笔记（{notes.length}）</div>
              <span style={META_STYLE}>手动写下的决策与备忘，可被记忆检索命中。</span>
            </div>

            <div
              style={{
                display: 'grid',
                gap: 6,
                padding: 'var(--space-3)',
                border: '1px solid var(--border-subtle)',
                borderRadius: 'var(--radius-sm)',
                background: 'var(--bg-surface)',
              }}
            >
              <input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="笔记标题…"
                style={FIELD_STYLE}
              />
              <textarea
                value={body}
                onChange={(e) => setBody(e.target.value)}
                placeholder="笔记内容…"
                rows={3}
                style={{ ...FIELD_STYLE, resize: 'vertical', lineHeight: 1.6 }}
              />
              <input
                value={tags}
                onChange={(e) => setTags(e.target.value)}
                placeholder="标签，逗号分隔…"
                style={FIELD_STYLE}
              />
              {noteError && (
                <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                  {noteError}
                </div>
              )}
              <div>
                <button
                  type="button"
                  className="c-btn c-btn--secondary c-btn--sm"
                  disabled={saving}
                  onClick={() => void saveNote()}
                >
                  {saving ? '保存中…' : '保存笔记'}
                </button>
              </div>
            </div>

            {notes.length === 0 ? (
              <div style={META_STYLE}>还没有手写笔记。保存后会出现在这里，可随时删除。</div>
            ) : (
              <div style={{ display: 'grid', gap: 8 }}>
                {notes.map((n) => (
                  <NoteCard key={n.id} note={n} onDelete={() => void removeNote(n.id)} />
                ))}
              </div>
            )}
          </section>
        </div>
      </PageBody>
    </div>
  )
}

function ItemList({ items }: { items: MemoryItemView[] }) {
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      {items.map((m) => (
        <div
          key={`${m.kind}-${m.id}`}
          style={{
            display: 'flex',
            flexWrap: 'wrap',
            gap: 8,
            alignItems: 'center',
            fontSize: 'var(--text-xs)',
          }}
        >
          <span className={KIND_BADGE[m.kind] ?? 'c-badge'}>{kindLabel(m.kind)}</span>
          {m.agentId && (
            <span className="mono" style={{ color: 'var(--text-tertiary)' }}>
              {m.agentId}
            </span>
          )}
          {m.source && <span style={{ color: 'var(--text-secondary)' }}>{m.source}</span>}
          {m.createdAt > 0 && <span style={META_STYLE}>{formatTime(m.createdAt)}</span>}
          <div style={{ flex: '1 1 100%', color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
            {m.title && <strong>{m.title}</strong>}
            {m.title && m.snippet ? ' · ' : ''}
            {m.snippet}
          </div>
        </div>
      ))}
    </div>
  )
}

function NoteCard({ note, onDelete }: { note: MemoryNoteView; onDelete: () => void }) {
  return (
    <div
      style={{
        border: '1px solid var(--border-default)',
        borderRadius: 'var(--radius-sm)',
        padding: 10,
        display: 'grid',
        gap: 6,
      }}
    >
      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{note.title || '(无标题)'}</span>
        {splitTags(note.tags).map((t) => (
          <span key={t} className="c-kb-chip">
            #{t}
          </span>
        ))}
        <span style={{ marginLeft: 'auto', ...META_STYLE }}>
          {note.updatedAt > 0 ? formatTime(note.updatedAt) : note.createdAt > 0 ? formatTime(note.createdAt) : ''}
        </span>
        <button
          type="button"
          className="c-icon-btn c-icon-btn--danger"
          style={{ width: 18, height: 18, fontSize: 'var(--text-sm)' }}
          title="删除笔记"
          onClick={onDelete}
        >
          ×
        </button>
      </div>
      {note.body && (
        <div
          style={{
            fontSize: 'var(--text-xs)',
            color: 'var(--text-secondary)',
            lineHeight: 1.6,
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
          }}
        >
          {note.body}
        </div>
      )}
    </div>
  )
}
