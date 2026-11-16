import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 灵动岛（/mini）交互护栏。
 *
 * 两态设计：折叠态瞥一眼，展开态操作与详情。
 * 这些断言锁的是「信息一眼可读」和「操作后所有视觉同步」，
 * 而不是像素级还原 —— 后者改设计就会碎。
 */

const AGENTS = [
  {
    id: 'a1',
    name: '前端',
    role: 'React 专家',
    color: '#5b9dff',
    status: 'running',
    createdAt: 1,
    messages: [],
    queueLen: 0,
    chars: 400,
    startedAt: Math.floor(Date.now() / 1000) - 20,
  },
  {
    id: 'a2',
    name: '后端',
    role: 'Go 专家',
    color: '#3ecf8e',
    status: 'queued',
    createdAt: 2,
    messages: [],
    queueLen: 1,
  },
  {
    id: 'a3',
    name: '测试',
    role: 'QA 专家',
    color: '#e8a33d',
    status: 'error',
    createdAt: 3,
    messages: [],
    queueLen: 0,
  },
]

const MINI_STATUS = {
  agents: AGENTS.map((a, i) => ({
    id: a.id,
    name: a.name,
    role: a.role,
    color: a.color,
    status: a.status,
    progress: [50, 8, 45][i],
    chars: a.chars ?? 0,
    startedAt: a.startedAt ?? 0,
    task: ['重构登录表单', '等待接口定义', '跑集成测试'][i],
    dep: i === 1 ? '前端' : undefined,
    logs: [
      { at: Math.floor(Date.now() / 1000), kind: 'tool', text: 'web.search(…)', detail: '' },
      { at: Math.floor(Date.now() / 1000) - 30, kind: 'tool', text: 'npm run build' },
    ],
  })),
  paused: false,
}

async function openIsland(page: import('./mock-runtime').Page, status = MINI_STATUS) {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/mini.html')
  await page.waitForSelector('.island')
  await page.evaluate((s) => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('mini.status', s)
  }, status)
  await page.waitForTimeout(300)
}

test('折叠态一眼能看出运行/等待/异常数量', async ({ page }) => {
  await openIsland(page)
  const island = page.locator('.island')
  await expect(island).toBeVisible()

  const text = (await island.textContent()) ?? ''
  expect(text, `折叠态文本: ${text}`).toMatch(/运行/)
  expect(text, `折叠态文本: ${text}`).toMatch(/等待/)
  expect(text, `折叠态文本: ${text}`).toMatch(/异常/)

  // 头像堆叠：最多 3 个
  const avatars = page.locator('.avatar')
  const n = await avatars.count()
  expect(n).toBeGreaterThan(0)
  expect(n).toBeLessThanOrEqual(3)

  // 总进度环存在（卡片里也有环，取折叠态那个）
  await expect(page.locator('.ring').first()).toBeVisible()
})

test('点击折叠态展开为完整面板', async ({ page }) => {
  await openIsland(page)
  expect(await page.locator('.island.island--collapsed').count()).toBe(1)

  await page.locator('.island__pill').click()
  await page.waitForTimeout(600)

  expect(await page.locator('.island.island--expanded').count()).toBe(1)
  // 展开后应能看到标题、总进度条、卡片列表、底部控制
  await expect(page.getByText('Agent 协作')).toBeVisible()
  await expect(page.locator('.panel__bar')).toBeVisible()
  await expect(page.locator('.panel__foot')).toBeVisible()
})

test('展开态每张卡片显示名称、状态徽章、进度', async ({ page }) => {
  await openIsland(page)
  await page.locator('.island__pill').click()
  await page.waitForTimeout(600)

  const cards = page.locator('.card')
  expect(await cards.count()).toBeGreaterThanOrEqual(2)

  const first = cards.first()
  const text = (await first.textContent()) ?? ''
  expect(text).toMatch(/前端/)
  expect(text).toMatch(/运行中|思考中/)
  expect(text).toMatch(/\d+%/)
  await expect(first.locator('.card__fill')).toBeVisible()
})

test('等待中的 agent 标注上游依赖', async ({ page }) => {
  await openIsland(page)
  await page.locator('.island__pill').click()
  await page.waitForTimeout(600)

  await expect(page.getByText(/等待上游产出/)).toBeVisible()
})

test('点击卡片展开日志时间线', async ({ page }) => {
  await openIsland(page)
  await page.locator('.island__pill').click()
  await page.waitForTimeout(600)

  const card = page.locator('.card').first()
  await expect(card.locator('.card__logs--open')).toHaveCount(0)

  await card.locator('button').first().click()
  await page.waitForTimeout(400)

  await expect(card.locator('.card__logs--open')).toHaveCount(1)
  await expect(card.getByText(/web\.search/).first()).toBeVisible()
})

test('Esc 收起面板', async ({ page }) => {
  await openIsland(page)
  await page.locator('.island__pill').click()
  await page.waitForTimeout(500)
  expect(await page.locator('.island--expanded').count()).toBe(1)

  await page.keyboard.press('Escape')
  await page.waitForTimeout(500)
  expect(await page.locator('.island--collapsed').count()).toBe(1)
})

test('全局操作会调后端并给出 toast 反馈', async ({ page }) => {
  await openIsland(page)
  await page.locator('.island__pill').click()
  await page.waitForTimeout(600)

  const calls: string[] = []
  await page.exposeFunction('__recordCall', (m: string) => calls.push(m))
  await page.evaluate(() => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = w.mygo.call
    w.mygo.call = (method: string, ...args: unknown[]) => {
      void (window as unknown as { __recordCall: (m: string) => void }).__recordCall(method)
      return orig(method, ...args)
    }
  })

  await page.getByRole('button', { name: '全部暂停' }).click()
  await page.waitForTimeout(400)

  expect(calls.some((c) => c === 'API.PauseAll')).toBe(true)
  await expect(page.locator('.toast')).toBeVisible()
})
