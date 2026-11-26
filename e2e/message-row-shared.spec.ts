import { expect, mockRuntime, test, type Page } from './mock-runtime'

/**
 * 消息层统一（i）：会话视图与频道视图必须走同一套行渲染。
 *
 * 取证方式：DOM 侧稳定钩子 `data-msg-row`（行级，取值 user/agent）+ 内嵌
 * `data-msg-body`（正文容器）。两侧同类消息都必须带这对钩子，且两边正文都落在
 * 同一个消息卡片（`c-msg-card`）里——单聊、频道、飞书三处一律「一条消息一个带边框
 * 的卡片」，不再允许某一侧无边框裸排。以后若有人在某一侧再分叉出第二套行渲染
 * 或私自取掉卡片，这条用例会直接挂掉。
 */

const AGENTS = [
  {
    id: 'a1',
    name: '前端',
    role: 'r',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1,
    queueLen: 0,
    backend: 'loop',
    workDir: '/tmp',
    messages: [
      { role: 'user', content: '会话里的提问', timestamp: 100 },
      { role: 'assistant', content: '## 会话回答\n\n会话正文段落。', timestamp: 200 },
    ],
  },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1'],
    policy: 'discuss',
    speaker: 'a1',
    messages: [
      { id: 'cm1', channelId: 'ch1', fromId: 'user', fromName: '你', text: '群里的提问', at: 100 },
      { id: 'cm2', channelId: 'ch1', fromId: 'a1', fromName: '前端', text: '## 群里回答\n\n群聊正文段落。', at: 200 },
    ],
  },
]

type RowShape = { side: string; hasBody: boolean; bodyClass: string }

async function rowShapes(page: Page): Promise<RowShape[]> {
  return page.$$eval('[data-msg-row]', (els) =>
    els.map((e) => {
      const body = e.querySelector('[data-msg-body]')
      return {
        side: e.getAttribute('data-msg-row') ?? '',
        hasBody: !!body,
        bodyClass: body?.className ?? '',
      }
    }),
  )
}

async function openShell(page: Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()
  await page.waitForTimeout(200)
}

test('会话与频道走同一套消息行渲染（data-msg-row + data-msg-body）', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openShell(page)

  // 会话视图：确保打开 a1。
  const sessionTab = page.getByRole('button', { name: '会话 前端', exact: true })
  if (await sessionTab.count()) await sessionTab.click()
  await page.waitForTimeout(200)

  const sessionRows = await rowShapes(page)
  const sessionUser = sessionRows.filter((r) => r.side === 'user')
  const sessionAgent = sessionRows.filter((r) => r.side === 'agent')
  expect(sessionUser.length, '会话视图缺用户行钩子').toBeGreaterThanOrEqual(1)
  expect(sessionAgent.length, '会话视图缺助手行钩子').toBeGreaterThanOrEqual(1)
  expect(sessionUser.every((r) => r.hasBody), '会话用户行缺正文钩子').toBe(true)
  expect(sessionAgent.every((r) => r.hasBody), '会话助手行缺正文钩子').toBe(true)
  expect(sessionAgent[0].bodyClass, '会话助手正文应落在 markdown-body 容器').toContain('markdown-body')
  expect(sessionAgent[0].bodyClass, '会话助手正文必须带消息卡片边框').toContain('c-msg-card')

  // 频道视图：切到评审群。
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(300)

  const channelRows = await rowShapes(page)
  const channelUser = channelRows.filter((r) => r.side === 'user')
  const channelAgent = channelRows.filter((r) => r.side === 'agent')
  expect(channelUser.length, '频道视图缺用户行钩子').toBeGreaterThanOrEqual(1)
  expect(channelAgent.length, '频道视图缺助手行钩子').toBeGreaterThanOrEqual(1)
  expect(channelUser.every((r) => r.hasBody), '频道用户行缺正文钩子').toBe(true)
  expect(channelAgent.every((r) => r.hasBody), '频道助手行缺正文钩子').toBe(true)
  expect(channelAgent[0].bodyClass, '频道助手正文应落在 markdown-body 容器').toContain('markdown-body')
  expect(channelAgent[0].bodyClass, '频道助手正文必须带消息卡片边框').toContain('c-msg-card')

  // 两侧结构对齐：同类行都带行钩子 + 正文钩子，助手正文共用同一容器类。
  expect(channelUser[0].hasBody).toBe(sessionUser[0].hasBody)
  expect(channelAgent[0].bodyClass).toContain('markdown-body')

  await expect(page.locator('[data-msg-row="agent"] [data-msg-body]', { hasText: '群里回答' })).toBeVisible()
  await expect(page.locator('[data-msg-row="user"] [data-msg-body]', { hasText: '群里的提问' })).toBeVisible()
})
