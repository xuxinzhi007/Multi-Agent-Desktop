import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 胶囊岛护栏。
 *
 * 岛始终是一颗小胶囊。通信用信封的位移表达，不展开面板。
 */

const AGENTS = [
  {
    id: 'a1',
    name: 'Research',
    role: '研究',
    color: '#54D9FF',
    status: 'running',
    createdAt: 1,
    messages: [],
    queueLen: 0,
    chars: 0,
    startedAt: Math.floor(Date.now() / 1000) - 20,
    lastTool: '',
  },
  {
    id: 'a2',
    name: 'Coder',
    role: '实现',
    color: '#A78BFA',
    status: 'queued',
    createdAt: 2,
    messages: [],
    queueLen: 1,
    lastTool: '',
  },
  {
    id: 'a3',
    name: 'Review',
    role: '审查',
    color: '#43D9A3',
    status: 'error',
    createdAt: 3,
    messages: [],
    queueLen: 0,
    lastTool: '',
  },
]

function mini(over: { id: string; name: string; color: string; status: string; lastTool?: string }[]) {
  return {
    agents: over.map((a) => ({
      id: a.id,
      name: a.name,
      role: '',
      color: a.color,
      status: a.status,
      progress: 0,
      chars: 0,
      lastTool: a.lastTool ?? '',
    })),
    paused: false,
  }
}

async function openIsland(page: import('./mock-runtime').Page, path = '/mini.html') {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto(path)
  await page.waitForSelector('.island')
}

async function emitStatus(
  page: import('./mock-runtime').Page,
  status: ReturnType<typeof mini>,
) {
  await page.evaluate((s) => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('mini.status', s)
  }, status)
}

test('默认是小胶囊，异常保持可见，且没有展开面板', async ({ page }) => {
  await openIsland(page)
  await emitStatus(
    page,
    mini([
      { id: 'a1', name: 'Research', color: '#54D9FF', status: 'running' },
      { id: 'a2', name: 'Coder', color: '#A78BFA', status: 'queued' },
      { id: 'a3', name: 'Review', color: '#43D9A3', status: 'error' },
    ]),
  )
  const island = page.locator('.island')
  await expect(island).toHaveAttribute('data-mode', 'error')
  await expect(island).toHaveAttribute('aria-label', /异常/)
  const box = await island.boundingBox()
  expect(box?.width ?? 999).toBeLessThanOrEqual(252)
  expect(box?.height ?? 999).toBeLessThanOrEqual(52)
  await expect(page.locator('.island__panel, .card')).toHaveCount(0)
  await expect(page.locator('.dot').first()).toBeVisible()
})

test('点击核心回到主窗口', async ({ page }) => {
  await openIsland(page)
  const calls: string[] = []
  await page.exposeFunction('__rec', (m: string) => calls.push(m))
  await page.evaluate(() => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = w.mygo.call
    w.mygo.call = (method: string, ...args: unknown[]) => {
      void (window as unknown as { __rec: (m: string) => void }).__rec(`${method}:${String(args[0])}`)
      return orig(method, ...args)
    }
  })
  await page.getByRole('button', { name: '回到主窗口' }).click()
  expect(calls.some((c) => c.includes('ToggleMiniWindow:false'))).toBe(true)
})

test('ask_agent 的状态变化让信封真实移动', async ({ page }) => {
  await openIsland(page)
  await emitStatus(
    page,
    mini([
      { id: 'a1', name: 'Research', color: '#54D9FF', status: 'running', lastTool: '' },
      { id: 'a2', name: 'Coder', color: '#A78BFA', status: 'running', lastTool: '' },
    ]),
  )
  await expect(page.locator('.island')).toHaveAttribute('data-mode', 'collaborating')
  await emitStatus(
    page,
    mini([
      { id: 'a1', name: 'Research', color: '#54D9FF', status: 'running', lastTool: 'ask_agent Coder' },
      { id: 'a2', name: 'Coder', color: '#A78BFA', status: 'running', lastTool: '' },
    ]),
  )
  const env = page.locator('.envelope')
  await expect(env).toBeVisible()
  const x1 = (await env.boundingBox())?.x ?? 0
  await page.waitForTimeout(280)
  const x2 = (await env.boundingBox())?.x ?? 0
  expect(Math.abs(x2 - x1)).toBeGreaterThan(4)
})

test('频道投递也会走出信封', async ({ page }) => {
  await openIsland(page)
  await emitStatus(
    page,
    mini([
      { id: 'a1', name: 'Research', color: '#54D9FF', status: 'running' },
      { id: 'a2', name: 'Coder', color: '#A78BFA', status: 'idle' },
    ]),
  )
  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('channel.message', {
      id: 'm1',
      channelId: 'c1',
      fromId: 'a1',
      fromName: 'Research',
      text: '接口定了',
      at: 1,
      deliveries: [{ agentId: 'a2', status: 'delivered' }],
    })
  })
  await expect(page.locator('.island')).toHaveAttribute('data-mode', 'communicating')
  await expect(page.locator('.envelope')).toBeVisible()
})

test('演示条可以切六态、暂停，并调节协作人数', async ({ page }) => {
  await openIsland(page, '/mini.html?demo=1')
  const island = page.locator('.island')
  await expect(page.locator('.demo')).toBeVisible()
  await expect(island).toHaveAttribute('data-mode', 'idle')

  await page.getByRole('button', { name: '思考' }).click()
  await expect(island).toHaveAttribute('data-mode', 'thinking')

  await page.getByRole('button', { name: '通信' }).click()
  await expect(island).toHaveAttribute('data-mode', 'communicating')
  await expect(page.locator('.envelope')).toBeVisible()

  await page.getByRole('button', { name: '协作' }).click()
  await expect(island).toHaveAttribute('data-mode', 'collaborating')
  expect(await page.locator('.dot').count()).toBe(3)

  await page.getByLabel('Agent 数量').fill('4')
  await expect(island).toHaveAttribute('data-mode', 'collaborating')
  expect(await page.locator('.dot').count()).toBe(4)

  await page.getByRole('button', { name: '暂停' }).click()
  const play = await island.evaluate((el) => getComputedStyle(el).animationPlayState)
  expect(play === 'paused' || (await page.locator('.core__heart').evaluate((el) => getComputedStyle(el).animationPlayState)) === 'paused').toBe(true)

  await page.getByRole('button', { name: '完成' }).click()
  await expect(island).toHaveAttribute('data-mode', 'completed')
  await expect(page.locator('.mark')).toBeVisible()

  await page.getByRole('button', { name: '异常' }).click()
  await expect(island).toHaveAttribute('data-mode', 'error')
  await expect(page.locator('.mark--err')).toBeVisible()

  await page.getByRole('button', { name: '空闲' }).click()
  await expect(island).toHaveAttribute('data-mode', 'idle')
  await expect.poll(async () => Math.round((await island.boundingBox())?.width ?? 0)).toBeLessThanOrEqual(184)
  expect(await page.locator('.dot').count()).toBe(0)
})

test('减少动态效果时不跑信封动画', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await openIsland(page, '/mini.html?demo=1')
  await page.getByRole('button', { name: '通信' }).click()
  await expect(page.locator('.envelope')).toHaveCount(0)
  await expect(page.locator('.island')).toHaveAttribute('data-mode', /communicating|collaborating/)
})
