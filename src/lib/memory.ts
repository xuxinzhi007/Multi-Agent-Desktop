import { useCallback, useEffect, useState } from 'react'
import { call, isMyGo } from 'mygo-runtime'
import {
  API,
  type MemoryItemView,
  type MemoryNoteView,
  type MemoryStatusView,
} from '../mygo'

/**
 * 记忆 IPC 薄适配层（M1 / internal/memory）。
 *
 * 契约（api_memory.go / 生成物 src/mygo.ts）：
 * - `API.MemoryStatus` → 总览视图（available / path / 计数 / 大小 / 最近写入 / error）
 * - `API.MemorySearch(query, agentId, limit)` → 命中的事件 / 笔记 / 事实
 * - `API.MemoryRecent(limit)` → 最近写入的记忆
 * - `API.MemoryNotes(limit)` → 手写笔记列表
 * - `API.MemoryUpsertNote(title, body, tags)` → 笔记 id
 * - `API.MemoryDeleteNote(id)` → 删除，不存在返回可读错误
 *
 * 视图类型直接复用生成物；运行时若旧二进制还没挂上 typed 方法，回退到按
 * Go 方法名裸 `call` 路由。`src/mygo.ts` 是生成物，本文件不手改它。
 *
 * 注意：记忆（跨会话自动沉淀 + 手写笔记）与知识库（人工整理、挂载到会话的资料）
 * 是两套数据，本层不读写知识库 note.* 接口。
 */

export type { MemoryItemView, MemoryNoteView, MemoryStatusView }

/** mem==nil / 运行时缺失时的保底空态：切片不返回 null。 */
export const MEMORY_DISABLED: MemoryStatusView = {
  available: false,
  path: '',
  dbBytes: 0,
  events: 0,
  notes: 0,
  facts: 0,
  lastWriteAt: 0,
  error: '',
}

function str(v: unknown): string {
  return v == null ? '' : String(v)
}

function num(v: unknown): number {
  const n = Number(v ?? 0)
  return Number.isFinite(n) ? n : 0
}

/** 后端 tags 是逗号分隔字符串；展示时拆成标签数组，空串回落空数组。 */
export function splitTags(tags: string | undefined | null): string[] {
  return str(tags)
    .split(/[,，]/)
    .map((t) => t.trim())
    .filter(Boolean)
}

function normalizeStatus(raw: unknown): MemoryStatusView {
  const r = (raw ?? {}) as Partial<MemoryStatusView>
  return {
    available: !!r.available,
    path: str(r.path),
    dbBytes: num(r.dbBytes),
    events: num(r.events),
    notes: num(r.notes),
    facts: num(r.facts),
    lastWriteAt: num(r.lastWriteAt),
    error: str(r.error),
  }
}

function normalizeItem(raw: unknown): MemoryItemView {
  const r = (raw ?? {}) as Partial<MemoryItemView>
  return {
    id: num(r.id),
    kind: str(r.kind) || 'message',
    source: str(r.source),
    agentId: str(r.agentId),
    title: str(r.title),
    snippet: str(r.snippet),
    createdAt: num(r.createdAt),
  }
}

function normalizeNote(raw: unknown): MemoryNoteView {
  const r = (raw ?? {}) as Partial<MemoryNoteView>
  return {
    id: num(r.id),
    title: str(r.title),
    body: str(r.body),
    tags: str(r.tags),
    source: str(r.source),
    createdAt: num(r.createdAt),
    updatedAt: num(r.updatedAt),
  }
}

function typed(method: string): ((...args: unknown[]) => Promise<unknown>) | undefined {
  const api = API as unknown as Record<string, unknown>
  const f = api[method]
  return typeof f === 'function' ? (f as (...args: unknown[]) => Promise<unknown>) : undefined
}

async function statusCall(): Promise<MemoryStatusView> {
  const t = typed('memoryStatus')
  if (t) return normalizeStatus(await t())
  return normalizeStatus(await call<unknown>('API.MemoryStatus'))
}

async function recentCall(limit: number): Promise<MemoryItemView[]> {
  const t = typed('memoryRecent')
  const raw = t ? await t(limit) : await call<unknown[]>('API.MemoryRecent', limit)
  return (Array.isArray(raw) ? raw : []).map(normalizeItem)
}

async function notesCall(limit: number): Promise<MemoryNoteView[]> {
  const t = typed('memoryNotes')
  const raw = t ? await t(limit) : await call<unknown[]>('API.MemoryNotes', limit)
  return (Array.isArray(raw) ? raw : []).map(normalizeNote)
}

/** 搜索记忆；空 query 由后端回落最近事件。错误原文透传给界面。 */
export async function searchMemory(
  query: string,
  agentId: string,
  limit: number,
): Promise<MemoryItemView[]> {
  if (!isMyGo()) throw new Error('记忆未启用：当前不在桌面运行时内')
  const t = typed('memorySearch')
  const raw = t
    ? await t(query, agentId, limit)
    : await call<unknown[]>('API.MemorySearch', query, agentId, limit)
  return (Array.isArray(raw) ? raw : []).map(normalizeItem)
}

export async function fetchMemoryRecent(limit: number): Promise<MemoryItemView[]> {
  return recentCall(limit)
}

export async function fetchMemoryNotes(limit: number): Promise<MemoryNoteView[]> {
  return notesCall(limit)
}

/** 新增 / 更新笔记；tags 为逗号分隔字符串，后端按 (source,title) 去重。返回笔记 id。 */
export async function upsertMemoryNote(title: string, body: string, tags: string): Promise<number> {
  if (!isMyGo()) throw new Error('记忆未启用：当前不在桌面运行时内')
  const t = typed('memoryUpsertNote')
  const raw = t ? await t(title, body, tags) : await call<unknown>('API.MemoryUpsertNote', title, body, tags)
  return num(raw)
}

export async function deleteMemoryNote(id: number): Promise<void> {
  if (!isMyGo()) throw new Error('记忆未启用：当前不在桌面运行时内')
  const t = typed('memoryDeleteNote')
  if (t) {
    await t(id)
    return
  }
  await call('API.MemoryDeleteNote', id)
}

export type MemoryStatusState = {
  status: MemoryStatusView
  loading: boolean
  /** 运行时不存在（浏览器直开）：按未启用处理，不算后端错误。 */
  unavailable: boolean
  /** 运行时存在但状态读取失败：错误原文，可重试。 */
  error: string
}

const INITIAL: MemoryStatusState = {
  status: MEMORY_DISABLED,
  loading: true,
  unavailable: false,
  error: '',
}

/**
 * 拉取记忆总览：挂载时拉一次，之后按间隔刷新；页面卸载即停。
 * 明细（最近 / 笔记）由页面在 available 后单独拉取。
 */
export function useMemoryStatus(pollMs = 5000): MemoryStatusState & { refresh: () => Promise<void> } {
  const [state, setState] = useState<MemoryStatusState>(INITIAL)

  const refresh = useCallback(async () => {
    if (!isMyGo()) {
      setState({ status: MEMORY_DISABLED, loading: false, unavailable: true, error: '' })
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

/** 字节数格式化成易读文本，只用于状态卡展示。 */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = bytes
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}
