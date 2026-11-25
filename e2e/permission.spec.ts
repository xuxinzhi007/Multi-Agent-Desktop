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

test('输入栏能切权限档位，默认逐次审批', async ({ page }) => {
  const chip = page.getByRole('button', { name: /逐次审批/ })
  await expect(chip).toBeVisible()
  await chip.click()

  const menu = page.getByRole('menu')
  await expect(menu.getByRole('menuitemradio', { name: /自动/ })).toBeVisible()
  await expect(menu.getByRole('menuitemradio', { name: /只读/ })).toBeVisible()
  // 不存在的「完整权限」不该出现：工作目录是硬边界
  await expect(menu.getByText('完整权限')).toHaveCount(0)
  await expect(menu.getByText(/工作目录是硬边界/)).toBeVisible()

  await menu.getByRole('menuitemradio', { name: /只读/ }).click()
  await expect(page.getByRole('button', { name: /只读/ })).toBeVisible()

  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const set = calls.filter((c) => c.method === 'API.SetAgentPermission')
  expect(set.length).toBe(1)
  expect(set[0].args).toEqual(['a1', 'readonly'])
})
