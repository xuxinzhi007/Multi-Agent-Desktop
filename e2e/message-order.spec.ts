import { expect, mockRuntime, test, type Page } from './mock-runtime'

/**
 * 消息按时间归并 + 插话乐观可见（用户复盘案例 4 / 5，§4 前两条）。
 *
 * 口径：
 *  - 发送时刻立刻盖章；列表按 ts 稳定排序，同 ts 保持到达顺序；
 *  - 服务端自带 timestamp 以服务端为准，本地新发用发送时刻；
 *  - busy 下插话发送即可见，位置按发送时刻，不等整轮结束、不重复插。
 */

function agent(over: Record<string, unknown>) {
  return {
    id: 'a1',
    name: '前端',
    role: 'React',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1,
    messages: [],
    queueLen: 0,
    ...over,
  }
}

async function openChat(page: Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()
  await page.waitForTimeout(150)
}

/** 对话流里每条气泡的正文（去掉时钟等尾巴，只保留可比对的片段）。 */
async function transcriptTexts(page: Page): Promise<string[]> {
  return page.$$eval('.c-transcript .c-msg', (els) => els.map((e) => (e.textContent || '').trim()))
}

function indexOf(texts: string[], snippet: string): number {
  return texts.findIndex((t) => t.includes(snippet))
}

async function emit(page: Page, name: string, data: unknown) {
  await page.evaluate(
    ([n, d]) => {
      const mygo = (window as unknown as { mygo: { emit: (k: string, v: unknown) => void } }).mygo
      mygo.emit(n, d)
    },
    [name, data],
  )
}

test('补拉进来的更早历史排在新消息前面', async ({ page }) => {
  await mockRuntime(page, { agents: [agent({})], channels: [], notes: [] })
  await openChat(page)

  const ta = page.locator('textarea').first()
  await ta.fill('A 新问题')
  await ta.press('Enter')
  await expect(page.getByText('A 新问题').first()).toBeVisible({ timeout: 2000 })

  // 模拟补拉到一条更早的历史消息（服务端时间戳更早）。
  await emit(page, 'agent.message', {
    id: 'a1',
    message: { role: 'user', content: 'B 更早历史', timestamp: 1000000 },
  })
  await page.waitForTimeout(200)

  const texts = await transcriptTexts(page)
  const iB = indexOf(texts, 'B 更早历史')
  const iA = indexOf(texts, 'A 新问题')
  expect(iB, '历史消息没进列表').toBeGreaterThanOrEqual(0)
  expect(iA, '新消息没进列表').toBeGreaterThanOrEqual(0)
  expect(iB, '更早的历史被排在新消息后面').toBeLessThan(iA)
})

test('busy 时插话立即可见，落在原任务之后、答案之前，且不重复', async ({ page }) => {
  await mockRuntime(
    page,
    {
      agents: [
        agent({
          status: 'running',
          messages: [{ role: 'user', content: 'A 长任务', timestamp: 1000000 }],
        }),
      ],
      channels: [],
      notes: [],
    },
  )
  await openChat(page)
  await expect(page.getByText('A 长任务').first()).toBeVisible()

  const ta = page.locator('textarea').first()
  await ta.fill('C 中途补充')
  await ta.press('Enter')

  // 不等答案：插话必须立刻出现在列表里。
  await expect(page.getByText('C 中途补充').first()).toBeVisible({ timeout: 2000 })
  const cCount = await page.getByText('C 中途补充', { exact: false }).count()
  expect(cCount, '插话被重复插入了一份').toBeLessThanOrEqual(1)

  // 答案返回后，顺序必须是 A → C → 答案，不能把 C 排到答案后面。
  // 时间戳在发出答案前现场取，避免用模块加载时的旧基准盖过发送时刻。
  const answerTs = Math.floor(Date.now() / 1000) + 5
  await emit(page, 'agent.message', {
    id: 'a1',
    message: { role: 'assistant', content: '答案来了', timestamp: answerTs },
  })
  await page.waitForTimeout(200)

  const texts = await transcriptTexts(page)
  const iA = indexOf(texts, 'A 长任务')
  const iC = indexOf(texts, 'C 中途补充')
  const iAns = indexOf(texts, '答案来了')
  expect(iA).toBeGreaterThanOrEqual(0)
  expect(iC).toBeGreaterThanOrEqual(0)
  expect(iAns).toBeGreaterThanOrEqual(0)
  expect(iC, '插话排在了原任务前面').toBeGreaterThan(iA)
  expect(iAns, '插话被排到了答案后面').toBeGreaterThan(iC)
})

