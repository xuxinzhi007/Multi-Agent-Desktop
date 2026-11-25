import { expect, mockRuntime, test } from './mock-runtime'

const agents = [
  { id: 'a1', name: '后端开发', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp/main', extraDirs: ['/tmp/other'] },
]

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents, channels: [], notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 后端开发', exact: true }).click()
})

test('工作目录弹层能看附加目录，能加能删', async ({ page }) => {
  await page.getByTitle(/工作目录：/).click()
  await expect(page.getByText('附加目录')).toBeVisible()
  await expect(page.getByText('/tmp/other')).toBeVisible()
  await expect(page.getByText(/命令仍在主目录跑/)).toBeVisible()

  await page.getByLabel('附加目录路径').fill('/tmp/third')
  await page.getByRole('button', { name: '添加', exact: true }).click()

  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const set = calls.filter((c) => c.method === 'API.SetAgentExtraDirs')
  expect(set.length).toBe(1)
  expect(set[0].args).toEqual(['a1', ['/tmp/other', '/tmp/third']])

  await page.getByLabel('移除附加目录 /tmp/other').click()
  const after = (await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls))
    .filter((c) => c.method === 'API.SetAgentExtraDirs')
  expect(after[1].args).toEqual(['a1', []])
})
