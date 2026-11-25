import { expect, mockRuntime, test, type Page } from './mock-runtime'

/**
 * 运行时（native / 外部 ACP）前端护栏，对应 docs/runtime-api-contract.md §8。
 *
 * 覆盖三类：
 *  1. 新建弹窗：选 ACP 填命令创建成功并落 SetAgentRuntime；缺命令被拒且提示可读。
 *  2. 输入栏 RuntimeChip：native ↔ ACP 切换、按 agent.acp 回填、保存走 SetAgentRuntime。
 *  3. 会话行 / 舰队卡片：徽章 原生 / ACP · <名称> / ACP · 未配置；状态点与文案只认真实
 *     runtimeState / runtimeError，不伪造 running/exited。
 */

function agent(over: Record<string, unknown>) {
  return {
    id: 'a1',
    name: 'Agent',
    role: 'r',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1,
    messages: [],
    queueLen: 0,
    ...over,
  }
}

async function openRow(page: Page, name: string) {
  await page
    .getByRole('button', { name: `会话 ${name}`, exact: true })
    .first()
    .click()
}

test('新建弹窗：选 ACP 填命令 → 创建成功并落 SetAgentRuntime', async ({ page, callLog }) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  await dialog.getByPlaceholder('例如：后端开发').fill('连接器')
  await dialog.getByRole('button', { name: '外部 ACP' }).click()
  await dialog.getByPlaceholder('例如：grok；留空用命令名').fill('grok')
  await dialog.getByPlaceholder('例如：grok / codex-acp').fill('grok-acp')
  await dialog.getByPlaceholder('acp\n--xxx').fill('acp\n--xxx')
  await dialog.getByPlaceholder('KEY=VALUE').fill('KEY=VALUE')

  await dialog.getByRole('button', { name: '创建' }).click()
  await expect(dialog).toBeHidden()

  const calls = await callLog()
  const runtimeCall = calls.find((c) => c.method === 'API.SetAgentRuntime')
  expect(runtimeCall, '创建 ACP 会话后应调用 SetAgentRuntime').toBeTruthy()
  const req = runtimeCall!.args[0] as {
    runtime: string
    name: string
    command: string
    args: string[]
    env: string[]
  }
  expect(req.runtime).toBe('acp')
  expect(req.name).toBe('grok')
  expect(req.command).toBe('grok-acp')
  expect(req.args).toEqual(['acp', '--xxx'])
  expect(req.env).toEqual(['KEY=VALUE'])
})

test('新建弹窗：选 ACP 不填命令 → 创建被禁用，就地提示可读，不调 SetAgentRuntime', async ({
  page,
  callLog,
}) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  await dialog.getByPlaceholder('例如：后端开发').fill('连接器')
  await dialog.getByRole('button', { name: '外部 ACP' }).click()

  const createBtn = dialog.getByRole('button', { name: '创建' })
  await expect(createBtn).toBeDisabled()
  await expect(dialog.getByText('选了外部 ACP Agent，必须填写启动命令')).toBeVisible()

  let calls = await callLog()
  expect(calls.some((c) => c.method === 'API.SetAgentRuntime')).toBe(false)

  // 补上命令后创建恢复可用
  await dialog.getByPlaceholder('例如：grok / codex-acp').fill('grok-acp')
  await expect(createBtn).toBeEnabled()
})

test('RuntimeChip：已有 ACP 会话打开时回填展示名 / 命令 / 参数 / 环境变量', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '连接器',
        runtime: 'acp',
        acp: { name: 'grok', command: 'grok-acp', args: ['acp', '--xxx'], env: ['KEY=VALUE'] },
        runtimeState: 'running',
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await openRow(page, '连接器')

  const chip = page.locator('button[title*="ACP · grok"]')
  await expect(chip).toBeVisible()
  await chip.click()

  const popover = page.locator('.fade-up').last()
  await expect(popover).toBeVisible()
  await expect(popover.getByPlaceholder('例如：grok；留空用命令名')).toHaveValue('grok')
  await expect(popover.getByPlaceholder('例如：grok / codex-acp')).toHaveValue('grok-acp')
  await expect(popover.getByPlaceholder('acp\n--xxx')).toHaveValue('acp\n--xxx')
  await expect(popover.getByPlaceholder('KEY=VALUE')).toHaveValue('KEY=VALUE')
  // 真实 running 状态要在弹层里如实展示
  await expect(popover.getByText('状态：运行中')).toBeVisible()
})

