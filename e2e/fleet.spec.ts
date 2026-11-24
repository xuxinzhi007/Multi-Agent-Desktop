import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 舰队视图（多 agent 并排监控台）。
 *
 * 这个视图回答的是「我那几个 agent 现在都在干什么」，
 * 所以每个 agent 要能看到：状态、当前任务、实时输出。
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

test('多个 agent 并排显示，各自独立一列', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({ id: 'a1', name: '前端', status: 'running', startedAt: Date.now() / 1000, chars: 120 }),
      agent({ id: 'a2', name: '后端', status: 'idle' }),
      agent({ id: 'a3', name: '测试', status: 'queued', queueLen: 2 }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 默认就是舰队视图，三个 agent 同屏
  for (const name of ['前端', '后端', '测试']) {
    await expect(page.getByText(name, { exact: true }).first()).toBeVisible()
  }

  // 状态要能区分开，不能都是「空闲」
  await expect(page.getByText('运行中').first()).toBeVisible()
  await expect(page.getByText('排队').first()).toBeVisible()
})

test('运行中的 agent 显示耗时与产出字数', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '前端',
        status: 'running',
        startedAt: Date.now() / 1000 - 5,
        chars: 42,
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 用户要能判断「它在动，不是在卡」：耗时和产出都在指标带里，
  // 它们被拆成多个 span，所以整体取文本再断言
  const metrics = (await page.locator('.c-fleet-card').first().textContent()) ?? ''
  expect(metrics, `卡片内容: ${metrics}`).toMatch(/\d+s/)
  expect(metrics, `卡片内容: ${metrics}`).toMatch(/42 字/)
})

test('舰队卡片先显示最近产出，打开会话后才加载完整历史', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '后端',
        status: 'running',
        startedAt: Date.now() / 1000,
        messages: [
          { role: 'system', content: 'sys' },
          { role: 'user', content: '把登录接口的错误码统一一下' },
          { role: 'assistant', content: '好的，我先看现有实现' },
        ],
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 没打开会话时只拿壳：卡片上不该假装知道这一轮的任务。
  // 有最近产出时就只展示它，不再摆一个空的「当前任务 / 还没有任务」标题。
  await expect(page.getByText('最近产出')).toBeVisible()
  await expect(page.getByText('当前任务')).toHaveCount(0)
  await expect(page.getByText('把登录接口的错误码统一一下')).toHaveCount(0)
  await expect(page.getByText('好的，我先看现有实现')).toBeVisible()

  // 打开对话后才去取历史，用户那句话在转写里
  await page.getByRole('button', { name: '打开对话' }).click()
  await expect(page.getByText('把登录接口的错误码统一一下')).toBeVisible()
})

test('实时输出优先于最近产出显示', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '前端',
        status: 'running',
        startedAt: Date.now() / 1000,
        messages: [{ role: 'assistant', content: '旧的完整回复' }],
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('agent.delta', { id: 'a1', delta: '正在输出这段内容' })
  })

  await expect(page.getByText('实时输出')).toBeVisible()
  await expect(page.getByText('正在输出这段内容')).toBeVisible()
})

test('从舰队可以打开某个 agent 的对话，也能切回舰队', async ({ page }) => {
  await mockRuntime(page, {
    agents: [agent({ id: 'a1', name: '前端' })],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.getByRole('button', { name: '打开对话' }).click()
  // 进入对话视图后能看到输入框
  await expect(page.locator('textarea').first()).toBeVisible()

  await page.getByRole('button', { name: /舰队/ }).click()
  await expect(page.getByRole('button', { name: '打开对话' })).toBeVisible()
})

test('blocked 的 agent 把原因显示出来', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({
        id: 'a1',
        name: '前端',
        status: 'blocked',
        blockedReason: '等待审批 edit_file',
      }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 侧栏行和舰队列都会显示，取第一个即可
  await expect(page.getByText('等待审批 edit_file').first()).toBeVisible()
  // 顶部汇总要提示有人等
  await expect(page.getByText(/1 个在等你/)).toBeVisible()
})

test('空舰队时提供一键创建', async ({ page }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await expect(page.getByText('组建你的 agent 舰队')).toBeVisible()
  const ctas = page.locator('.c-empty').getByRole('button')
  expect(await ctas.count()).toBeGreaterThan(0)
})

test('舰队视图默认不吃掉检查器空间', async ({ page }) => {
  await mockRuntime(page, {
    agents: [
      agent({ id: 'a1', name: '前端' }),
      agent({ id: 'a2', name: '后端' }),
      agent({ id: 'a3', name: '测试' }),
    ],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  // 检查器初始收起：舰队的价值是一眼看全，被挤窄就没意义
  await expect(page.getByText('agent 还没有改动文件')).toHaveCount(0)

  // 三张卡片都在视口内，不需要横向滚动才能看全
  const cards = page.locator('.c-fleet-card')
  await expect(cards).toHaveCount(3)
  const viewport = page.viewportSize()!
  for (let i = 0; i < 3; i++) {
    const box = (await cards.nth(i).boundingBox())!
    expect(box.x + box.width, `第 ${i + 1} 张卡片超出视口`).toBeLessThanOrEqual(viewport.width + 1)
  }
})

test('切到对话视图时检查器自动展开', async ({ page }) => {
  await mockRuntime(page, {
    agents: [agent({ id: 'a1', name: '前端' })],
    channels: [],
    notes: [],
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.getByRole('button', { name: /对话/ }).first().click()
  await expect(page.getByRole('button', { name: '工具', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /改动/ })).toHaveCount(0)
  // 精确匹配：侧栏底部的「⑂ 自动拆任务」不该被当成检查器的任务页
  await expect(page.getByRole('button', { name: '任务', exact: true })).toHaveCount(0)
})
