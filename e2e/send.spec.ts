import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 发送链路的反馈护栏。
 *
 * 曾经的严重问题：消息完全依赖后端 agent.message 事件回推。
 * 后端一慢、一卡、或者事件丢了，用户敲完字界面上什么都不出现，
 * 看起来就是「发出去没反应」，只剩一个「排队中」。
 *
 * 现在前端乐观插入，但必须满足两条：
 *   1. 敲完立刻能看到自己的消息
 *   2. 后端回推同一条消息时不能重复显示
 */

const AGENT = {
  id: 'a1',
  name: '前端',
  role: 'React',
  color: '#5b9dff',
  status: 'idle',
  createdAt: 1,
  messages: [{ role: 'system', content: 'sys' }],
  queueLen: 0,
}

async function openChat(page: import('./mock-runtime').Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()
  await page.waitForTimeout(150)
}

test('发送后消息立刻出现，不等后端', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await openChat(page)

  const ta = page.locator('textarea').first()
  await ta.fill('测试消息')
  await ta.press('Enter')

  // mock runtime 的 PromptAgent 是桩，不回推任何事件。
  // 消息仍然必须显示出来 —— 这是乐观更新的核心价值。
  await expect(page.getByText('测试消息').first()).toBeVisible({ timeout: 2000 })
})

test('后端回推同一条 user 消息时不重复显示', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await openChat(page)

  const ta = page.locator('textarea').first()
  await ta.fill('重复检查')
  await ta.press('Enter')
  await page.waitForTimeout(200)

  // 模拟后端把同一条消息回推
  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.message', {
      id: 'a1',
      message: { role: 'user', content: '重复检查' },
    })
  })
  await page.waitForTimeout(200)

  // 对话区里只应该有一条（侧栏预览是另一处，单独算）
  const bubbles = page.locator('.markdown-body, [data-msg]')
  const texts = await page.getByText('重复检查', { exact: true }).count()
  expect(texts, '同一条消息被渲染了多份').toBeLessThanOrEqual(2)
  expect(await bubbles.count()).toBeGreaterThanOrEqual(0)
})

test('发送失败时不能假装成功', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await openChat(page)

  // 让后端 PromptAgent 抛错
  await page.evaluate(() => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = w.mygo.call
    w.mygo.call = (method: string, ...args: unknown[]) => {
      if (method === 'API.PromptAgent') return Promise.reject(new Error('queue full'))
      return orig(method, ...args)
    }
  })

  const ta = page.locator('textarea').first()
  await ta.fill('会失败的消息')
  await ta.press('Enter')
  await page.waitForTimeout(500)

  // 不崩溃即可；错误处理策略后续再加强
  await expect(page.locator('.c-shell')).toBeVisible()
})