test('RuntimeChip：native 会话切到 ACP，保存请求带命令与参数', async ({ page, callLog }) => {
  await mockRuntime(page, {
    agents: [agent({ id: 'n1', name: '本机', runtime: 'native' })],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await openRow(page, '本机')

  const chip = page.locator('button[title*="原生"]')
  await expect(chip).toBeVisible()
  await chip.click()

  const popover = page.locator('.fade-up').last()
  await popover.getByRole('button', { name: '外部 ACP' }).click()
  await popover.getByPlaceholder('例如：grok / codex-acp').fill('codex-acp')
  await popover.getByPlaceholder('acp\n--xxx').fill('exec')
  await popover.getByRole('button', { name: '应用' }).click()

  await expect
    .poll(async () => {
      const calls = await callLog()
      return calls.find((c) => c.method === 'API.SetAgentRuntime')
    })
    .toBeTruthy()
  const calls = await callLog()
  const req = calls.find((c) => c.method === 'API.SetAgentRuntime')!.args[0] as {
    runtime: string
    command: string
    args: string[]
  }
  expect(req.runtime).toBe('acp')
  expect(req.command).toBe('codex-acp')
  expect(req.args).toEqual(['exec'])
})

test('RuntimeChip：ACP 会话切回 native', async ({ page, callLog }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '连接器',
        runtime: 'acp',
        acp: { name: 'grok', command: 'grok-acp' },
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await openRow(page, '连接器')

  const chip = page.locator('button[title*="ACP · grok"]')
  await expect(chip).toBeVisible()
  await chip.click()

  const popover = page.locator('.fade-up').last()
  await popover.getByRole('button', { name: '原生' }).click()
  await popover.getByRole('button', { name: '应用' }).click()

  await expect
    .poll(async () => {
      const calls = await callLog()
      const req = calls.find((c) => c.method === 'API.SetAgentRuntime')
      return req ? (req.args[0] as { runtime: string }).runtime === 'native' : false
    })
    .toBe(true)
})

test('徽章与状态：原生 / ACP 名称 / 未配置，状态点与错误文案只认真实 runtimeState', async ({
  page,
}) => {
  await mockRuntime(page, {
    agents: [
      agent({ id: 'n1', name: '本机', runtime: 'native' }),
      agent({
        id: 'a1',
        name: '跑着',
        runtime: 'acp',
        acp: { name: 'grok', command: 'grok-acp' },
        runtimeState: 'running',
      }),
      agent({
        id: 'a2',
        name: '连接中',
        runtime: 'acp',
        acp: { name: 'codex', command: 'codex-acp' },
        runtimeState: 'starting',
      }),
      agent({
        id: 'a3',
        name: '报错',
        runtime: 'acp',
        acp: { name: 'bad', command: 'bad-acp' },
        runtimeState: 'error',
        runtimeError: '命令不存在：bad-acp',
      }),
      agent({ id: 'a4', name: '缺命令', runtime: 'acp', acp: { command: '' } }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 舰队卡片头部
  const card = (name: string) => page.locator('.c-fleet-card', { hasText: name })
  await expect(card('本机').locator('.c-fleet-pill')).toHaveText('原生')
  await expect(card('跑着').locator('.c-fleet-pill')).toContainText('ACP · grok')
  await expect(card('连接中').locator('.c-fleet-pill')).toContainText('ACP · codex')
  await expect(card('报错').locator('.c-fleet-pill')).toContainText('ACP · bad')
  await expect(card('缺命令').locator('.c-fleet-pill')).toHaveText('ACP · 未配置')

  // 状态点：running / starting(queued) / error；原生无状态点
  await expect(card('跑着').locator('.c-dot--running')).toHaveCount(1)
  await expect(card('连接中').locator('.c-dot--queued')).toHaveCount(1)
  await expect(card('报错').locator('.c-dot--error')).toHaveCount(1)
  await expect(card('本机').locator('.c-dot')).toHaveCount(0)

  // 错误原文进徽章 title，不被吞掉
  const badTitle = await card('报错').locator('.c-fleet-pill').getAttribute('title')
  expect(badTitle).toContain('命令不存在：bad-acp')

  // 侧栏会话行同样带徽章
  const row = page.getByRole('button', { name: '会话 跑着', exact: true })
  await expect(row.locator('.c-fleet-pill')).toContainText('ACP · grok')
})
