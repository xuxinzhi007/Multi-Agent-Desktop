import { useCallback, useEffect, useRef, useState } from 'react'
import { API, type ModelChoice } from '../mygo'
import { toast } from './Toast'

/**
 * 会话里的模型选择。
 *
 * 选中态只看这次对话自己记下的模型。没选就停在「选择模型」，
 * 不会把别的会话或连接里的名字写进来。
 */
/** shortModel 把长模型名压成能读的样子：
 *  qwen2.5-0.5b-instruct-q4_k_m.gguf → qwen2.5-0.5b
 *  完整名字放在 title 里，鼠标悬停可见。 */
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

export default function ModelPicker({
  agentId,
  model,
  provider,
  onPick,
}: {
  agentId: string
  /** 这次对话已经记下的模型。空字符串表示还没选。 */
  model: string
  provider?: string
  onPick: (m: ModelChoice) => void
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
    if (open) void load()
  }, [open, load])

  // 换了一场对话，清掉上一场临时显示的名字。
  useEffect(() => {
    setBound('')
  }, [agentId])

  // 点击外部收起。用 mousedown 而不是 click：
  // click 在元素被移除后才触发，会漏判。
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
    if (!m.ready) {
      // 拦截但保持展开：用户下一步就要去配置，
      // 直接收起会让他再点一次才能回到这里。
      toast(`⚠️ ${m.model} 尚未配置，请先在设置里确认连接`, 'error')
      return
    }
    setBound(m.model)
    onPick(m)
    setOpen(false)
    toast(`✅ 已切换至 ${m.model}`, 'success')
  }

  const shown = model || bound
  const label = shown || '选择模型'
  const selected = (m: ModelChoice) =>
    m.ready && m.model === shown && (!provider || !model || m.provider === provider)
  const ready = items.filter((m) => m.ready)
  const disabled = items.filter((m) => !m.ready)

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
          {shortModel(label)}
        </span>
        <svg className="mp__caret" viewBox="0 0 12 12" aria-hidden>
          <path d="M3 4.5 6 7.5 9 4.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      </button>

      {open && (
        <div className="mp__pop" role="listbox" aria-label="可用模型">
          {loading && items.length === 0 && <div className="mp__empty">正在读取…</div>}
          {!loading && items.length === 0 && (
            <div className="mp__empty">
              没有可用模型。本地模型需要先启动服务。
            </div>
          )}

          {ready.map((m) => {
            const on = selected(m)
            return (
              <button
                key={`${m.provider}/${m.model}`}
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
            )
          })}

          {disabled.length > 0 && (
            <>
              <div className="mp__sep" />
              {disabled.map((m) => (
                <button
                  key={`${m.provider}/${m.model}`}
                  type="button"
                  role="option"
                  aria-selected={false}
                  // 不用 aria-disabled：这一项是可以点的 ——
                  // 点它是一种有意义的操作（告诉用户「去设置里配」）。
                  // 标成 disabled 会让屏幕阅读器和自动化测试都认为它不可交互，
                  // 用户点下去得不到任何解释。
                  aria-describedby={undefined}
                  className="mp__item is-off"
                  onClick={() => handlePick(m)}
                >
                  <span className="mp__item-name">{m.model}</span>
                  <span className="mp__tag mp__tag--off">未配置</span>
                </button>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  )
}
