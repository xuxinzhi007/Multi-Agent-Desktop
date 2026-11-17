import { useCallback, useEffect, useState } from 'react'
import { API, type ModelChoice } from '../mygo'
import type { LLMConfig } from '../lib/useBackend'

/**
 * 模型列表。
 *
 * 之前的设置页是「先选 provider → 填 BaseURL → 填 Key → 再选模型」四步。
 * 任何一步不懂就卡住，而且用户真正想做的事只有一件：换个模型。
 *
 * 这里把它压成一个列表：看到模型名，点一下，就切过去了。
 * 凭据等细节收进「高级」，默认不占位置。
 */
export default function ModelList({
  current,
  onPick,
}: {
  current: LLMConfig
  onPick: (cfg: LLMConfig) => void
}) {
  const [items, setItems] = useState<ModelChoice[]>([])
  const [busy, setBusy] = useState(true)
  const [err, setErr] = useState('')

  const load = useCallback(async () => {
    setBusy(true)
    setErr('')
    try {
      setItems(await API.listAllModels())
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const pick = (m: ModelChoice) => {
    if (!m.ready || m.current) return
    onPick({
      provider: m.provider,
      baseUrl: m.baseUrl ?? '',
      // 同一 provider 内切模型要保留原 Key；跨 provider 没有 Key 可复用
      apiKey: m.provider === current.provider ? current.apiKey : '',
      model: m.model,
    })
  }

  const ready = items.filter((m) => m.ready)
  const notReady = items.filter((m) => !m.ready)

  return (
    <section className="c-settings__card">
      <div className="c-settings__card-head">
        <div>
          <div className="c-settings__card-title">模型</div>
          <div className="c-settings__card-sub">
            {current.provider ? `当前：${current.model || '未选择'}` : '点一个直接用'}
          </div>
        </div>
        <button className="c-btn c-btn--ghost" onClick={load} disabled={busy} type="button">
          {busy ? '刷新中…' : '刷新'}
        </button>
      </div>

      <div className="c-settings__card-body">
        {err && <p className="c-settings__prose">{err}</p>}
        {busy && items.length === 0 && <p className="c-settings__prose">正在读取可用模型…</p>}

        {!busy && ready.length === 0 && (
          <p className="c-settings__prose">
            没有可用的模型。本地模型需要先启动服务（<code className="c-settings__code">npm run dev:llama</code>）。
          </p>
        )}

        <div role="listbox" aria-label="可用模型" className="c-modellist">
          {ready.map((m) => {
            const on = m.current
            return (
              <button
                key={`${m.provider}/${m.model}`}
                type="button"
                role="option"
                aria-selected={on}
                onClick={() => pick(m)}
                className={`c-modellist__item ${on ? 'c-modellist__item--on' : ''}`}
              >
                <span className="c-modellist__name">{m.model}</span>
                <span className="c-modellist__meta">
                  {m.local ? '本地' : m.provider}
                </span>
                <span className="c-modellist__check">{on ? '✓' : ''}</span>
              </button>
            )
          })}
        </div>

        {notReady.length > 0 && (
          <div className="c-modellist__disabled">
            <div className="c-modellist__divider">还不可用</div>
            {notReady.map((m) => (
              <div key={`${m.provider}/${m.model}`} className="c-modellist__item is-off">
                <span className="c-modellist__name">{m.model}</span>
                <span className="c-modellist__meta">{m.note}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}
