import { expect, mockRuntime, test, type Page } from './mock-runtime'

/**
 * 插话打断的工具状态展示。
 *
 * 后端 loop 在工具被插话信号打断时把该调用标成 cancelled（不是 error/timeout）：
 * 已完成的结果原样保留，只有被打断那条走中性态。前端不能把它渲染成红色失败卡，
 * 也不能在文案里出现「失败」字样。
 */

const AGENT = {
  id: 'a1',
  name: '前端',
  role: 'React',
  color: '#5b9dff',
  status: 'running',
  createdAt: 1,
  messages: [],
  queueLen: 0,
}

async function openChat(page: Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()
  await page.waitForTimeout(150)
}

async function emitTool(page: Page, payload: Record<string, unknown>) {
  await page.evaluate((p) => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.tool', p)
  }, payload)
}

test('被插话打断的工具卡是中性已打断态，同批成功工具仍是完成态', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await openChat(page)

  // 先跑完的 bash：成功态。
  await emitTool(page, { id: 't1', tool: 'bash', phase: 'start', content: 'ls -la' })
  await emitTool(page, { id: 't1', tool: 'bash', phase: 'end', status: 'ok', content: 'drwxr-xr-x' })

  // 跑到一半被插话打断的 read：cancelled。
  await emitTool(page, { id: 't2', tool: 'read', phase: 'start', content: 'config.json' })
  await emitTool(page, {
    id: 't2',
    tool: 'read',
    phase: 'end',
    status: 'cancelled',
    content: '调用被用户插话打断，没有完成结果。',
  })
  await page.waitForTimeout(200)

  const inspector = page.locator('aside')
  const cancelledCard = inspector.locator('[data-tool-status]').filter({ hasText: 'read' })
  const okCard = inspector.locator('[data-tool-status]').filter({ hasText: 'bash' })

  await expect(cancelledCard).toHaveAttribute('data-tool-status', 'cancelled')
  await expect(cancelledCard).toContainText('插话打断')
  await expect(cancelledCard).not.toContainText('失败')

  await expect(okCard).toHaveAttribute('data-tool-status', 'ok')
  await expect(okCard).toContainText('✓')
})
