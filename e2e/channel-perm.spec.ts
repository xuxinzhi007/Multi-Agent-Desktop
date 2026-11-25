import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 单聊里的频道标识 + 频道侧的权限入口。
 * 群聊回合不进单聊历史，单聊靠 ChannelStrip 标出他在哪些群干活；
 * 权限（ask/auto/readonly）在成员名单和个人主页里直接给。
 */

const AGENTS = [
  {
    id: 'a1',
    name: '前端',
    role: 'React',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1,
    messages: [],
    queueLen: 0,
    backend: 'loop',
    workDir: '/tmp',
  },
  {
    id: 'a2',
    name: '后端',
    role: 'Go',
    color: '#3ecf8e',
    status: 'idle',
    createdAt: 2,
    messages: [],
    queueLen: 0,
    backend: 'loop',
    workDir: '/tmp',
  },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1', 'a2'],
    policy: 'discuss',
    speaker: 'a2',
    messages: [
      { id: 'm1', channelId: 'ch1', fromId: 'user', fromName: '你', text: '大家看下这个方案', at: 100 },
      { id: 'm2', channelId: 'ch1', fromId: 'a1', fromName: '前端', text: '我看完了，没问题', at: 200 },
    ],
  },
]

test('单聊显示频道动态 chips 并可跳转', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 前端', exact: true }).click()

  await expect(page.getByText('频道动态', { exact: false })).toBeVisible()
  const chip = page.getByRole('button', { name: '# 评审群 · 1 条' })
  await expect(chip).toBeVisible()
  await chip.click()
  await expect(page.getByLabel('默认接话人')).toBeVisible()
})

test('busy 时单聊显示正在哪个群回答的横幅', async ({ page }) => {
  await mockRuntime(page, {
    agents: [{ ...AGENTS[0], status: 'running', busy: '评审群' }, AGENTS[1]],
    channels: CHANNELS,
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 前端', exact: true }).click()

  await expect(page.getByText('他正在 #评审群 里回答', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: '去频道看 →' }).click()
  await expect(page.getByLabel('默认接话人')).toBeVisible()
})

test('成员名单里能直接改权限', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)

  await page.locator('[title="查看成员名单"]').click()
  await expect(page.getByText('成员 · 评审群')).toBeVisible()

  const chips = page.getByRole('button', { name: /逐次审批/ })
  await expect(chips).toHaveCount(2)
  await chips.first().click()
  await page.getByRole('menu').getByRole('menuitemradio', { name: /自动/ }).click()

  // 改权限不该把人带进个人主页，名单还开着
  await expect(page.getByText('成员 · 评审群')).toBeVisible()
  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const set = calls.filter((c) => c.method === 'API.SetAgentPermission')
  expect(set.length).toBe(1)
  expect(set[0].args).toEqual(['a1', 'auto'])
})

test('个人主页里有权限选择器', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)

  await page.locator('[title="看他的个人主页"]').first().click()
  await expect(page.getByText('个人主页')).toBeVisible()
  await expect(page.getByText('干活权限')).toBeVisible()
  await expect(page.getByRole('button', { name: /逐次审批/ })).toBeVisible()
})