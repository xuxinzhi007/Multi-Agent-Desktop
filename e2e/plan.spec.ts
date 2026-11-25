import { expect, mockRuntime, test, type Page, type PlanMock, type PlanMockTask } from './mock-runtime'

/**
 * 计划页（M2 / internal/scheduler）：状态来自 API.PlanStatus，
 * 列表走 PlanList，新建 / 暂停 / 删除 / 立即运行分别走
 * PlanCreate / PlanPause / PlanDelete / PlanRunNow。
 *
 * mock 覆盖：ok（运行中）/ disabled（调度器未启用）/ error（初始化异常）。
 * 后端校验错误原文透传到页面；前端不伪造执行结果。
 */

const AGENTS = [
  { id: 'a1', name: '前端', role: 'React', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0 },
  { id: 'a2', name: '后端', role: 'Go', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0 },
]

const TASKS: PlanMockTask[] = [
  {
    id: 'plan-1',
    type: 'interval',
    intervalSec: 300,
    cron: '',
    agentId: 'a1',
    prompt: '汇总今天的日志异常',
    enabled: true,
    createdAt: 1700000000,
    lastRunAt: 1700000100,
    nextRunAt: 1700000400,
    failures: 0,
    lastError: '',
  },
  {
    id: 'plan-2',
    type: 'cron',
    intervalSec: 0,
    cron: '0 9 * * *',
    agentId: 'a2',
    prompt: '生成早报摘要',
    enabled: false,
    createdAt: 1700000001,
    lastRunAt: 1700000200,
    nextRunAt: 0,
    failures: 3,
    lastError: 'runner 超时',
  },
]

async function openPlan(page: Page, plan: PlanMock) {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [], plan })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '计划' }).click()
  return page.locator('[data-testid="plan-page"]')
}

test('已接入：状态 / 类型 / 绑定 Agent / 上次下次 / 失败次数 / 启用暂停全部渲染', async ({ page }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })

  await expect(plan.getByText('运行中')).toBeVisible()
  await expect(plan.getByText('共 2 · 启用 1')).toBeVisible()

  const row1 = plan.getByTestId('plan-row-plan-1')
  await expect(row1.getByText('间隔')).toBeVisible()
  await expect(row1.getByText('300 秒')).toBeVisible()
  await expect(row1.getByText('汇总今天的日志异常')).toBeVisible()
  await expect(row1.getByText('启用')).toBeVisible()

  const row2 = plan.getByTestId('plan-row-plan-2')
  await expect(row2.getByText('Cron')).toBeVisible()
  await expect(row2.locator('.mono')).toHaveText('0 9 * * *')
  await expect(row2.getByText('生成早报摘要')).toBeVisible()
  await expect(row2.getByText('已暂停')).toBeVisible()
  await expect(row2.getByText('失败 3')).toBeVisible()
  await expect(row2.getByText('runner 超时')).toBeVisible()
})

test('新建 interval：校验通过后调 PlanCreate，任务出现在列表', async ({ page, callLog }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })

  await plan.getByPlaceholder('间隔秒数，例如 300').fill('600')
  await plan.getByRole('button', { name: '绑定 Agent' }).click()
  await plan.getByRole('option', { name: '前端' }).click()
  await plan.getByPlaceholder('发给绑定 Agent 的指令…').fill('定时巡检日志')
  await plan.getByRole('button', { name: '新建计划' }).click()

  await expect(plan.getByText('定时巡检日志')).toBeVisible()
  const calls = await callLog()
  const create = calls.find((c) => c.method === 'API.PlanCreate')
  expect(create).toBeTruthy()
  expect(create?.args[0]).toMatchObject({
    type: 'interval',
    intervalSec: 600,
    cron: '',
    agentId: 'a1',
    prompt: '定时巡检日志',
    channel: '',
    enabled: true,
  })
})

test('新建 cron 校验失败：后端错误原文上屏，不留成功假象', async ({ page, callLog }) => {
  const plan = await openPlan(page, {
    mode: 'ok',
    tasks: TASKS,
    createError: 'cron 表达式无法解析：第 3 段不是数字',
  })

  await plan.getByRole('button', { name: 'Cron' }).click()
  await plan.getByPlaceholder('5 段 cron，例如 0 9 * * *').fill('0 9 x * *')
  await plan.getByRole('button', { name: '绑定 Agent' }).click()
  await plan.getByRole('option', { name: '后端' }).click()
  await plan.getByPlaceholder('发给绑定 Agent 的指令…').fill('跑一遍日报')
  await plan.getByRole('button', { name: '新建计划' }).click()

  await expect(plan.getByText('cron 表达式无法解析：第 3 段不是数字')).toBeVisible()
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.PlanCreate'))
    .toHaveLength(1)
})

test('暂停：调 PlanPause(id, true)，行内状态变为已暂停', async ({ page, callLog }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })
  const row1 = plan.getByTestId('plan-row-plan-1')

  await row1.getByRole('button', { name: '暂停' }).click()

  await expect(row1.getByText('已暂停')).toBeVisible()
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.PlanPause'))
    .toEqual([{ method: 'API.PlanPause', args: ['plan-1', true] }])
})

test('恢复：调 PlanPause(id, false)', async ({ page, callLog }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })
  const row2 = plan.getByTestId('plan-row-plan-2')

  await row2.getByRole('button', { name: '恢复' }).click()

  await expect(row2.getByText('启用')).toBeVisible()
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.PlanPause'))
    .toEqual([{ method: 'API.PlanPause', args: ['plan-2', false] }])
})

test('立即运行：调 PlanRunNow，无视静默与限频', async ({ page, callLog }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })

  await plan.getByTestId('plan-row-plan-1').getByRole('button', { name: '立即运行' }).click()

  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.PlanRunNow'))
    .toEqual([{ method: 'API.PlanRunNow', args: ['plan-1'] }])
})

test('立即运行失败：runner 错误原文留在行内', async ({ page }) => {
  const plan = await openPlan(page, {
    mode: 'ok',
    tasks: TASKS,
    actionError: 'runner 超时拒绝执行',
  })

  await plan.getByTestId('plan-row-plan-1').getByRole('button', { name: '立即运行' }).click()

  await expect(plan.getByTestId('plan-row-plan-1').getByText('runner 超时拒绝执行')).toBeVisible()
})

test('删除：调 PlanDelete，任务从列表消失', async ({ page, callLog }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: TASKS })

  await plan.getByTestId('plan-row-plan-2').getByRole('button', { name: '删除' }).click()

  await expect(plan.getByTestId('plan-row-plan-2')).toHaveCount(0)
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.PlanDelete'))
    .toEqual([{ method: 'API.PlanDelete', args: ['plan-2'] }])
})

test('空态：没有任务时展示引导空态', async ({ page }) => {
  const plan = await openPlan(page, { mode: 'ok', tasks: [] })

  await expect(plan.getByText('还没有计划')).toBeVisible()
})

test('未启用：disabled 时展示未接入空态', async ({ page }) => {
  const plan = await openPlan(page, { mode: 'disabled' })

  await expect(plan.getByText('调度器尚未启动')).toBeVisible()
  await expect(plan.getByText('运行中')).toHaveCount(0)
})

test('状态异常：error 模式把后端错误原因带到页面', async ({ page }) => {
  const plan = await openPlan(page, { mode: 'error' })

  await expect(plan.getByText('调度器初始化失败：schedules.json 损坏')).toBeVisible()
})
