import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 频道的「同事」体验：投递显示真名、跳过折叠、无人接话提醒、
 * 成员名单面板、个人主页。全部走浏览器 mock，不碰真实后端。
 */

const AGENTS = [
  {
    id: 'agent-1764000001',
    name: '前端',
    role: 'React',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1764000001,
    messages: [],
    queueLen: 0,
  },
  {
    id: 'agent-1764000002',
    name: '后端',
    role: 'Go',
    color: '#3ecf8e',
    status: 'idle',
    createdAt: 1764000002,
    messages: [],
    queueLen: 0,
  },
  {
    id: 'agent-1764000003',
    name: '测试',
    role: 'QA',
    color: '#f5a623',
    status: 'idle',
    createdAt: 1764000003,
    messages: [],
    queueLen: 0,
  },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['agent-1764000001', 'agent-1764000002'],
    policy: 'discuss',
    speaker: 'agent-1764000002',
    messages: [
      {
        id: 'm1',
        channelId: 'ch1',
        fromId: 'user',
        fromName: '你',
        text: '大家看下这个方案',
        at: 1764000100,
        deliveries: [
          { agentId: 'agent-1764000002', status: 'delivered', reason: '默认接话人' },
          { agentId: 'agent-1764000001', status: 'skipped', reason: '未被点名' },
        ],
      },
      {
        id: 'm2',
        channelId: 'ch1',
        fromId: 'agent-1764000001',
        fromName: '前端',
        text: '我看完了，没问题',
        at: 1764000200,
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

test('投递结果显示成员真名，不再显示截断的 agent-17', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  await expect(page.getByText('已送达')).toBeVisible()
  const body = await page.locator('body').innerText()
  expect(body, '投递行仍在显示截断 ID').not.toContain('agent-17')
})

test('跳过的人折叠成一条，点开能看到名字和原因', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  const toggle = page.getByText('1 人未接话')
  await expect(toggle).toBeVisible()
  await toggle.click()
  await expect(page.getByText('前端（未被点名）')).toBeVisible()
})

test('没 @ 且没默认接话人时，发送框提示将无人接话', async ({ page }) => {
  await mockRuntime(page, {
    agents: AGENTS,
    channels: [{ ...CHANNELS[0], speaker: '' }],
    notes: [],
  })
  await openChannel(page)

  await expect(page.getByText('这条消息将无人接话', { exact: false })).toBeVisible()
})

test('点消息头像打开个人主页：职责、所在频道、最近发言、去单聊', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  await page.locator('[title="看他的个人主页"]').first().click()
  await expect(page.getByText('个人主页')).toBeVisible()
  await expect(page.getByText('所在频道（1）')).toBeVisible()
  await expect(page.getByRole('button', { name: '# 评审群' })).toBeVisible()
  await expect(page.getByText('我看完了，没问题')).toHaveCount(2) // 消息流 + 主页最近发言
  await expect(page.getByRole('button', { name: '去单聊' })).toBeVisible()
})

test('点头像区打开成员名单，非成员显示为其他同事', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  await openChannel(page)

  await page.locator('[title="查看成员名单"]').click()
  await expect(page.getByText('成员 · 评审群')).toBeVisible()
  await expect(page.getByText('默认接话', { exact: true })).toBeVisible()
  await expect(page.getByText('其他同事')).toBeVisible()
  await expect(page.getByRole('button', { name: '邀请' })).toBeDisabled()
})