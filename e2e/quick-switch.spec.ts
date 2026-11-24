import { expect, mockRuntime, test } from './mock-runtime'

const agents = [
  { id: 'a1', name: '后端开发', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
  { id: 'a2', name: '测试工程师', role: 'QA', color: '#3ecf8e', status: 'running', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]
const channels = [{ id: 'c1', name: '发布评审', members: ['a1'], messages: [] }]

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents, channels, notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
})

test('⌘K 打开快速跳转，输入名字直接进会话', async ({ page }) => {
  await page.keyboard.press('Meta+k')
  const dialog = page.getByRole('dialog', { name: '快速跳转' })
  await expect(dialog).toBeVisible()

  await dialog.getByPlaceholder('跳到会话、频道或飞书群…').fill('测试')
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page.getByRole('button', { name: '会话 测试工程师', exact: true })).toHaveAttribute('aria-current', 'true')
})

test('快速跳转把会话和频道放在同一个列表里', async ({ page }) => {
  await page.keyboard.press('Meta+k')
  const dialog = page.getByRole('dialog', { name: '快速跳转' })
  await expect(dialog.getByText('后端开发')).toBeVisible()
  await expect(dialog.getByText('发布评审')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()
})

test('侧栏分组能收起，条数还在', async ({ page }) => {
  const head = page.getByRole('button', { name: '收起频道' })
  await expect(head).toBeVisible()
  await head.click()
  await expect(page.getByRole('button', { name: '频道 发布评审' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: '展开频道' })).toContainText('1')
})
