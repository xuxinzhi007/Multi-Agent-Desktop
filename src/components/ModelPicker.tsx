import { useCallback, useEffect, useRef, useState } from 'react'
import { API, type ModelChoice } from '../mygo'
import { toast } from './Toast'

/**
 * 会话里的模型选择。
 *
 * 选中态只看这次对话自己记下的模型。没选就停在「选择模型」，
 * 不会把别的会话或连接里的名字写进来。
 * 能思考的模型，鼠标停在那一行时，档位直接浮在右侧。离开就收起。
 */
function shortModel(name: string): string {
  if (name.length <= 18) return name
  const cut = name.split(/[-_.]/)
  let out = ''
  for (const part of cut) {
    if (out && (out.length + part.length + 1) > 16) break
    out = out ? `${out}-${part}` : part
  }
  return out || name.slice(0, 16)
}

const EFFORT_LABEL: Record<string, string> = {
  low: '快',
  medium: '中',
  high: '高',
  xhigh: '更高',
  max: '最高',
}

function effortLabel(id: string): string {
  return EFFORT_LABEL[id] || id
}

export default function ModelPicker({
  agentId,
  model,
  provider,
  thinking,
  onPick,
  onThinking,
}: {
  agentId: string
  /** 这次对话已经记下的模型。空字符串表示还没选。 */
  model: string
  provider?: string
  thinking?: string
  onPick: (m: ModelChoice, thinking?: string) => void
  onThinking: (next: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<ModelChoice[]>([])
  const [loading, setLoading] = useState(false)
  const [bound, setBound] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setItems(await API.listAllModels())
    } catch {
      // 列表读不出来不该打断操作，保持上一次的结果
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    if (open) void load()
  }, [open, load])

  useEffect(() => {
    setBound('')
  }, [agentId])

  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    window.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      window.removeEventListener('keydown', onKey)
    }
  }, [open])

  const handlePick = (m: ModelChoice) => {
    if (!m.ready) return
    setBound(m.model)
    onPick(m)
    setOpen(false)
    toast(`✅ 已切换至 ${m.model}`, 'success')
  }

  const shown = model || bound
  const selected = (m: ModelChoice) =>
    m.ready && m.model === shown && (!provider || !model || m.provider === provider)
  const ready = items.filter((m) => m.ready)
  const current = ready.find(selected)
  const efforts = current?.efforts ?? []
  const stored = thinking || ''
  const currentEffort = stored === 'off'
    ? ''
    : efforts.includes(stored)
      ? stored
      : efforts.includes('medium')
        ? 'medium'
        : efforts[0] || ''
  const mark = stored === 'off' ? '关' : currentEffort ? effortLabel(currentEffort) : ''
  const label = shown ? (mark ? `${shown} · ${mark}` : shown) : '选择模型'

  const pickEffort = (m: ModelChoice, next: string) => {
    if (selected(m)) {
      onThinking(next)
      return
    }
    onPick(m, next)
  }

  return (
    <div ref={rootRef} className="mp">
      <button
        type="button"
        className={`mp__trigger ${open ? 'is-open' : ''}`}
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="listbox"
        aria-expanded={open}
        title={label}
      >
        <span className="mp__icon" aria-hidden>
          ✦
        </span>
        <span className="mp__label" title={label}>
          {shown ? shortModel(shown) : '选择模型'}
          {shown && mark ? ` · ${mark}` : ''}
        </span>
        <svg className="mp__caret" viewBox="0 0 12 12" aria-hidden>
          <path d="M3 4.5 6 7.5 9 4.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </button>

      {open && (
        <div className="mp__pop" role="listbox" aria-label="可用模型">
          {loading && items.length === 0 && <div className="mp__empty">正在读取…</div>}
          {!loading && ready.length === 0 && (
            <div className="mp__empty">没有已配置的模型。到设置的「连接」里保存后再选。</div>
          )}

          {ready.map((m) => {
            const on = selected(m)
            const levels = m.efforts ?? []
            const rowEffort = on && stored === 'off'
              ? ''
              : on && levels.includes(stored)
                ? stored
                : levels.includes('medium')
                  ? 'medium'
                  : levels[0] || ''
            return (
              <div key={`${m.provider}/${m.model}`} className="mp__row">
                <button
                  type="button"
                  role="option"
                  aria-selected={on}
                  className={`mp__item ${on ? 'is-on' : ''}`}
                  onClick={() => handlePick(m)}
                >
                  <span className="mp__item-name">{m.model}</span>
                  {m.local && <span className="mp__tag">本地</span>}
                  {on && (
                    <svg className="mp__check" viewBox="0 0 12 12" aria-hidden>
                      <path d="M2 6.2 4.8 9 10 3.4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
                    </svg>
                  )}
                </button>
                {levels.length > 0 && (
                  <div className="mp__side" role="menu" aria-label="推理强度">
                    {levels.map((id) => (
                      <button
                        key={id}
                        type="button"
                        role="menuitem"
                        className={on && id === rowEffort ? 'is-on' : ''}
                        onClick={() => pickEffort(m, id)}
                      >
                        <span>{effortLabel(id)}</span>
                        {on && id === rowEffort && (
                          <svg viewBox="0 0 12 12" aria-hidden>
                            <path d="M2 6.2 4.8 9 10 3.4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" />
                          </svg>
                        )}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
