import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 群聊_live_交互：@所有人、防已读不回的状态行、停止、消息回填、断线兜底。
 */

const AGENTS = [
  { id: 'a1', name: '前端', role: 'React', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
  { id: 'a2', name: '后端', role: 'Go', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1', 'a2'],
    policy: 'discuss',
    speaker: 'a2',
    messages: [
      {
        id: 'm1',
        channelId: 'ch1',
        fromId: 'user',
        fromName: '你',
        text: '大家看下这个方案',
        at: 100,
        deliveries: [
          { agentId: 'a1', status: 'delivered', reason: '被 @' },
          { agentId: 'a2', status: 'delivered', reason: '被 @' },
        ],
      },
    ],
  },
]

async function openChannel(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)
}

test('提及选择器里有所有人并进入点名区', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  const editor = page.getByLabel(/发消息到 #评审群/)
  await editor.click()
  await editor.pressSequentially('@')
  const item = page.getByRole('option', { name: /所有人/ })
  await expect(item).toBeVisible()
  await item.click()
  await expect(page.locator('.c-mention-chip', { hasText: '@所有人' })).toBeVisible()
})

test('已送达未开口的成员显示接入状态并可停止', async ({ page }) => {
  await mockRuntime(page, {
    agents: AGENTS.map((a) => ({ ...a, status: 'queued' })),
    channels: CHANNELS,
    notes: [],
  })
  await openChannel(page)

  // a1/a2 都排队 → 两行排队状态
  await expect(page.getByText('排队中，前面的回合结束后接入…')).toHaveCount(2)
  const stops = page.getByRole('button', { name: '停止', exact: true })
  await expect(stops).toHaveCount(2)
  await stops.first().click()
  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  expect(calls.some((c) => c.method === 'API.StopAgent' || c.method === 'API.Stop')).toBe(true)
})

test('回合结束未回复的成员不再显示接入状态', async ({ page }) => {
  // 停止/空回复后 agent 回 idle：回复永远不会来，状态行必须消失
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)
  await expect(page.getByText('排队中，前面的回合结束后接入…')).toHaveCount(0)
  await expect(page.getByText('正在接入…')).toHaveCount(0)
  await expect(page.getByRole('button', { name: '停止', exact: true })).toHaveCount(0)
})

test('running 且 busy 在本频道的成员显示正在思考中', async ({ page }) => {
  await mockRuntime(page, {
    agents: [AGENTS[0], { ...AGENTS[1], status: 'running', busy: '评审群' }],
    channels: CHANNELS,
    notes: [],
  })
  await openChannel(page)
  await expect(page.getByText('正在思考中…')).toBeVisible()
})

test('成员回复后不再显示接入状态', async ({ page }) => {
  await mockRuntime(page, {
    agents: AGENTS.map((a) => ({ ...a, status: 'queued' })),
    channels: [
      {
        ...CHANNELS[0],
        messages: [
          ...CHANNELS[0].messages,
          { id: 'm2', channelId: 'ch1', fromId: 'a1', fromName: '前端', text: '看完了', at: 200 },
          { id: 'm3', channelId: 'ch1', fromId: 'a2', fromName: '后端', text: '没问题', at: 300 },
        ],
      },
    ],
    notes: [],
  })
  await openChannel(page)
  await expect(page.getByText('排队中，前面的回合结束后接入…')).toHaveCount(0)
})

test('点自己发的消息回填输入框', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  await page.getByTitle('点击回填到输入框，修改后重发').click()
  await expect(page.getByLabel(/发消息到 #评审群/)).toHaveText('大家看下这个方案')
})

test('断线时频道显示重连横幅且发送禁用', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  // 让心跳连续失败：把 GetWorkDir 换成 reject
  await page.evaluate(() => {
    const g = globalThis as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = g.mygo.call.bind(g.mygo)
    g.mygo.call = (m: string, ...a: unknown[]) =>
      m === 'API.GetWorkDir' ? Promise.reject(new Error('offline')) : orig(m, ...a)
  })
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await expect(page.getByText('与后端的连接已断开', { exact: false })).toBeVisible({ timeout: 15000 })
  const editor = page.getByLabel(/发消息到 #评审群/)
  await editor.click()
  await editor.pressSequentially('还在吗')
  await expect(page.getByRole('button', { name: '发送' })).toBeDisabled()
})