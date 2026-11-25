import { expect, mockRuntime, test } from './mock-runtime'

const tags = Array.from({ length: 30 }, (_, i) => `tag${i}`)

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      {
        id: 'a1',
        name: '前端',
        role: 'r',
        color: '#5b9dff',
        status: 'idle',
        createdAt: 1,
        messages: [],
        queueLen: 0,
        backend: 'loop',
        knowledge: ['n1'],
      },
    ],
    channels: [],
    notes: [
      {
        id: 'n1',
        title: '预算耗尽',
        body: '一次 run 的预算包含 maxWallMs。本次是墙钟 705s 超 600s，而不是迭代或工具数耗尽。',
        tags,
        at: 1,
      },
      { id: 'n2', title: '另一条', body: '短笔记', tags: ['tag0'], at: 2 },
    ],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell', { timeout: 10_000 })
})

test('知识标签变多时收成一行，笔记保持紧凑卡片', async ({ page }) => {
  await page.locator('aside').getByText('前端', { exact: true }).click()
  // 进对话时检查器已经自动展开，这里直接切页签
  await page.getByRole('button', { name: /知识/ }).click()

  const row = page.locator('.c-kb-tags')
  await expect(row).toBeVisible()
  const rowBox = await row.boundingBox()
  expect(rowBox).not.toBeNull()
  expect(rowBox!.height).toBeLessThan(36)

  await expect(page.getByRole('button', { name: '全部 30 个标签' })).toBeVisible()

  const card = page.locator('.c-compact-card').first()
  const cardBox = await card.boundingBox()
  expect(cardBox).not.toBeNull()
  expect(cardBox!.height).toBeGreaterThanOrEqual(60)
  expect(cardBox!.height).toBeLessThanOrEqual(80)
  await expect(card.locator('.c-compact-card__cap')).toHaveText('前端')

  await page.getByRole('button', { name: '全部 30 个标签' }).click()
  const drawer = page.locator('.c-kb-tags__drawer')
  await expect(drawer).toBeVisible()
  const drawerBox = await drawer.boundingBox()
  expect(drawerBox).not.toBeNull()
  expect(drawerBox!.height).toBeLessThanOrEqual(220)
  await expect(card).toBeVisible()
})
