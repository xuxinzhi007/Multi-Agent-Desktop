import { useCallback, useEffect, useState } from 'react'
import { call, isMyGo } from 'mygo-runtime'
import {
  API,
  type GatewayAgentView,
  type GatewayApprovalView,
  type GatewayBindingView,
  type GatewayStatusView,
} from '../mygo'

/**
 * 网关 IPC 薄适配层。
 *
 * 契约（api_gateway.go / 生成物 src/mygo.ts）：
 * - `API.gatewayStatus()` → 总览视图（进程状态 / wired / 渠道 / 计数 / 列表）
 * - `API.gatewayBindings()` → 渠道绑定列表
 * - `API.gatewayDecideApproval(id, allow, remember, reason)` → 审批决定，错误原文透传
 *
 * 类型直接复用生成物；运行时若旧二进制还没挂上 typed 方法，回退到按 Go 方法名
 * 裸 `call` 路由。运行时不存在（浏览器直开）时返回明确的「未启用」状态，不编数据。
 * `src/mygo.ts` 是生成物，本文件不手改它。
 */

export type { GatewayAgentView, GatewayApprovalView, GatewayBindingView, GatewayStatusView }

/** gw==nil / 运行时缺失时的保底空态：切片与 map 都不返回 null。 */
export const GATEWAY_DISABLED: GatewayStatusView = {
  gateway: false,
  running: false,
  addr: '',
  listenError: '',
  wired: {},
  channels: [],
  uptimeSec: 0,
  bindings: 0,
  agents: [],
  pendingApprovals: [],
  sseSubscribers: 0,
}

function str(v: string | undefined | null): string {
  return v ?? ''
}

function num(v: number | undefined | null): number {
  const n = Number(v ?? 0)
  return Number.isFinite(n) ? n : 0
}

function normalizeAgent(raw: GatewayAgentView | undefined): GatewayAgentView {
  const r = raw ?? ({} as Partial<GatewayAgentView>)
  return {
    id: str(r.id),
    name: str(r.name),
    status: str(r.status),
    queueLen: num(r.queueLen),
    lastError: str(r.lastError),
  }
}

function normalizeApproval(raw: GatewayApprovalView | undefined): GatewayApprovalView {
  const r = raw ?? ({} as Partial<GatewayApprovalView>)
  return {
    id: str(r.id),
    agentId: str(r.agentId),
    agentName: str(r.agentName),
    tool: str(r.tool),
    title: str(r.title),
    detail: str(r.detail),
    diff: str(r.diff),
    createdAt: num(r.createdAt),
  }
}

function normalizeBinding(raw: GatewayBindingView | undefined): GatewayBindingView {
  const r = raw ?? ({} as Partial<GatewayBindingView>)
  return {
    channel: str(r.channel),
    externalChatId: str(r.externalChatId),
    agentId: str(r.agentId),
    policy: str(r.policy),
    createdAt: num(r.createdAt),
  }
}

function normalizeStatus(raw: GatewayStatusView | null | undefined): GatewayStatusView {
  const r = raw ?? ({} as Partial<GatewayStatusView>)
  return {
    gateway: !!r.gateway,
    running: !!r.running,
    addr: str(r.addr),
    listenError: str(r.listenError),
    wired: r.wired ?? {},
    channels: r.channels ?? [],
    uptimeSec: num(r.uptimeSec),
    bindings: num(r.bindings),
    agents: (r.agents ?? []).map(normalizeAgent),
    pendingApprovals: (r.pendingApprovals ?? []).map(normalizeApproval),
    sseSubscribers: num(r.sseSubscribers),
  }
}

async function statusCall(): Promise<GatewayStatusView> {
  const api = API as unknown as Record<string, unknown>
  const typed = api.gatewayStatus
  if (typeof typed === 'function') return (typed as () => Promise<GatewayStatusView>)()
  return call<GatewayStatusView>('API.GatewayStatus')
}

async function bindingsCall(): Promise<GatewayBindingView[]> {
  const api = API as unknown as Record<string, unknown>
  const typed = api.gatewayBindings
  if (typeof typed === 'function') {
    return (typed as () => Promise<GatewayBindingView[]>)()
  }
  const raw = await call<GatewayBindingView[]>('API.GatewayBindings')
  return (raw ?? []).map(normalizeBinding)
}

export async function fetchGatewayBindings(): Promise<GatewayBindingView[]> {
  const list = await bindingsCall()
  return list.map(normalizeBinding)
}

/** 转发一次网关侧审批决定；后端错误原文原样抛出，界面只展示不伪装成功。 */
export async function decideGatewayApproval(
  id: string,
  allow: boolean,
  remember: boolean,
  reason: string,
): Promise<void> {
  if (!isMyGo()) throw new Error('网关未启用：当前不在桌面运行时内')
  const api = API as unknown as Record<string, unknown>
  const typed = api.gatewayDecideApproval
  if (typeof typed === 'function') {
    await (typed as (a: string, b: boolean, c: boolean, d: string) => Promise<void>)(
      id,
      allow,
      remember,
      reason,
    )
    return
  }
  await call('API.GatewayDecideApproval', id, allow, remember, reason)
}

export type GatewayStatusState = {
  status: GatewayStatusView
  loading: boolean
  /** 运行时不存在（浏览器直开）：按未启用处理，不算后端错误。 */
  unavailable: boolean
  /** 运行时存在但状态读取失败：错误原文，可重试。 */
  error: string
}

const INITIAL: GatewayStatusState = {
  status: GATEWAY_DISABLED,
  loading: true,
  unavailable: false,
  error: '',
}

/**
 * 轻量拉取网关总览：挂载时拉一次，之后按间隔刷新；页面卸载即停。
 * 不用每帧轮询，也不订阅网关事件流（状态变化由下一次轮询补齐）。
 */
export function useGatewayStatus(
  pollMs = 5000,
): GatewayStatusState & { refresh: () => Promise<void> } {
  const [state, setState] = useState<GatewayStatusState>(INITIAL)

  const refresh = useCallback(async () => {
    if (!isMyGo()) {
      setState({ status: GATEWAY_DISABLED, loading: false, unavailable: true, error: '' })
      return
    }
    try {
      const raw = await statusCall()
      setState({ status: normalizeStatus(raw), loading: false, unavailable: false, error: '' })
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

/** 秒数格式化成「mm:ss / h:mm:ss」，只用于展示。 */
export function formatUptime(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return '00:00'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = Math.floor(sec % 60)
  const mm = String(m).padStart(2, '0')
  const ss = String(s).padStart(2, '0')
  return h > 0 ? `${h}:${mm}:${ss}` : `${mm}:${ss}`
}
