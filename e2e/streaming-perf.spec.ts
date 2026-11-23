import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 流式渲染性能护栏。
 *
 * agent.delta 每个 token 触发一次 setState，若没有组件隔离，
 * 每次 delta 都会让整棵组件树重渲染 —— 用户看到的是打字卡顿。
 *
 * 这里直接量「连续 delta 期间主线程被占用了多久」：
 * 用 requestAnimationFrame 统计掉帧，比数 DOM 更贴近用户感受。
 */

const AGENTS = Array.from({ length: 8 }, (_, i) => ({
  id: `a${i}`,
  name: `Agent ${i}`,
  role: 'r',
  color: '#5b9dff',
  status: 'idle',
  createdAt: i + 1,
  messages: Array.from({ length: 20 }, (_, j) => ({
    role: j % 2 === 0 ? 'user' : 'assistant',
    content: `历史消息 ${j}：这是一段用来撑大列表的占位内容。`,
  })),
  queueLen: 0,
}))

test('高频 delta 下界面不应掉帧', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.waitForTimeout(300)

  // 统计 2 秒内的帧间隔，记录最长一帧
  const result = await page.evaluate(async () => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo

    // 先热一轮：首帧要编译样式、建列表，那一两个长任务是启动成本，不是打字卡顿
    mygo.emit('agent.status', { id: 'a0', status: 'running' })
    const warmUntil = performance.now() + 500
    while (performance.now() < warmUntil) {
      mygo.emit('agent.delta', { id: 'a0', delta: '预' })
      await new Promise((r) => setTimeout(r, 16))
    }

    const gaps: number[] = []
    let last = performance.now()
    let running = true
    const tick = () => {
      const now = performance.now()
      gaps.push(now - last)
      last = now
      if (running) requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)

    // 以真实模型的节奏推 delta：每 16ms 一个 token，持续 1.5s
    const deadline = performance.now() + 1500
    while (performance.now() < deadline) {
      mygo.emit('agent.delta', { id: 'a0', delta: '流' })
      await new Promise((r) => setTimeout(r, 16))
    }
    running = false

    gaps.sort((x, y) => y - x)
    return {
      samples: gaps.length,
      worst: Math.round(gaps[0] ?? 0),
      p95: Math.round(gaps[Math.floor(gaps.length * 0.05)] ?? 0),
    }
  })

  // 60fps 的预算是 16.7ms。修 Bubble memo 前最差帧是 62ms（明显卡顿），
  // 修后稳定在 18ms 左右。这里把阈值设在 40ms —— 留足 CI 抖动余量，
  // 但能挡住「整棵树重渲染」这类回归。
  // 注意：本机跑着本地模型服务（llama-server 等重负载）时，
  // 帧间隔会被系统调度拖到 50ms+ 而没有代码问题。
  // 判定回归前先确认机器是空的，否则会把 CPU 竞争误诊成性能回归。
  expect(result.worst, `最差帧 ${result.worst}ms（样本 ${result.samples}）`).toBeLessThan(40)
  expect(result.p95, `p95 帧间隔 ${result.p95}ms`).toBeLessThan(30)
})
