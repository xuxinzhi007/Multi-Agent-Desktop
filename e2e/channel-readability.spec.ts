import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 阅读与协作：@ 全员不截断、弹层可滚、markdown 渲染、复制/转派、表格横向滚动。
 */

const NAMES = ['前端 5', '后端 10', '架构 11', '测试工程师', '验收员 6', '能力规划师', 'Reviewer 8', '审查助手', '业务']
const AGENTS9 = NAMES.map((name, i) => ({
  id: `a${i}`,
  name,
  role: 'r',
  color: '#5b9dff',
  status: 'idle',
  createdAt: i + 1,
  messages: [],
  queueLen: 0,
  backend: 'loop',
  workDir: '/tmp',
}))

const CH9 = [
  { id: 'ch1', name: '评审群', members: AGENTS9.map((a) => a.id), policy: 'discuss', speaker: 'a1', messages: [] },
]

test('@ 空query列出所有人+全部成员不截断', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS9, channels: CH9, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)

  const editor = page.getByLabel(/发消息到 #评审群/)
  await editor.click()
  await editor.pressSequentially('@')
  const list = page.getByRole('listbox', { name: '提及成员' })
  await expect(list.getByRole('option')).toHaveCount(10)
  // 最后一名「业务」必须完整露出（可滚到底）
  await expect(list.getByRole('option', { name: /业务/ })).toBeVisible()
  await list.getByRole('option', { name: /业务/ }).scrollIntoViewIfNeeded()
  await expect(list.getByRole('option', { name: /后端 10/ })).toBeVisible()
})

const AGENTS2 = [
  { id: 'a1', name: '业务', role: 'r', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
  { id: 'a2', name: '后端 10', role: 'r', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]
const MD = '## 评估结论\n\n| 项 | 结果 |\n|---|---|\n| 方案 | 需调整 |\n\n可派给后端。'
const CH2 = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1', 'a2'],
    policy: 'discuss',
    speaker: 'a2',
    messages: [{ id: 'm1', channelId: 'ch1', fromId: 'a1', fromName: '业务', text: MD, at: 100 }],
  },
]

async function openCh2(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)
}

test('agent 消息渲染 markdown 表格与标题', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS2, channels: CH2, notes: [] })
  await openCh2(page)
  await expect(page.locator('.c-channel-card h2', { hasText: '评估结论' })).toBeVisible()
  await expect(page.locator('.c-channel-card table')).toBeVisible()
  await expect(page.locator('.c-channel-card td', { hasText: '需调整' })).toBeVisible()
})

test('agent 消息可转派给其他成员', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS2, channels: CH2, notes: [] })
  await openCh2(page)
  await page.getByRole('button', { name: '转派' }).click()
  await page.locator('.c-channel-forward').getByRole('button', { name: '@后端 10' }).click()
  await page.waitForTimeout(300)
  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const sent = calls.filter((c) => c.method === 'API.SendToChannel')
  expect(sent.length).toBe(1)
  expect(String(sent[0].args[3])).toContain('@后端 10 请跟进这条')
  expect(String(sent[0].args[3])).toContain('评估结论')
})

test('agent 消息有复制按钮', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS2, channels: CH2, notes: [] })
  await openCh2(page)
  await expect(page.getByRole('button', { name: '复制' })).toBeVisible()
})