import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 群聊视图的健壮性。
 *
 * 点进频道曾经直接白屏 —— 整棵 React 树抛异常后没有任何兜底，
 * 用户看到的是空白窗口，连错误信息都没有。
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
  },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1', 'a2'],
    policy: 'discuss',
    messages: [
      {
        id: 'm1',
        channelId: 'ch1',
        fromId: 'user',
        fromName: '你',
        text: '大家看下这个方案',
        at: 1,
        deliveries: [{ agentId: 'a1', status: 'delivered' }],
      },
      {
        id: 'm2',
        channelId: 'ch1',
        fromId: 'a1',
        fromName: '前端',
        text: '@后端 接口字段定了吗',
        at: 2,
      },
    ],
  },
]

test('点进频道不白屏，且渲染出已有消息', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 点频道
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(500)

  // 必须看到频道内容，而不是空白
  await expect(page.getByText('大家看下这个方案')).toBeVisible()
  await expect(page.getByText('接口字段定了吗', { exact: false })).toBeVisible()
  await expect(page.getByLabel('默认接话人')).toBeVisible()

  expect(errors, `页面抛异常: ${errors.join(' | ')}`).toEqual([])
})

test('频道消息里的 @提及也渲染成 chip', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(300)

  await expect(page.locator('.c-mention', { hasText: '@后端' })).toBeVisible()
})

test('频道成员已删除时不崩溃', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await mockRuntime(page, {
    agents: AGENTS,
    // 频道里引用了不存在的 a9
    channels: [{ ...CHANNELS[0], members: ['a1', 'a9'] }],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(500)

  await expect(page.getByText('大家看下这个方案')).toBeVisible()
  expect(errors, `页面抛异常: ${errors.join(' | ')}`).toEqual([])
})

test('频道里的成员被删除后，成员计数不崩溃', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  // members 为 null：Go 侧序列化 nil 切片时会产生 null
  await mockRuntime(page, {
    agents: AGENTS,
    channels: [{ ...CHANNELS[0], members: null, messages: null }],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 侧栏那一行「N 成员 · M 消息」曾经直接读 .length 导致白屏
  await expect(page.getByText('0 成员 · 0 消息')).toBeVisible()
  expect(errors, `页面抛异常: ${errors.join(' | ')}`).toEqual([])
})

test('旧数据缺 fromName / fromId 时不白屏', async ({ page }) => {
  // 老版本写进 channels.json 的消息缺这些字段，
  // 反序列化后是 undefined，渲染层 .slice(0,1) 直接 throw。
  await mockRuntime(page, {
    agents: [],
    notes: [],
    channels: [
      {
        id: 'ch-legacy',
        name: '历史群',
        members: [],
        messages: [
          { id: 'm1', channelId: 'ch-legacy', text: '来自旧版本的消息', at: Date.now() / 1000 },
          { id: 'm2', text: '连 channelId 都没有' },
        ],
      },
    ],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByText('历史群').first().click()
  await page.waitForTimeout(400)

  const body = await page.locator('body').innerText()
  expect(body, '缺字段的历史消息让整个页面崩了').not.toContain('Cannot read properties')
  // 消息本身还是要显示出来
  await expect(page.getByText('来自旧版本的消息')).toBeVisible()
})
