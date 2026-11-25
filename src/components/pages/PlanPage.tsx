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
  createPlan,
  deletePlan,
  fetchPlanList,
  pausePlan,
  runPlanNow,
  usePlanStatus,
  type PlanRequest,
  type PlanView,
} from '../../lib/plan'
import type { AgentView as Agent } from '../../mygo'

/**
 * 计划页（M2 / internal/scheduler）。
 *
 * 数据全部来自调度 IPC：调度器总览、任务列表、新建 interval / cron、
 * 暂停 / 恢复、删除、立即运行。任务 Runner 由主进程接到 Agent.Manager.Prompt，
 * 按绑定 agentId 发 prompt，不在前端伪造回复；工具操作仍走现有审批，
 * 审批超时 / 无人值守按 fail-closed 拒绝。
 *
 * 四态齐全：加载 / 读取错误（可重试）/ 未启用（未装配或不在桌面运行时内）/ 已接入。
 * 后端校验错误原文上屏；空列表明确提示。
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
  if (!sec || sec <= 0) return '—'
  return new Date(sec * 1000).toLocaleString()
}

function typeLabel(type: string): string {
  return type === 'cron' ? 'Cron' : '间隔'
}

export default function PlanPage({ agents }: { agents: Agent[] }) {
  const { status, loading, error, unavailable, refresh } = usePlanStatus(5000)

  const [list, setList] = useState<PlanView[]>([])
  const [listError, setListError] = useState('')

  const [type, setType] = useState<'interval' | 'cron'>('interval')
  const [intervalSec, setIntervalSec] = useState('300')
  const [cron, setCron] = useState('')
  const [agentId, setAgentId] = useState('')
  const [prompt, setPrompt] = useState('')
  const [creating, setCreating] = useState(false)
  const [formError, setFormError] = useState('')

  const [busy, setBusy] = useState<Record<string, string>>({})
  const [rowError, setRowError] = useState<Record<string, string>>({})

  const reload = useCallback(async () => {
    try {
      const tasks = await fetchPlanList()
      setList(tasks)
      setListError('')
    } catch (e) {
      setListError(errText(e))
    }
  }, [])

  useEffect(() => {
    if (!status.available) {
      setList([])
      return
    }
    let stop = false
    void reload().then(() => {
      if (stop) return
    })
    return () => {
      stop = true
    }
  }, [status.available, reload])

  const validate = (): string => {
    if (!agentId) return '请选择要绑定的 Agent。'
    if (!prompt.trim()) return '请填写发给 Agent 的指令。'
    if (type === 'interval') {
      const sec = Number(intervalSec)
      if (!Number.isFinite(sec) || sec <= 0) return '间隔任务的秒数必须大于 0。'
    }
    if (type === 'cron' && !cron.trim()) return 'Cron 任务需要填写 5 段 cron 表达式。'
    return ''
  }

  const submit = async () => {
    const local = validate()
    if (local) {
      setFormError(local)
      return
    }
    const req: PlanRequest = {
      id: '',
      type,
      intervalSec: type === 'interval' ? Number(intervalSec) : 0,
      cron: type === 'cron' ? cron.trim() : '',
      agentId,
      prompt: prompt.trim(),
      channel: '',
      enabled: true,
    }
    setCreating(true)
    setFormError('')
    try {
      await createPlan(req)
      setPrompt('')
      setCron('')
      setIntervalSec('300')
      await reload()
    } catch (e) {
      setFormError(errText(e))
    } finally {
      setCreating(false)
    }
  }

  const act = async (id: string, action: 'pause' | 'resume' | 'delete' | 'run') => {
    setRowError((prev) => ({ ...prev, [id]: '' }))
    setBusy((prev) => ({ ...prev, [id]: action }))
    try {
      if (action === 'pause') await pausePlan(id, true)
      else if (action === 'resume') await pausePlan(id, false)
      else if (action === 'delete') await deletePlan(id)
      else await runPlanNow(id)
      await reload()
    } catch (e) {
      setRowError((prev) => ({ ...prev, [id]: errText(e) }))
    } finally {
      setBusy((prev) => {
        const next = { ...prev }
        delete next[id]
        return next
      })
    }
  }

  if (loading) {
    return (
      <div className="c-col" data-testid="plan-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="计划" />
        <PageBody>
          <SkeletonRows rows={4} />
        </PageBody>
      </div>
    )
  }

  if (error && !unavailable) {
    return (
      <div className="c-col" data-testid="plan-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="计划" />
        <PageBody>
          <ErrorState message={error} onRetry={() => void refresh()} />
        </PageBody>
      </div>
    )
  }

  if (unavailable || !status.available) {
    return (
      <div className="c-col" data-testid="plan-page" style={{ flex: 1, minWidth: 0 }}>
        <PageHeader title="计划" badge={<span className="c-badge c-badge--disabled">未接入</span>} />
        <PageBody>
          <DisabledState
            icon="🗓"
            title="调度器尚未启动"
            description="这里管理间隔 / cron 定时任务，按绑定 Agent 定时发送指令，显示上次 / 下次运行时间与失败次数；任务执行仍走现有审批，超时或无人值守按拒绝处理。当前调度器未装配，不会创建或执行任何定时任务。"
            note={status.error || '调度器落地后，本页提供新建、暂停 / 恢复、删除与立即运行。'}
          />
        </PageBody>
      </div>
    )
  }

  const badge = status.error ? (
    <span className="c-badge c-badge--warning">部分异常</span>
  ) : status.running ? (
    <span className="c-badge c-badge--success">运行中</span>
  ) : (
    <span className="c-badge c-badge--disabled">已停止</span>
  )

  return (
    <div className="c-col" data-testid="plan-page" style={{ flex: 1, minWidth: 0 }}>
      <PageHeader
        title="计划"
        badge={badge}
        right={
          <>
            <span style={META_STYLE}>
              共 {status.count} · 启用 {status.enabled}
            </span>
            {status.quiet && <span style={META_STYLE}>静默 {status.quiet}</span>}
            {status.lastTickAt > 0 && <span style={META_STYLE}>上次心跳 {formatTime(status.lastTickAt)}</span>}
          </>
        }
      />
      <PageBody>
        <div style={{ display: 'grid', gap: 'var(--space-3)' }}>
          {status.error && (
            <section style={PANEL_STYLE} aria-label="调度错误">
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                {status.error}
              </div>
            </section>
          )}
          {listError && (
            <section style={PANEL_STYLE} aria-label="列表错误">
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                列表读取失败：{listError}
              </div>
            </section>
          )}

          {/* 新建任务 */}
          <section style={PANEL_STYLE} aria-label="新建计划" data-testid="plan-create">
            <div style={SECTION_TITLE}>新建计划</div>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              <div role="group" aria-label="任务类型" style={{ display: 'flex', gap: 8 }}>
                <button
                  type="button"
                  className={type === 'interval' ? 'c-btn c-btn--secondary c-btn--sm' : 'c-btn c-btn--ghost c-btn--sm'}
                  onClick={() => setType('interval')}
                >
                  间隔
                </button>
                <button
                  type="button"
                  className={type === 'cron' ? 'c-btn c-btn--secondary c-btn--sm' : 'c-btn c-btn--ghost c-btn--sm'}
                  onClick={() => setType('cron')}
                >
                  Cron
                </button>
              </div>
              {type === 'interval' ? (
                <input
                  value={intervalSec}
                  onChange={(e) => setIntervalSec(e.target.value)}
                  placeholder="间隔秒数，例如 300"
                  style={{ ...FIELD_STYLE, width: 160 }}
                />
              ) : (
                <input
                  value={cron}
                  onChange={(e) => setCron(e.target.value)}
                  placeholder="5 段 cron，例如 0 9 * * *"
                  style={{ ...FIELD_STYLE, width: 220 }}
                />
              )}
              <div style={{ width: 180 }}>
                <Select
                  value={agentId}
                  placeholder="绑定 Agent"
                  options={agents.map((a) => ({ value: a.id, label: a.name || a.id }))}
                  onChange={setAgentId}
                />
              </div>
            </div>
            <textarea
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="发给绑定 Agent 的指令…"
              rows={2}
              style={{ ...FIELD_STYLE, resize: 'vertical', lineHeight: 1.6 }}
            />
            {formError && (
              <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
                {formError}
              </div>
            )}
            <div>
              <button
                type="button"
                className="c-btn c-btn--primary c-btn--sm"
                disabled={creating}
                onClick={() => void submit()}
              >
                {creating ? '创建中…' : '新建计划'}
              </button>
            </div>
          </section>

          {/* 任务列表 */}
          <section style={PANEL_STYLE} aria-label="计划列表" data-testid="plan-list">
            <div style={SECTION_TITLE}>任务（{list.length}）</div>
            {list.length === 0 ? (
              <EmptyState
                icon="🗓"
                title="还没有计划"
                hint="新建间隔或 cron 任务，绑定一个 Agent 并写下指令；到达时间后由调度器投递，执行结果与失败次数会更新在这里。"
              />
            ) : (
              <div style={{ display: 'grid', gap: 8 }}>
                {list.map((p) => (
                  <PlanRow
                    key={p.id}
                    plan={p}
                    busy={busy[p.id]}
                    error={rowError[p.id] ?? ''}
                    onAct={(action) => void act(p.id, action)}
                  />
                ))}
              </div>
            )}
          </section>
        </div>
      </PageBody>
    </div>
  )
}

