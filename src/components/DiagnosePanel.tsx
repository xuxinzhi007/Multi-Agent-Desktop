import { useCallback, useEffect, useState } from 'react'
import { API, type Diagnostics } from '../mygo'

/**
 * 诊断面板。
 *
 * 存在的理由：用户报「发出去一直排队」时，可能的原因有五六种
 * （backend 配错、模型连不上、worker 死了、事件没派发、跑的是旧二进制），
 * 光看界面一个都分不出来，只能靠反复重启和猜。
 *
 * 这里把每一项直接测出来摆在一起，并且真的调一次模型 ——
 * 不是读配置说「应该能用」，而是确认「现在就能用」。
 */
export default function DiagnosePanel() {
  const [d, setD] = useState<Diagnostics | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const run = useCallback(async () => {
    setBusy(true)
    setErr('')
    try {
      setD(await API.diagnose())
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    void run()
  }, [run])

  return (
    <>
      <section className="c-settings__card">
        <div className="c-settings__card-head">
          <div>
            <div className="c-settings__card-title">链路自检</div>
            <div className="c-settings__card-sub">
              会真的发一次请求给模型，确认现在就能用
            </div>
          </div>
          <button
            className="c-btn c-btn--ghost"
            onClick={run}
            disabled={busy}
            type="button"
          >
            {busy ? '检查中…' : '重新检查'}
          </button>
        </div>
        <div className="c-settings__card-body">
          {err && (
            <p className="c-settings__prose" style={{ color: 'var(--c-danger, #ff5a7a)' }}>
              检查失败：{err}
            </p>
          )}
          {!d && !err && <p className="c-settings__prose">正在检查…</p>}
          {d && (
            <dl className="c-settings__kv">
              <Row label="后端版本" value={d.buildStamp} hint="重启应用后才会变" />
              <Row label="Provider" value={d.provider || '未设置'} />
              <Row label="模型" value={d.model || '未设置'} />
              <Row label="API Key" value={d.hasKey ? '已配置' : '未配置'} />
              <Row
                label="模型连通性"
                value={d.modelOk ? `正常（回复：${d.modelReply}）` : d.modelErr || '失败'}
                tone={d.modelOk ? 'ok' : 'bad'}
              />
            </dl>
          )}
        </div>
      </section>

      {d && (
        <section className="c-settings__card">
          <div className="c-settings__card-head">
            <div>
              <div className="c-settings__card-title">Agent 状态</div>
              <div className="c-settings__card-sub">
                一直「排队」通常是这里某个 agent 的 worker 已经停了
              </div>
            </div>
          </div>
          <div className="c-settings__card-body">
            {d.agents.length === 0 && <p className="c-settings__prose">还没有 agent</p>}
            <ul style={{ display: 'grid', gap: 8, margin: 0, padding: 0, listStyle: 'none' }}>
              {d.agents.map((a) => {
                // 排队的判断条件是队列里真的有东西。
                // 状态说 queued 但队列是空的，说明 worker 卡住了 ——
                // 这两种情况必须分开展示，否则永远查不出「为什么不动」。
                const stuck = a.status === 'queued' && a.queueLen === 0
                return (
                  <li
                    key={a.id}
                    style={{
                      display: 'grid',
                      gridTemplateColumns: '1fr auto',
                      gap: 8,
                      padding: '8px 10px',
                      border: '1px solid var(--border-subtle)',
                      borderRadius: 'var(--radius-md)',
                      background: 'var(--bg-surface)',
                      fontSize: 'var(--text-xs)',
                    }}
                  >
                    <div style={{ minWidth: 0 }}>
                      <div style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                        {a.name}
                      </div>
                      <div style={{ color: 'var(--text-tertiary)', marginTop: 2 }}>
                        backend={a.backend || '(默认)'} · 消息 {a.messageCount}
                        {a.queueLen > 0 && ` · 队列 ${a.queueLen}`}
                      </div>
                      {a.lastError && (
                        <div style={{ color: 'var(--c-danger, #ff5a7a)', marginTop: 3 }}>
                          {a.lastError}
                        </div>
                      )}
                    </div>
                    <div
                      style={{
                        alignSelf: 'center',
                        color: stuck ? 'var(--c-danger, #ff5a7a)' : 'var(--text-secondary)',
                        fontWeight: 600,
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {stuck ? '卡在排队' : a.status}
                    </div>
                  </li>
                )
              })}
            </ul>
          </div>
        </section>
      )}

      {d && d.loops.length > 0 && (
        <section className="c-settings__card">
          <div className="c-settings__card-head">
            <div>
              <div className="c-settings__card-title">最近的循环检查点</div>
              <div className="c-settings__card-sub">
                停在哪一轮、为什么停，这里能直接看到
              </div>
            </div>
          </div>
          <div className="c-settings__card-body">
            <dl className="c-settings__kv">
              {d.loops.map((l) => (
                <Row
                  key={l.agentId + l.checkpointAt}
                  label={l.agentId.slice(0, 12)}
                  value={`${l.phase} · 第 ${l.iteration} 轮 · 工具 ${l.usedTools} 次${l.stopReason ? ` · ${l.stopReason}` : ''}`}
                />
              ))}
            </dl>
          </div>
        </section>
      )}
    </>
  )
}

function Row({
  label,
  value,
  hint,
  tone,
}: {
  label: string
  value: string
  hint?: string
  tone?: 'ok' | 'bad'
}) {
  const color =
    tone === 'ok'
      ? 'var(--c-success, #34d399)'
      : tone === 'bad'
        ? 'var(--c-danger, #ff5a7a)'
        : 'var(--text-primary)'
  return (
    <div>
      <dt>{label}</dt>
      <dd style={{ color }}>{value}</dd>
      {hint && (
        <dd style={{ color: 'var(--text-disabled)', fontSize: 10 }}>{hint}</dd>
      )}
    </div>
  )
}
