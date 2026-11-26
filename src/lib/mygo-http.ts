/**
 * 浏览器直连桌面进程的 MyGo 替身：同一份前端，两种宿主。
 *
 * 桌面窗口里 `globalThis.mygo` 由原生宿主注入；浏览器里没有宿主，
 * 但它能连到桌面进程内嵌的网关（默认 127.0.0.1:8780）。这里装一个同签名的
 * 对象，把调用转到 `POST /api/rpc`、把事件订阅转到 `GET /api/events`（SSE）。
 *
 * 为什么不是"再写一套网页"：所有组件只认 `globalThis.mygo` 的 call/on/once，
 * 让这个对象有第二个来源，界面代码一行都不用改，也就不会出现两套页面漂移。
 *
 * 连不上就返回 false，由调用方退到「预览」——**绝不静默假装成功**。
 */

type Listener = (data: unknown) => void

const DEFAULT_BASE = 'http://127.0.0.1:8780'
const TOKEN_KEY = 'mad.gateway.token'

/** 网关地址：?api= 优先；页面本身由网关提供时用同源；否则打固定回环地址。 */
function baseURL(): string {
  if (typeof location === 'undefined') return DEFAULT_BASE
  const q = new URLSearchParams(location.search).get('api')?.trim()
  if (q) return q.replace(/\/+$/, '')
  if (location.port === '8780') return location.origin
  return DEFAULT_BASE
}

/** token：?token= 优先并记住，其次读上次存的。 */
function readToken(): string {
  if (typeof location !== 'undefined') {
    const q = new URLSearchParams(location.search).get('token')?.trim() ?? ''
    if (q) {
      try {
        localStorage.setItem(TOKEN_KEY, q)
      } catch {
        /* 隐私模式写不进去，不影响本次 */
      }
      return q
    }
  }
  try {
    return localStorage.getItem(TOKEN_KEY) ?? ''
  } catch {
    return ''
  }
}

function authHeaders(token: string): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function probe(base: string, token: string, timeoutMS = 3000): Promise<boolean> {
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), timeoutMS)
  try {
    const r = await fetch(base + '/api/status', {
      signal: ctrl.signal,
      cache: 'no-store',
      headers: authHeaders(token),
    })
    if (!r.ok) return false
    const body = (await r.json()) as { gateway?: boolean }
    return body.gateway === true
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
}

/** 事件通道：一条 SSE 连接，按事件名分发；EventSource 自己负责重连。 */
function eventBus(base: string, token: string) {
  const listeners = new Map<string, Set<Listener>>()
  const bound = new Set<string>()
  let es: EventSource | null = null

  const ensure = () => {
    if (es) return es
    // EventSource 不能带 Authorization 头，token 只能走 query；
    // 网关只对 /api/events 接受这种写法。
    const url = base + '/api/events' + (token ? `?token=${encodeURIComponent(token)}` : '')
    es = new EventSource(url)
    es.addEventListener('open', () => console.info('[web] 事件流已连接'))
    es.addEventListener('error', () => console.warn('[web] 事件流断开，浏览器自动重连'))
    for (const name of bound) {
      es.addEventListener(name, (ev) => dispatch(name, parse((ev as MessageEvent).data)))
    }
    return es
  }

  const dispatch = (name: string, data: unknown) => {
    for (const fn of listeners.get(name) ?? []) fn(data)
  }

  return {
    subscribe(name: string, fn: Listener, once = false): () => void {
      const set = listeners.get(name) ?? new Set<Listener>()
      listeners.set(name, set)
      const wrapper: Listener = once
        ? (data) => {
            set.delete(wrapper)
            fn(data)
          }
        : fn
      set.add(wrapper)
      if (!bound.has(name)) {
        bound.add(name)
        // 连接已建立时补挂监听，避免"先订阅后建连"的顺序问题。
        es?.addEventListener(name, (ev) => dispatch(name, parse((ev as MessageEvent).data)))
      }
      ensure()
      return () => {
        set.delete(wrapper)
      }
    },
    close() {
      es?.close()
      es = null
    },
  }
}

function parse(raw: string): unknown {
  try {
    return JSON.parse(raw)
  } catch {
    return raw
  }
}

/**
 * 装上 HTTP 版 runtime。返回 false 表示网关不可达（调用方应退到预览）。
 */
export async function installHttpRuntime(): Promise<boolean> {
  const base = baseURL()
  const token = readToken()
  if (!(await probe(base, token))) {
    console.warn('[web] 网关不可达：' + base + '/api/status')
    return false
  }

  const bus = eventBus(base, token)
  const g = globalThis as { mygo?: unknown }
  if (g.mygo) return true // 宿主注入的先到，优先用真宿主

  const noop = async () => {}
  const call = async (method: string, ...args: unknown[]) => {
    const r = await fetch(base + '/api/rpc', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders(token),
        // 自定义头会触发预检；网关用它区分「本应用页面」和「任意网页」。
        'X-MAD-Web': '1',
      },
      body: JSON.stringify({ method, args }),
    })
    if (!r.ok) {
      const text = await r.text()
      let msg = text
      try {
        const body = JSON.parse(text) as { error?: { message?: string } }
        msg = body.error?.message ?? text
      } catch {
        /* 保持原文 */
      }
      throw new Error(method + ' 失败：' + msg)
    }
    const body = (await r.json()) as { result?: unknown }
    return body.result
  }

  // 平台按 UA 粗判：Runtime 接口里 platform 决定个别 CSS（如 mac 的红绿灯留白），
  // 缺字段会让 mygo-runtime 的 runtime() 读到 undefined。
  const ua = typeof navigator === 'undefined' ? '' : navigator.userAgent
  const platform = /Mac/i.test(ua) ? 'macos' : /Win/i.test(ua) ? 'windows' : 'linux'

  g.mygo = {
    call,
    on: (name: string, fn: Listener) => bus.subscribe(name, fn),
    once: (name: string, fn: Listener) => bus.subscribe(name, fn, true),
    // channel() 是宿主侧的流式通道，HTTP 面没有对应语义，明确报错而不是假装能用。
    channel: () => {
      throw new Error('网页端暂不支持 channel() 流式调用')
    },
    platform,
    windowId: 0,
    version: 'web',
    locale: 'zh',
    dir: 'ltr',
    preferences: {},
    windowActive: true,
    window: {
      minimize: noop,
      maximize: noop,
      unmaximize: noop,
      toggleMaximize: noop,
      isMaximized: async () => false,
      close: noop,
      setTitle: noop,
      setSize: noop,
      center: noop,
      show: noop,
      hide: noop,
      focus: noop,
      setAlwaysOnTop: noop,
      isAlwaysOnTop: async () => false,
      startDrag: noop,
      setBackdrop: noop,
    },
  }

  document.documentElement.dataset.web = '1'
  document.title = 'Multi-Agent Desktop · Web'
  console.info('[web] 已连接桌面进程网关：' + base + '（真实数据）')
  return true
}

/** 当前是不是网页直连模式（用于界面提示，不用于数据分支）。 */
export function webMode(): boolean {
  return typeof document !== 'undefined' && document.documentElement.dataset.web === '1'
}