function PlanRow({
  plan,
  busy,
  error,
  onAct,
}: {
  plan: PlanView
  busy: string | undefined
  error: string
  onAct: (action: 'pause' | 'resume' | 'delete' | 'run') => void
}) {
  const agentLabel = plan.agentId
  const failures = plan.failures > 0 ? plan.failures : 0
  return (
    <div
      data-testid={`plan-row-${plan.id}`}
      style={{
        border: '1px solid var(--border-default)',
        borderRadius: 'var(--radius-sm)',
        padding: 10,
        display: 'grid',
        gap: 8,
      }}
    >
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}>
        <span className="c-badge c-badge--accent">{typeLabel(plan.type)}</span>
        {plan.type === 'interval' ? (
          <span style={META_STYLE}>{plan.intervalSec} 秒</span>
        ) : (
          <span className="mono" style={{ fontSize: 'var(--text-xs)' }}>
            {plan.cron}
          </span>
        )}
        <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{agentLabel}</span>
        <span className={plan.enabled ? 'c-badge c-badge--success' : 'c-badge c-badge--disabled'}>
          {plan.enabled ? '启用' : '已暂停'}
        </span>
        {failures > 0 && <span className="c-badge c-badge--warning">失败 {failures}</span>}
        <span style={{ marginLeft: 'auto', ...META_STYLE }}>
          上次 {formatTime(plan.lastRunAt)} · 下次 {formatTime(plan.nextRunAt)}
        </span>
      </div>

      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
        {plan.prompt}
      </div>

      {plan.lastError && (
        <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
          最近错误：{plan.lastError}
        </div>
      )}
      {error && (
        <div role="alert" style={{ fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
          {error}
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        {plan.enabled ? (
          <button
            type="button"
            className="c-btn c-btn--ghost c-btn--sm"
            disabled={!!busy}
            onClick={() => onAct('pause')}
          >
            {busy === 'pause' ? '暂停中…' : '暂停'}
          </button>
        ) : (
          <button
            type="button"
            className="c-btn c-btn--secondary c-btn--sm"
            disabled={!!busy}
            onClick={() => onAct('resume')}
          >
            {busy === 'resume' ? '恢复中…' : '恢复'}
          </button>
        )}
        <button
          type="button"
          className="c-btn c-btn--secondary c-btn--sm"
          disabled={!!busy}
          onClick={() => onAct('run')}
        >
          {busy === 'run' ? '运行中…' : '立即运行'}
        </button>
        <button
          type="button"
          className="c-btn c-btn--danger c-btn--sm"
          disabled={!!busy}
          onClick={() => onAct('delete')}
        >
          {busy === 'delete' ? '删除中…' : '删除'}
        </button>
      </div>
    </div>
  )
}
