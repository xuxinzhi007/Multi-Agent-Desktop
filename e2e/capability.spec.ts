import { expect, mockRuntime, test } from './mock-runtime'

const agents = [
  { id: 'a1', name: '后端开发', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents, channels: [], notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 后端开发', exact: true }).click()
})

test('能力页列出工具，并说清哪一项现在用不了', async ({ page }) => {
  await page.getByRole('button', { name: /^能力/ }).click()
  await expect(page.getByText('bash', { exact: true })).toBeVisible()
  await expect(page.getByText('内置浏览器还没打开')).toBeVisible()
  await expect(page.getByText(/1 项能力现在用不了/)).toBeVisible()
})

test('技能能展开看正文，也能新建', async ({ page }) => {
  await page.getByRole('button', { name: /^能力/ }).click()
  await page.getByRole('button', { name: /排查慢SQL/ }).first().click()
  await expect(page.getByText(/先按租户收窄/)).toBeVisible()

  await page.getByRole('button', { name: '新建', exact: true }).click()
  await page.getByLabel('技能名').fill('值班排障')
  await page.getByLabel('步骤正文').fill('1. 先看日志\n2. 再对代码')
  await page.getByRole('button', { name: '保存技能' }).click()
  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string }[] }).__mockCalls)
  expect(calls.some((c) => c.method === 'API.SaveSkill')).toBe(true)
})
