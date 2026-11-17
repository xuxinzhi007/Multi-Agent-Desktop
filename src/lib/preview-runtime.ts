/**
 * 浏览器预览用的 MyGo runtime 替身。
 *
 * 真实环境由 MyGo 窗口注入 globalThis.mygo。直接用 `npm run dev`
 * 在浏览器打开时没有这个对象，mygo-runtime 的每个方法都会抛
 * 「runtime not found」，整个应用直接进 ErrorBoundary。
 *
 * 这个 shim 只在检测到没有 runtime 时装上，用于纯前端调样式和交互。
 * 它不追求功能完整 —— 数据是假的，写入操作直接返回成功不改状态。
 * 需要真实行为时请用 `npm run dev` 的 MyGo 窗口，而不是浏览器。
 */

type Listener = (data: unknown) => void

const listeners = new Map<string, Listener[]>()

function emit(name: string, data: unknown) {
  for (const fn of listeners.get(name) ?? []) fn(data)
}

const now = () => Date.now() / 1000

const demoAgents = [
  {
    id: 'demo-1',
    name: '前端',
    role: 'React 专家',
    color: '#5b9dff',
    status: 'running',
    createdAt: now() - 600,
    messages: [
      { role: 'user', content: '把登录表单的状态管理重构一下' },
      {
        role: 'assistant',
        content:
          '我看了一下 AuthForm，现在有 7 个独立 useState，可以合并成一个表单对象 + 一次 dispatch。',
      },
    ],
    queueLen: 0,
    backend: 'loop',
    workDir: '~/code/app',
    startedAt: Math.floor(now()) - 42,
    chars: 520,
    lastTool: 'edit_file',
  },
  {
    id: 'demo-2',
    name: '后端',
    role: 'Go 专家',
    color: '#3ecf8e',
    status: 'queued',
    createdAt: now() - 580,
    messages: [{ role: 'user', content: '等接口定义先' }],
    queueLen: 1,
    backend: 'loop',
    workDir: '~/code/api',
    blockedReason: '等待上游产出',
  },
  {
    id: 'demo-3',
    name: '测试',
    role: 'QA 专家',
    color: '#e8a33d',
    status: 'error',
    createdAt: now() - 520,
    messages: [],
    queueLen: 0,
    backend: 'loop',
    workDir: '~/code/e2e',
    lastTool: 'bash',
  },
]

const demoChannels = [
  {
    id: 'ch-demo',
    name: '登录重构',
    members: ['demo-1', 'demo-2', 'demo-3'],
    messages: [
      { role: 'user', content: '大家一起看下登录这块' },
      { role: 'assistant', content: '前端先把表单状态合并，后端等接口定下来再动' },
    ],
  },
]

const demoNotes = [
  {
    id: 'n1',
    scope: 'ch-demo',
    text: 'AuthForm 有 7 个 useState，可合并',
    author: '前端',
    at: now() - 300,
    tags: ['refactor'],
  },
  {
    id: 'n2',
    scope: '',
    text: '接口变更要同步 e2e 断言',
    author: '测试',
    at: now() - 200,
    tags: ['process'],
  },
]

const handlers: Record<string, (...args: unknown[]) => unknown> = {
  'API.ListAgents': () => demoAgents,
  'API.ListChannels': () => demoChannels,
  'API.QueryNotes': () => demoNotes,
  'API.GetLLMConfig': () => ({ provider: 'mock', baseUrl: '', apiKey: '', model: 'demo' }),
  'API.GetWorkDir': () => '~/code/app',
  'API.ProvidersStatus': () => [],
  'API.IsAlwaysOnTop': () => false,
  'API.CreateAgent': (req) => ({
    id: `demo-${Date.now()}`,
    name: (req as { name?: string })?.name ?? '新 agent',
    role: (req as { role?: string })?.role ?? '',
    color: '#5b9dff',
    status: 'idle',
    createdAt: now(),
    messages: [],
    queueLen: 0,
    backend: 'loop',
  }),
  'API.ListModelsFor': () => ['demo-model'],
  'API.ListModels': () => ['demo-model'],
  'API.ListProviderEndpoints': () => [
    { provider: 'llamacpp', baseUrl: 'http://127.0.0.1:8080', saved: false, ok: false, reason: '未保存' },
  ],
  'API.SaveProvider': (cfg: unknown) => {
    const c = cfg as { provider?: string; baseUrl?: string; apiKey?: string }
    return {
      provider: c.provider ?? '',
      baseUrl: c.baseUrl ?? '',
      saved: true,
      ok: true,
      hasKey: !!c.apiKey,
      models: ['demo-model'],
    }
  },
  'API.GetTheme': () => 'dark',
  'API.GetAgentLLM': () => ({ provider: '', baseUrl: '', apiKey: '', model: '' }),
}

/** 无副作用地返回 undefined，写入类操作在预览里不真正生效。 */
function fallback(method: string): unknown {
  if (method.startsWith('API.Get') || method.startsWith('API.List')) return []
  return undefined
}

export function installPreviewRuntime() {
  const g = globalThis as { mygo?: unknown }
  if (g.mygo) return // 真实窗口里什么都不做

  g.mygo = {
    call: async (method: string, ...args: unknown[]) => {
      const h = handlers[method]
      const out = h ? h(...args) : fallback(method)
      // 让界面看起来"有响应"，但不是真实执行
      await new Promise((r) => setTimeout(r, 60))
      return out
    },
    on: (name: string, fn: Listener) => {
      const arr = listeners.get(name) ?? []
      arr.push(fn)
      listeners.set(name, arr)
      return () => {
        const cur = listeners.get(name) ?? []
        listeners.set(
          name,
          cur.filter((f) => f !== fn),
        )
      }
    },
    once: (name: string, fn: Listener) => {
      const arr = listeners.get(name) ?? []
      const wrapper = (d: unknown) => {
        const cur = listeners.get(name) ?? []
        listeners.set(name, cur.filter((f) => f !== wrapper))
        fn(d)
      }
      arr.push(wrapper)
      listeners.set(name, arr)
      return () => {
        const cur = listeners.get(name) ?? []
        listeners.set(name, cur.filter((f) => f !== wrapper))
      }
    },
    locale: 'zh',
    dir: 'ltr',
    preferences: {},
    windowActive: true,
    window: {
      minimize: async () => {},
      maximize: async () => {},
      unmaximize: async () => {},
      toggleMaximize: async () => {},
      isMaximized: async () => false,
      close: async () => {},
      setTitle: async () => {},
      setSize: async () => {},
      center: async () => {},
      show: async () => {},
      hide: async () => {},
      focus: async () => {},
      setAlwaysOnTop: async () => {},
      isAlwaysOnTop: async () => false,
      startDrag: async () => {},
      setBackdrop: async () => {},
    },
  }

  // 挂一个能产生事件的开关，方便在控制台手动触发事件调试
  ;(globalThis as { previewEmit?: typeof emit }).previewEmit = emit
  console.info(
    '[preview] 浏览器预览模式：数据是假的，写入不生效。要真实行为请用 MyGo 窗口打开。',
  )
}
