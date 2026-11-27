import { expect, mockRuntime, test, type Page } from './mock-runtime'

const AGENT = {
  id: 'a1',
  name: '后端',
  role: 'Go',
  color: '#5b9dff',
  status: 'running',
  createdAt: 1,
  messages: [],
  queueLen: 0,
  backend: 'loop',
  workDir: '/tmp/e2e-project',
}

async function openChat(page: Page) {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '会话 后端', exact: true }).click()
}

async function emit(page: Page, name: string, payload: Record<string, unknown>) {
  await page.evaluate(
    ({ event, data }) => {
      const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
      mygo.emit(event, data)
    },
    { event: name, data: payload },
  )
}

test('同名并行工具按 callId 回填，输入和完整结果可以展开', async ({ page }) => {
  await openChat(page)
  await emit(page, 'agent.tool', { id: 'a1', callId: 'call-a', tool: 'read_file', phase: 'start', content: 'a.go' })
  await emit(page, 'agent.tool', { id: 'a1', callId: 'call-b', tool: 'read_file', phase: 'start', content: 'b.go' })
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'call-a', tool: 'read_file', phase: 'end', status: 'ok',
    input: '{"path":"a.go"}', content: 'A 的结果', output: 'A 的完整输出\n第二行',
  })
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'call-b', tool: 'read_file', phase: 'end', status: 'ok',
    input: '{"path":"b.go"}', content: 'B 的结果', output: 'B 的完整输出',
  })

  const inspector = page.locator('aside').last()
  const cardA = inspector.locator('[data-tool-status]').filter({ hasText: 'a.go' })
  const cardB = inspector.locator('[data-tool-status]').filter({ hasText: 'b.go' })
  await expect(cardA).toContainText('A 的结果')
  await expect(cardA).not.toContainText('B 的结果')
  await expect(cardB).toContainText('B 的结果')

  await cardA.getByText('查看调用与结果', { exact: true }).click()
  await expect(cardA.getByText('A 的完整输出\n第二行')).toBeVisible()
  await expect(cardA.getByText('{"path":"a.go"}')).toBeVisible()
})

test('改动页显示实际文件路径和 diff，只有 end 的事件也不丢', async ({ page }) => {
  await openChat(page)
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'write-1', tool: 'edit_file', phase: 'end', status: 'ok',
    content: 'edited a.go', output: 'edited a.go', path: 'internal/a.go',
    diff: '@@ -1 +1 @@\n-old\n+new', durationMs: 12,
  })

  const inspector = page.locator('aside').last()
  await inspector.getByRole('button', { name: /改动/ }).click()
  const change = inspector.locator('[data-file-change="internal/a.go"]')
  await expect(change).toBeVisible()
  await expect(change).toContainText('已落盘')
  await expect(change).toContainText('@@ -1 +1 @@')
  await expect(change).toContainText('+new')
})

test('正文开始流式输出后，思考文本仍然展开可见', async ({ page }) => {
  await openChat(page)
  await emit(page, 'agent.reasoning', { id: 'a1', reasoning: '先核对工具结果，再决定编辑锚点。' })
  await emit(page, 'agent.delta', { id: 'a1', delta: '已经开始写回复', channelId: '' })

  await expect(page.getByText('先核对工具结果，再决定编辑锚点。')).toBeVisible()
  await expect(page.getByText('已经开始写回复')).toBeVisible()
})
