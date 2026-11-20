import { test as base, type Page } from '@playwright/test'

/**
 * 在浏览器里 mock MyGo runtime。
 *
 * 真实环境由 MyGo 窗口注入 globalThis.mygo；浏览器里没有，
 * 不 mock 的话 useBackend 的 API.* 全部 reject，UI 渲染不出来。
 *
 * 只 mock UI 结构测试需要的方法，返回值都是空数据。
 */

type MockState = {
  agents: unknown[]
  channels: unknown[]
  notes: unknown[]
  feishu?: unknown[]
}

const EMPTY_STATE: MockState = { agents: [], channels: [], notes: [], feishu: [] }

/** 每个方法调用记录，测试可以断言。 */
export type CallLog = { method: string; args: unknown[] }

export async function mockRuntime(page: Page, state: MockState = EMPTY_STATE) {
  await page.addInitScript((initial) => {
    const listeners = new Map<string, ((data: unknown) => void)[]>()
    const calls: { method: string; args: unknown[] }[] = []
    ;(window as unknown as Record<string, unknown>).__mockCalls = calls

    const handlers: Record<string, (...args: unknown[]) => unknown> = {
      'API.ListAgents': () => initial.agents,
      'API.ListChannels': () => initial.channels,
      'API.QueryNotes': () => initial.notes,
      'API.GetLLMConfig': () => ({
        provider: 'mock',
        baseUrl: '',
        apiKey: '',
        model: 'mock',
      }),
      'API.GetWorkDir': () => '',
      'API.IsAlwaysOnTop': () => false,
      'API.ListAllModels': () => [
        { model: 'qwen2.5-0.5b-instruct-q4_k_m.gguf', provider: 'llamacpp', local: true, ready: true, current: true },
        { model: 'deepseek-chat', provider: 'deepseek', local: false, ready: true, current: false },
        { model: 'OpenAI（未配置 Key）', provider: 'openai', local: false, ready: false, note: '未配置 Key' },
      ],
      'API.Diagnose': () => ({
        buildStamp: 'test-build',
        provider: 'mock',
        model: 'mock',
        hasKey: true,
        modelOk: true,
        modelReply: '通了',
        staleBuild: false,
        agents: [],
        loops: [],
        checkedAt: Math.floor(Date.now() / 1000),
      }),
      'API.CreateAgent': (req) => ({
        id: 'agent-test-1',
        name: (req as { name?: string })?.name ?? 'test',
        role: (req as { role?: string })?.role ?? '',
        color: '#5b9dff',
        status: 'idle',
        createdAt: Date.now() / 1000,
        messages: [],
        queueLen: 0,
        backend: 'loop',
        workDir: '',
      }),
      'API.CreateCLIAgent': (name, role, backend) => ({
        id: 'agent-test-1',
        name: name ?? 'test',
        role: role ?? '',
        color: '#5b9dff',
        status: 'idle',
        createdAt: Date.now() / 1000,
        messages: [],
        queueLen: 0,
        backend: backend ?? 'loop',
        workDir: '',
      }),
      'API.CreateChannel': () => ({
        id: 'ch-test-1',
        name: 'test',
        members: [],
        messages: [],
      }),
      'API.SetLLMConfig': (cfg) => cfg,
      'API.SettingsPath': () =>
        '/Users/me/Library/Application Support/multi-agent-desktop/config.json',
      'API.ListFeishu': () => initial.feishu ?? [],
      'API.DeleteFeishu': () => undefined,
      'API.SetFeishuEnabled': (id: unknown, enabled: unknown) => ({
        id: String(id ?? ''),
        name: '',
        enabled: !!enabled,
        appId: '',
        hasSecret: false,
        hasUser: false,
        agentId: '',
        mentionOnly: false,
        status: enabled ? 'connecting' : 'stopped',
      }),
      'API.SaveFeishu': (input) => {
        const in_ = input as { id?: string; name?: string; enabled?: boolean; appId?: string; appSecret?: string; agentId?: string; mentionOnly?: boolean }
        return {
          id: in_.id || 'bot_test',
          name: in_.name ?? '',
          enabled: !!in_.enabled,
          appId: in_.appId ?? '',
          hasSecret: !!in_.appSecret,
          hasUser: false,
          agentId: in_.agentId ?? '',
          mentionOnly: !!in_.mentionOnly,
          status: in_.enabled ? 'connecting' : 'stopped',
        }
      },
      'API.SetWorkDir': () => undefined,
      'API.ListModels': () => ['mock'],
      'API.ListProviderEndpoints': () => [
        {
          provider: 'llamacpp',
          baseUrl: 'http://127.0.0.1:8080',
          saved: true,
          ok: true,
          hasKey: false,
          models: ['qwen2.5-0.5b'],
        },
        { provider: 'qoder', saved: false, ok: false, reason: '未登录' },
        { provider: 'deepseek', saved: false, ok: false, reason: '未保存' },
        { provider: 'openai', saved: false, ok: false, reason: '未保存' },
        { provider: 'claude', saved: false, ok: false, reason: '未保存' },
      ],
      'API.SaveProvider': (cfg) => {
        const c = cfg as { provider?: string; baseUrl?: string; apiKey?: string }
        return {
          provider: c.provider ?? '',
          baseUrl: c.baseUrl ?? '',
          saved: true,
          ok: true,
          hasKey: !!c.apiKey,
          models: ['from-vendor'],
        }
      },
      'API.IslandResize': () => true,
      'API.SetScreenAssist': (on) => ({ on: !!on }),
      'API.LayoutMini': () => undefined,
      'API.AcceptScreenHint': () => undefined,
      'API.DismissScreenHint': () => undefined,
      'API.PauseAll': () => 2,
      'API.ResumeAll': () => 0,
      'API.StopAllAgents': () => 2,
      'API.ClearAgentLLM': (id: unknown) => ({ id, messages: [] }),
      'API.ProvidersStatus': () => [],
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

export type { Page } from "@playwright/test"
