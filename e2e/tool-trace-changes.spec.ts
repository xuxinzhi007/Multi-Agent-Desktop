import { expect, mockRuntime, test, type Page } from './mock-runtime'

const AGENT = {
  id: 'a1',
  name: '后端',
  role: 'Go',
  color: '#5b9dff',
  status: 'idle',
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

const inspector = (page: Page) => page.locator('aside').last()

/**
 * 回归：改动清单不能再被「只留最近 8 条工具」截断。
 * 一轮里先改了 3 个文件、又读了 9 个文件，最早的 3 个改动必须还在。
 */
test('长轮次里较早改过的文件仍然出现在改动清单', async ({ page }) => {
  await openChat(page)

  const edited = ['internal/agent/loop.go', 'src/App.tsx', 'src/styles/components.css']
  for (const [i, path] of edited.entries()) {
    await emit(page, 'agent.tool', {
      id: 'a1', callId: `edit-${i}`, tool: 'edit_file', phase: 'end', status: 'ok',
      path, diff: '@@ -1 +1 @@\n-old\n+new', content: `edited ${path}`,
    })
  }
  for (let i = 0; i < 9; i++) {
    await emit(page, 'agent.tool', {
      id: 'a1', callId: `read-${i}`, tool: 'read_file', phase: 'end', status: 'ok',
      content: `read ${i}`, output: 'ok',
    })
  }

  const card = page.locator('text=本轮改了').first()
  await expect(card).toBeVisible()
  await expect(card).toContainText('3 个文件')
  for (const path of edited) {
    await expect(page.getByRole('button', { name: new RegExp(escapePath(path)) })).toBeVisible()
  }
})

/**
 * Read / Glob 的 path 是「读哪里、在哪找」，不是落盘改动。
 * 之前改动清单把它们全算进去，一轮下来能刷出一堆没动过的文件。
 */
test('只有写盘工具进改动清单，读取与搜索不算', async ({ page }) => {
  await openChat(page)
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'read-1', tool: 'Read', phase: 'end', status: 'ok',
    path: 'internal/agent/loop.go', content: 'read ok',
  })
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'glob-1', tool: 'Glob', phase: 'end', status: 'ok',
    path: '/tmp/e2e-project', content: 'found 3',
  })
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'write-1', tool: 'Write', phase: 'end', status: 'ok',
    path: 'src/App.tsx', diff: '@@ -1 +1 @@\n-a\n+b',
  })

  const card = page.locator('text=本轮改了').first()
  await expect(card).toBeVisible()
  await expect(card).toContainText('1 个文件')
  await expect(page.getByRole('button', { name: /src\/App\.tsx/ })).toBeVisible()
  await expect(page.getByRole('button', { name: /internal\/agent\/loop\.go/ })).toHaveCount(0)
})

test('点击对话里的文件名直接打开检查器改动页', async ({ page }) => {
  await openChat(page)
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'write-1', tool: 'edit_file', phase: 'end', status: 'ok',
    path: 'internal/agent/doubao_backend.go', diff: '@@ -3 +3 @@\n-旧实现\n+新实现',
    content: 'edited',
  })

  await page
    .getByRole('button', { name: /internal\/agent\/doubao_backend\.go/ })
    .click()

  await expect(inspector(page)).toBeVisible()
  await expect(inspector(page).getByRole('button', { name: /改动/ })).toHaveAttribute(
    'data-tab-active',
    'true',
  )
  const change = inspector(page).locator('[data-file-change="internal/agent/doubao_backend.go"]')
  await expect(change).toBeVisible()
  await expect(change).toContainText('+新实现')
})

/**
 * 频道与单聊共用同一套思考链：流式回复时，工具步骤和推理都要能看见，
 * 而不是只有一句「正在思考中」。
 */
test('频道流式回复显示与单聊一致的工具步骤和推理', async ({ page }) => {
  const members = [
    { ...AGENT, name: '业务', id: 'a0', createdAt: 0 },
    { ...AGENT, id: 'a1', name: '后端', createdAt: 1, status: 'running', busy: '评审群' },
  ]
  const channels = [
    { id: 'ch1', name: '评审群', members: ['a0', 'a1'], policy: 'discuss', speaker: 'a1' },
  ]
  await mockRuntime(page, { agents: members, channels, notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(300)

  await emit(page, 'agent.reasoning', { id: 'a1', reasoning: '先看 diff，再决定改哪一行。' })
  await emit(page, 'agent.tool', {
    id: 'a1', callId: 'read-1', tool: 'read_file', phase: 'start', content: 'internal/agent/loop.go',
  })
  await emit(page, 'agent.delta', { id: 'a1', channelId: 'ch1', delta: '正在改这一段' })
  await page.waitForTimeout(500)

  const rail = page.locator('.c-think').last()
  await expect(rail).toBeVisible()
  await expect(rail).toContainText('读取')
  await expect(page.getByText('先看 diff，再决定改哪一行。')).toBeVisible()
  await expect(page.getByText('正在改这一段')).toBeVisible()
})

function escapePath(p: string) {
  return p.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&')
}
