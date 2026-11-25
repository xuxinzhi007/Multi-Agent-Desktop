import { useCallback, useEffect, useState } from 'react'
import { call, isMyGo } from 'mygo-runtime'
import {
  API,
  type PlanRequest,
  type PlanStatusView,
  type PlanView,
} from '../mygo'

/**
 * 调度（计划）IPC 薄适配层（M2 / internal/scheduler）。
 *
 * 契约（api_plan.go / 生成物 src/mygo.ts）：
 * - `API.PlanStatus` → 调度器总览（available / running / count / enabled / quiet / lastTickAt / error）
 * - `API.PlanList` → 任务列表
 * - `API.PlanCreate(req)` → 新建（interval / cron，绑定 agentId + prompt）
 * - `API.PlanUpdate(req)` → 按 ID 全量替换
 * - `API.PlanPause(id, paused)` → 暂停 / 恢复
 * - `API.PlanDelete(id)` → 删除
 * - `API.PlanRunNow(id)` → 立即运行（无视静默 / 限频 / 退避）
 *
 * 视图类型直接复用生成物；旧二进制未挂 typed 方法时回退裸 `call` 路由。
 * `src/mygo.ts` 不手改。
 *
 * 任务执行走现有审批：不自动批准工具；审批超时 / 无人值守按 fail-closed。
 * 本层只转发请求与后端返回的错误原文，不在前端伪造执行结果。
 */

export type { PlanRequest, PlanStatusView, PlanView }

export const PLAN_DISABLED: PlanStatusView = {
  available: false,
  running: false,
  count: 0,
  enabled: 0,
  quiet: '',
  lastTickAt: 0,
  error: '',
}

function str(v: unknown): string {
  return v == null ? '' : String(v)
}

function num(v: unknown): number {
  const n = Number(v ?? 0)
  return Number.isFinite(n) ? n : 0
}

function normalizeStatus(raw: unknown): PlanStatusView {
  const r = (raw ?? {}) as Partial<PlanStatusView>
  return {
    available: !!r.available,
    running: !!r.running,
    count: num(r.count),
    enabled: num(r.enabled),
    quiet: str(r.quiet),
    lastTickAt: num(r.lastTickAt),
    error: str(r.error),
  }
}

function normalizePlan(raw: unknown): PlanView {
  const r = (raw ?? {}) as Partial<PlanView>
  return {
    id: str(r.id),
    type: str(r.type) || 'interval',
    intervalSec: num(r.intervalSec),
    cron: str(r.cron),
    agentId: str(r.agentId),
    prompt: str(r.prompt),
    channel: str(r.channel),
    enabled: r.enabled !== false,
    createdAt: num(r.createdAt),
    lastRunAt: num(r.lastRunAt),
    nextRunAt: num(r.nextRunAt),
    failures: num(r.failures),
    lastError: str(r.lastError),
  }
}

function typed(method: string): ((...args: unknown[]) => Promise<unknown>) | undefined {
  const api = API as unknown as Record<string, unknown>
  const f = api[method]
  return typeof f === 'function' ? (f as (...args: unknown[]) => Promise<unknown>) : undefined
}

async function statusCall(): Promise<PlanStatusView> {
  const t = typed('planStatus')
  if (t) return normalizeStatus(await t())
  return normalizeStatus(await call<unknown>('API.PlanStatus'))
}

export async function fetchPlanList(): Promise<PlanView[]> {
  const t = typed('planList')
  const raw = t ? await t() : await call<unknown[]>('API.PlanList')
  return (Array.isArray(raw) ? raw : []).map(normalizePlan)
}

/** 新建任务；后端校验 interval / cron / agentId / prompt，错误原文透传。 */
export async function createPlan(req: PlanRequest): Promise<PlanView> {
  if (!isMyGo()) throw new Error('调度器未启用：当前不在桌面运行时内')
  const t = typed('planCreate')
  const raw = t ? await t(req) : await call<unknown>('API.PlanCreate', req)
  return normalizePlan(raw)
}

export async function updatePlan(req: PlanRequest): Promise<PlanView> {
  if (!isMyGo()) throw new Error('调度器未启用：当前不在桌面运行时内')
  const t = typed('planUpdate')
  const raw = t ? await t(req) : await call<unknown>('API.PlanUpdate', req)
  return normalizePlan(raw)
}

export async function pausePlan(id: string, paused: boolean): Promise<void> {
  if (!isMyGo()) throw new Error('调度器未启用：当前不在桌面运行时内')
  const t = typed('planPause')
  if (t) {
    await t(id, paused)
    return
  }
  await call('API.PlanPause', id, paused)
}

export async function deletePlan(id: string): Promise<void> {
  if (!isMyGo()) throw new Error('调度器未启用：当前不在桌面运行时内')
  const t = typed('planDelete')
  if (t) {
    await t(id)
    return
  }
  await call('API.PlanDelete', id)
}

/** 立即运行：无视静默 / 限频 / 退避，直接触发 Runner；失败计入后端 failures。 */
export async function runPlanNow(id: string): Promise<void> {
  if (!isMyGo()) throw new Error('调度器未启用：当前不在桌面运行时内')
  const t = typed('planRunNow')
  if (t) {
    await t(id)
    return
  }
  await call('API.PlanRunNow', id)
}

export type PlanStatusState = {
  status: PlanStatusView
  loading: boolean
  /** 运行时不存在（浏览器直开）：按未启用处理，不算后端错误。 */
  unavailable: boolean
  /** 运行时存在但状态读取失败：错误原文，可重试。 */
  error: string
}

const INITIAL: PlanStatusState = {
  status: PLAN_DISABLED,
  loading: true,
  unavailable: false,
  error: '',
}

/** 拉取调度器总览：挂载时拉一次，之后按间隔刷新；页面卸载即停。 */
export function usePlanStatus(pollMs = 5000): PlanStatusState & { refresh: () => Promise<void> } {
  const [state, setState] = useState<PlanStatusState>(INITIAL)

  const refresh = useCallback(async () => {
    if (!isMyGo()) {
      setState({ status: PLAN_DISABLED, loading: false, unavailable: true, error: '' })
      return
    }
    try {
      const raw = await statusCall()
      setState({ status: raw, loading: false, unavailable: false, error: '' })
    } catch (e) {
      setState((s) => ({
        ...s,
        loading: false,
        error: e instanceof Error && e.message ? e.message : String(e),
      }))
    }
  }, [])

  useEffect(() => {
    let stop = false
    void refresh()
    const timer = window.setInterval(() => {
      if (!stop) void refresh()
    }, pollMs)
    return () => {
      stop = true
      window.clearInterval(timer)
    }
  }, [refresh, pollMs])

  return { ...state, refresh }
}
