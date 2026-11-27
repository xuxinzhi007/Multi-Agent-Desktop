import { expect, mockRuntime, test, type Page } from './mock-runtime'

const AGENT = {
  id: 'a1',
  name: '前端',
  role: 'React',
  color: '#5b9dff',
  status: 'idle',
  createdAt: 1,
  messages: [],
  queueLen: 0,
  backend: 'loop',
  runtime: 'native',
  workDir: '/tmp/project',
}

async function openChat(page: Page, agent: Record<string, unknown> = AGENT) {
  await mockRuntime(page, { agents: [agent], channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()
  await expect(page.locator('aside.c-inspector')).toBeVisible()
}

async function emitTool(page: Page, payload: Record<string, unknown>) {
  await page.evaluate((p) => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.tool', p)
  }, payload)
}

test('工具检查器按状态筛选并默认降噪空结果', async ({ page }) => {
  await openChat(page)
  await emitTool(page, { id: 'a1', tool: 'read_file', phase: 'start', content: 'src/empty.ts:1' })
  await emitTool(page, { id: 'a1', tool: 'read_file', phase: 'end', status: 'ok', content: '(no matches)' })
  await emitTool(page, { id: 'a1', tool: 'bash', phase: 'start', content: 'git status' })
  await emitTool(page, { id: 'a1', tool: 'bash', phase: 'end', status: 'ok', content: 'working tree clean' })
  await emitTool(page, { id: 'a1', tool: 'grep', phase: 'start', content: 'needle src/' })
  await emitTool(page, { id: 'a1', tool: 'grep', phase: 'end', status: 'error', content: 'permission denied' })

  const inspector = page.locator('aside.c-inspector')
  await expect(inspector.getByText('(no matches)', { exact: true })).toHaveCount(0)
  await expect(inspector.getByRole('button', { name: '展开 1 条空结果' })).toBeVisible()
  await expect(inspector.locator('[data-tool-status="ok"]')).toHaveCount(1)

  await inspector.getByLabel('按结果状态筛选').selectOption('error')
  await expect(inspector.locator('.c-tool-card')).toHaveCount(1)
  await expect(inspector.locator('[data-tool-status="error"]')).toContainText('permission denied')

  await inspector.getByLabel('按结果状态筛选').selectOption('all')
  await inspector.getByLabel('按工具类型筛选').selectOption('command')
  await expect(inspector.locator('.c-tool-card')).toHaveCount(1)
  await expect(inspector.locator('.c-tool-card')).toContainText('git status')

  await inspector.getByLabel('按工具类型筛选').selectOption('all')
  await inspector.getByRole('button', { name: '展开 1 条空结果' }).click()
  await expect(inspector.getByText('(no matches)', { exact: true }).first()).toBeVisible()
  await expect(inspector.locator('.c-tool-card')).toHaveCount(3)
})

test('工具时间轴可定位并展开文件证据', async ({ page }) => {
  await openChat(page)
  await emitTool(page, { id: 'a1', tool: 'read_file', phase: 'start', content: 'src/App.tsx:12' })
  await emitTool(page, {
    id: 'a1',
    tool: 'read_file',
    phase: 'end',
    status: 'ok',
    content: '11  export function App() {\n12    const answer = 42\n13    return answer\n14  }',
  })
  await emitTool(page, { id: 'a1', tool: 'bash', phase: 'start', content: 'npm run typecheck' })
  await emitTool(page, { id: 'a1', tool: 'bash', phase: 'end', status: 'ok', content: '0 errors' })

  const inspector = page.locator('aside.c-inspector')
  const timeline = inspector.getByLabel('工具执行时间轴')
  await expect(timeline).toHaveValue('2')
  await timeline.focus()
  await timeline.press('Home')
  await expect(timeline).toHaveValue('1')

  const selected = inspector.locator('[data-tool-selected="true"]')
  await expect(selected).toContainText('read_file')
  const callsBefore = await page.evaluate(() => ((window as unknown as { __mockCalls?: unknown[] }).__mockCalls ?? []).length)
  await selected.getByRole('button', { name: 'src/App.tsx:12' }).click()
  await expect(selected.locator('[data-tool-evidence]')).toContainText('const answer = 42')
  const callsAfter = await page.evaluate(() => ((window as unknown as { __mockCalls?: unknown[] }).__mockCalls ?? []).length)
  expect(callsAfter).toBe(callsBefore)
})

test('窄窗口遥测与输入栏不裁掉核心操作', async ({ page }) => {
  await page.setViewportSize({ width: 860, height: 640 })
  await openChat(page, { ...AGENT, status: 'running', queueLen: 2 })
  await page.locator('textarea').first().fill('插一句')
  const send = page.getByRole('button', { name: '插入', exact: true })
  await expect(send).toBeVisible()
  const box = await send.boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width).toBeLessThanOrEqual(860)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await expect(page.locator('[data-session-telemetry]')).toBeVisible()
})

test('上下文水位在会话顶部并按阈值提示', async ({ page }) => {
  await openChat(page, {
    ...AGENT,
    llm: { provider: 'qoder', model: 'Qwen4-Max', baseUrl: '', apiKey: '', contextWindow: 1000 },
  })
  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.message', {
      id: 'a1',
      message: { role: 'assistant', content: '完成', timestamp: 2 },
      tokensIn: 920,
      tokensOut: 80,
    })
  })

  const telemetry = page.locator('[data-session-telemetry]')
  const meter = telemetry.getByRole('meter', { name: '上下文占用' })
  await expect(meter).toHaveAttribute('aria-valuenow', '920')
  await expect(meter).toHaveAttribute('data-level', 'warning')
  await expect(meter).toContainText('92%')
  await expect(page.getByRole('meter', { name: '上下文占用' })).toHaveCount(1)
  await expect(page.locator('.composer-bar')).not.toContainText('/tmp/project')
  await expect(telemetry).toContainText('tmp/project')
})