test('切回旧会话，旧消息顺序不被新消息顶乱', async ({ page }) => {
  await mockRuntime(
    page,
    {
      agents: [
        agent({
          id: 'a1',
          name: '旧案',
          messages: [
            { role: 'user', content: '旧一', timestamp: 1000 },
            { role: 'assistant', content: '旧二回复', timestamp: 2000 },
          ],
        }),
        agent({ id: 'a2', name: '新案', messages: [] }),
      ],
      channels: [],
      notes: [],
    },
  )
  await openChat(page)

  // 在新会话里发一条新消息
  await page.getByRole('button', { name: '会话 新案', exact: true }).click()
  await page.waitForTimeout(150)
  const ta = page.locator('textarea').first()
  await ta.fill('新会话消息')
  await ta.press('Enter')
  await expect(page.getByText('新会话消息').first()).toBeVisible({ timeout: 2000 })

  // 切回旧会话：旧一必须在旧二回复前面。
  await page.getByRole('button', { name: '会话 旧案', exact: true }).click()
  await page.waitForTimeout(200)
  const oldTexts = await transcriptTexts(page)
  const i1 = indexOf(oldTexts, '旧一')
  const i2 = indexOf(oldTexts, '旧二回复')
  expect(i1).toBeGreaterThanOrEqual(0)
  expect(i2).toBeGreaterThanOrEqual(0)
  expect(i1, '旧会话消息顺序被打乱').toBeLessThan(i2)

  // 切回新会话，新消息还在。
  await page.getByRole('button', { name: '会话 新案', exact: true }).click()
  await page.waitForTimeout(150)
  await expect(page.getByText('新会话消息').first()).toBeVisible()
})

test('同一时间戳多条消息保持到达顺序（稳定排序）', async ({ page }) => {
  await mockRuntime(page, { agents: [agent({})], channels: [], notes: [] })
  await openChat(page)

  await emit(page, 'agent.message', {
    id: 'a1',
    message: { role: 'user', content: '同秒甲', timestamp: 500 },
  })
  await emit(page, 'agent.message', {
    id: 'a1',
    message: { role: 'user', content: '同秒乙', timestamp: 500 },
  })
  await page.waitForTimeout(200)

  const texts = await transcriptTexts(page)
  const iA = indexOf(texts, '同秒甲')
  const iB = indexOf(texts, '同秒乙')
  expect(iA).toBeGreaterThanOrEqual(0)
  expect(iB).toBeGreaterThanOrEqual(0)
  expect(iA, '同 ts 消息到达顺序被排序打乱').toBeLessThan(iB)
})

test('频道消息按 at 归并排序', async ({ page }) => {
  await mockRuntime(page, {
    agents: [agent({})],
    channels: [
      {
        id: 'ch1',
        name: '评审群',
        members: ['a1'],
        messages: [
          { id: 'm2', channelId: 'ch1', fromId: 'user', fromName: '你', text: '乙 at 2', at: 2 },
          { id: 'm1', channelId: 'ch1', fromId: 'user', fromName: '你', text: '甲 at 1', at: 1 },
          { id: 'm3', channelId: 'ch1', fromId: 'user', fromName: '你', text: '丙 at 3', at: 3 },
        ],
      },
    ],
    notes: [],
  })
  await openChat(page)

  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(300)

  const texts = await page.$$eval('.c-transcript > div', (els) =>
    els.map((e) => (e.textContent || '').trim()),
  )
  const i1 = texts.findIndex((t) => t.includes('甲 at 1'))
  const i2 = texts.findIndex((t) => t.includes('乙 at 2'))
  const i3 = texts.findIndex((t) => t.includes('丙 at 3'))
  expect(i1).toBeGreaterThanOrEqual(0)
  expect(i2).toBeGreaterThanOrEqual(0)
  expect(i3).toBeGreaterThanOrEqual(0)
  expect(i1).toBeLessThan(i2)
  expect(i2).toBeLessThan(i3)
})
