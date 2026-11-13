import { test as base, type Page } from '@playwright/test'

/**
 * 在浏览器里 mock MyGo runtime。
 *
 * 真实环境由 MyGo 窗口注入 globalThis.mygo；浏览器里没有，
 * 不 mock 的话 useSidecar 的 Sidecar.* 全部 reject，UI 渲染不出来。
 *
 * 只 mock UI 结构测试需要的方法，返回值都是空数据。
 */

type MockState = {
  agents: unknown[]
  channels: unknown[]
  notes: unknown[]
}

const EMPTY_STATE: MockState = { agents: [], channels: [], notes: [] }

/** 每个方法调用记录，测试可以断言。 */
export type CallLog = { method: string; args: unknown[] }

export async function mockRuntime(page: Page, state: MockState = EMPTY_STATE) {
  await page.addInitScript((initial) => {
    const listeners = new Map<string, ((data: unknown) => void)[]>()
    const calls: { method: string; args: unknown[] }[] = []
    ;(window as unknown as Record<string, unknown>).__mockCalls = calls

    const handlers: Record<string, (...args: unknown[]) => unknown> = {
      'Sidecar.ListAgents': () => initial.agents,
      'Sidecar.ListChannels': () => initial.channels,
      'Sidecar.QueryNotes': () => initial.notes,
      'Sidecar.GetLLMConfig': () => ({
        provider: 'mock',
        baseUrl: '',
        apiKey: '',
        model: 'mock',
      }),
      'Sidecar.GetWorkDir': () => '',
      'Sidecar.IsAlwaysOnTop': () => false,
      'Sidecar.CreateAgent': (req) => ({
        id: 'agent-test-1',
        name: (req as { name?: string })?.name ?? 'test',
        role: (req as { role?: string })?.role ?? '',
        color: '#5b9dff',
        status: 'idle',
        createdAt: Date.now() / 1000,
        messages: [],
        queueLen: 0,
        backend: (req as { backend?: string })?.backend ?? 'mock',
        workDir: '',
      }),
      'Sidecar.CreateChannel': () => ({
        id: 'ch-test-1',
        name: 'test',
        members: [],
        messages: [],
      }),
      'Sidecar.SetLLMConfig': (cfg) => cfg,
      'Sidecar.SetWorkDir': () => undefined,
      'Sidecar.ListModels': () => ['mock'],
    }

    ;(window as unknown as Record<string, unknown>).mygo = {
      call: (method: string, ...args: unknown[]) => {
        calls.push({ method, args })
        const fn = handlers[method]
        return Promise.resolve(fn ? fn(...args) : undefined)
      },
      on: (name: string, listener: (data: unknown) => void) => {
        const arr = listeners.get(name) ?? []
        arr.push(listener)
        listeners.set(name, arr)
        return () => {
          const cur = listeners.get(name) ?? []
          listeners.set(
            name,
            cur.filter((l) => l !== listener),
          )
        }
      },
      once: (name: string, listener: (data: unknown) => void) => {
        const arr = listeners.get(name) ?? []
        const wrapper = (data: unknown) => {
          listener(data)
          listeners.set(
            name,
            (listeners.get(name) ?? []).filter((l) => l !== wrapper),
          )
        }
        arr.push(wrapper)
        listeners.set(name, arr)
        return () => {}
      },
      emit: (name: string, data: unknown) => {
        for (const l of listeners.get(name) ?? []) l(data)
      },
      locale: 'zh-CN',
      dir: 'ltr',
      preferences: {},
    }
  }, state)
}

/** 带 mock 的 test，beforeEach 自动注入。 */
export const test = base.extend<{ callLog: () => Promise<CallLog[]> }>({
  callLog: async ({ page }, use) => {
    await use(async () => {
      return page.evaluate(
        () => ((window as unknown as Record<string, unknown>).__mockCalls as CallLog[]) ?? [],
      )
    })
  },
})

export { expect } from '@playwright/test'
