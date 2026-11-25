import { test as base, type Page } from '@playwright/test'

/**
 * 在浏览器里 mock MyGo runtime。
 *
 * 真实环境由 MyGo 窗口注入 globalThis.mygo；浏览器里没有，
 * 不 mock 的话 useBackend 的 API.* 全部 reject，UI 渲染不出来。
 *
 * 只 mock UI 结构测试需要的方法，返回值都是空数据。
 */

export type GatewayMockAgent = {
  id: string
  name: string
  status: string
  queueLen: number
  lastError?: string
}

export type GatewayMockApproval = {
  id: string
  agentId: string
  agentName: string
  tool: string
  title: string
  detail: string
  diff?: string
  createdAt: number
}

export type GatewayMockBinding = {
  channel: string
  externalChatId: string
  agentId: string
  policy: string
  createdAt: number
}

export type GatewayMock = {
  /** ok：运行中；listenError：端口占用降级；disabled：gw 未启用。 */
  mode?: 'ok' | 'listenError' | 'disabled'
  agents?: GatewayMockAgent[]
  approvals?: GatewayMockApproval[]
  bindings?: GatewayMockBinding[]
}

type MockState = {
  agents: unknown[]
  channels: unknown[]
  notes: unknown[]
  feishu?: unknown[]
  /** 非空时 SaveFeishu 以该文案 reject，用于验证后端错误原文展示。 */
  feishuError?: string
  gateway?: GatewayMock
}

const EMPTY_STATE: MockState = { agents: [], channels: [], notes: [], feishu: [] }

/** 每个方法调用记录，测试可以断言。 */
export type CallLog = { method: string; args: unknown[] }

