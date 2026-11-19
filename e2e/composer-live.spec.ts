import { expect, mockRuntime, test } from './mock-runtime'

const AGENT = {
  id: 'a1',
  name: '前端',
  role: 'React',
  color: '#5b9dff',
  status: 'running',
  createdAt: 1,
  messages: [
    { role: 'system', content: 'sys' },
    { role: 'user', content: '上一句' },
  ],
  queueLen: 0,
}

test('运行中输入框有停止，空着不能再显示成发送', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()

  await expect(page.getByRole('button', { name: '停止' }).last()).toBeVisible()
  await expect(page.getByRole('button', { name: '发送', exact: true })).toHaveCount(0)
  await expect(page.getByText('这一轮还在进行')).toBeVisible()
  await expect(page.getByText(/上下文/)).toBeVisible()

  await page.getByRole('button', { name: '停止' }).last().click()
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          ((window as unknown as { __mockCalls?: { method: string }[] }).__mockCalls ?? []).some(
            (c) => c.method === 'API.StopAgent',
          ),
      ),
    )
    .toBe(true)
})

test('运行中写了字，发送变成排队而不是插进当前这一轮', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()

  await page.locator('textarea').first().fill('等这轮完再说')
  await expect(page.getByRole('button', { name: '排队', exact: true })).toBeEnabled()
  await expect(page.getByRole('button', { name: '停止' }).last()).toBeVisible()
})
