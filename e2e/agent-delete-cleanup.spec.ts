import { expect, mockRuntime, test } from './mock-runtime'

const agents = [
  { id: 'a1', name: '后端开发', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
  { id: 'a2', name: '前端开发', role: 'UI', color: '#3ecf8e', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]

/**
 * 检查器拿的是全部会话的工具行（App.tsx 传 s.tools，Inspector 不按会话过滤），
 * 所以「删会话没清 tools」是用户看得见的：那一行会永远留在检查器里，还计入条数。
 */
test('删会话后它的工具行不再留在检查器里', async ({ page }) => {
  await mockRuntime(page, { agents, channels: [], notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 后端开发', exact: true }).click()

  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.tool', { id: 'a1', tool: 'bash', target: 'git status', phase: 'start', at: 1 })
  })
  const inspector = page.locator('aside').last()
  await expect(inspector.getByText('bash')).toBeVisible()

  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.deleted', 'a1')
  })

  await expect(inspector.getByText('bash')).toHaveCount(0)
  await expect(inspector.getByText('还没有工具调用')).toBeVisible()
})