export async function mockRuntime(page: Page, state: MockState = EMPTY_STATE) {
  await page.addInitScript((initial) => {
    const listeners = new Map<string, ((data: unknown) => void)[]>()
    const calls: { method: string; args: unknown[] }[] = []
    ;(window as unknown as Record<string, unknown>).__mockCalls = calls

    // 网关 mock 状态：决定审批后从本地副本摘除，模拟后端队列收缩。
    const gwConfig = initial.gateway ?? { mode: 'disabled' }
    const gwAgents = [...(gwConfig.agents ?? [])]
    const gwApprovals = [...(gwConfig.approvals ?? [])]
    const gwBindings = [...(gwConfig.bindings ?? [])]

    const handlers: Record<string, (...args: unknown[]) => unknown> = {
      'API.ListAgents': () => initial.agents,
      // 会话历史按需加载：页面先拿壳，打开某个 agent 时才取它自己的消息。
      'API.AgentMessages': (id) =>
        (initial.agents as { id?: string; messages?: unknown[] }[]).find((a) => a.id === id)?.messages ?? [],
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
        { model: 'qwen2.5-0.5b-instruct-q4_k_m.gguf', provider: 'llamacpp', local: true, ready: true, current: true, reasoning: false },
        { model: 'deepseek-chat', provider: 'deepseek', local: false, ready: true, current: false, reasoning: true, efforts: ['low', 'medium', 'high', 'xhigh'] },
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
      'API.ListFeishuRooms': () => initial.feishu ?? [],
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
        if (initial.feishuError) return Promise.reject(new Error(initial.feishuError))
        const in_ = input as {
          id?: string
          name?: string
          enabled?: boolean
          appId?: string
          appSecret?: string
          agentId?: string
          mentionOnly?: boolean
          ownerOpenId?: string
          approverOpenIds?: string[]
        }
        return {
          id: in_.id || 'bot_test',
          name: in_.name ?? '',
          enabled: !!in_.enabled,
          appId: in_.appId ?? '',
          hasSecret: !!in_.appSecret,
          hasUser: false,
          agentId: in_.agentId ?? '',
          mentionOnly: !!in_.mentionOnly,
          ownerOpenId: in_.ownerOpenId ?? '',
          approverOpenIds: in_.approverOpenIds ?? [],
          status: in_.enabled ? 'connecting' : 'stopped',
        }
      },
      'API.SetWorkDir': () => undefined,
      'API.SetAgentExtraDirs': (id: unknown, dirs: unknown) => ({ id, extraDirs: dirs ?? [], messages: [] }),
      'API.PickDirectory': () => '/tmp/e2e-project',
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
      'API.PauseAll': () => 2,
      'API.ResumeAll': () => 0,
      'API.StopAllAgents': () => 2,
      'API.ClearAgentLLM': (id: unknown) => ({ id, messages: [] }),
      'API.SetAgentLLM': (id: unknown) => ({ id, messages: [] }),
      'API.CreateSupervisor': () => ({
        id: 'sup-1',
        name: 'Supervisor',
        role: '任务编排者',
        color: '#c084fc',
        status: 'idle',
        createdAt: Date.now() / 1000,
        messages: [],
        queueLen: 0,
        backend: 'supervisor',
        workDir: '/tmp/e2e-project',
      }),
      'API.SetAgentRuntime': (req) => {
        const r = req as {
          agentId?: string
          runtime?: string
          name?: string
          command?: string
          args?: string[]
          env?: string[]
        }
        return {
          id: r.agentId ?? 'agent-test-1',
          name: 'test',
          role: '',
          color: '#5b9dff',
          status: 'idle',
          createdAt: Date.now() / 1000,
          messages: [],
          queueLen: 0,
          backend: 'loop',
          workDir: '',
          runtime: r.runtime ?? 'native',
          acp:
            r.runtime === 'acp'
              ? { name: r.name ?? '', command: r.command ?? '', args: r.args ?? [], env: r.env ?? [] }
              : undefined,
          runtimeState: 'idle',
          runtimeError: '',
        }
      },
      'API.SetAgentWorktree': (id: unknown, on: unknown) => ({
        id,
        messages: [],
        worktree: on ? '/tmp/e2e-project/.mad-worktrees/demo' : '',
        worktreeBranch: on ? 'mad/demo' : '',
      }),
      'API.ProvidersStatus': () => [],
      // 审批：启动时补状态返回空队列；点击决定后记录调用即可。
      'API.ListSkills': () => [
        { id: '排查慢SQL', name: '排查慢SQL', description: '生产慢 SQL 的定位顺序', updatedAt: 1 },
      ],
      'API.ReadSkill': (id: unknown) => ({
        id: String(id ?? ''),
        name: '排查慢SQL',
        description: '生产慢 SQL 的定位顺序',
        body: '1. 先按租户收窄\n2. 再看索引命中',
        updatedAt: 1,
      }),
      'API.SaveSkill': (name: unknown, description: unknown, body: unknown) => ({
        id: String(name ?? ''),
        name: String(name ?? ''),
        description: String(description ?? ''),
        body: String(body ?? ''),
        updatedAt: 2,
      }),
      'API.SkillDir': () => '/tmp/skills',
      'API.AgentCapabilities': () => [
        { name: 'bash', kind: '工具', detail: '在工作目录执行命令', ready: true },
        { name: 'web', kind: '工具', detail: '操作内置浏览器', ready: false, why: '内置浏览器还没打开' },
        { name: '排查慢SQL', kind: '技能', detail: '生产慢 SQL 的定位顺序', ready: true },
      ],
      'API.AgentPermission': () => 'ask',
      'API.SetAgentPermission': (_id: unknown, mode: unknown) => String(mode ?? 'ask'),
      'API.ListApprovals': () => [],
      'API.DecideApproval': () => undefined,
      // 网关：状态 / 绑定 / 审批决定。未提供 gateway 配置时回落 disabled 空态。
      'API.GatewayStatus': () => {
        const mode = gwConfig.mode ?? 'disabled'
        if (mode === 'disabled') {
          return {
            gateway: false,
            running: false,
            addr: '',
            wired: {},
            channels: [],
            uptimeSec: 0,
            bindings: 0,
            agents: [],
            pendingApprovals: [],
            sseSubscribers: 0,
          }
        }
        const listenError =
          mode === 'listenError' ? 'listen tcp 127.0.0.1:8780: bind: address already in use' : ''
        return {
          gateway: true,
          running: mode !== 'listenError',
          addr: '127.0.0.1:8780',
          listenError,
          wired: { session: true, approval: true, sender: true, binder: true },
          channels: ['web'],
          uptimeSec: 125,
          bindings: gwBindings.length,
          agents: gwAgents,
          pendingApprovals: gwApprovals,
          sseSubscribers: 1,
        }
      },
      'API.GatewayBindings': () => gwBindings,
      'API.GatewayDecideApproval': (id: unknown) => {
        const idx = gwApprovals.findIndex((a) => a.id === id)
        if (idx >= 0) gwApprovals.splice(idx, 1)
        return undefined
      },
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
