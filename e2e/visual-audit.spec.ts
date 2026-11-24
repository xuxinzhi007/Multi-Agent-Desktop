import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 视觉巡检：把每个真实界面在亮/暗两套主题下截下来，人眼过一遍。
 *
 * 断言只保证「渲染出来了」，好不好看靠看图。截图落在 test-results/shots/。
 * 跑法：npx playwright test e2e/visual-audit.spec.ts
 */

const now = Math.floor(Date.now() / 1000)

const agents = [
  {
    id: 'a1',
    name: '后端开发',
    role: '负责 Go 服务与 SQL',
    color: '#4d8cf0',
    status: 'running',
    createdAt: now - 900,
    startedAt: now - 42,
    lastTool: 'bash',
    chars: 2480,
    queueLen: 0,
    backend: 'loop',
    workDir: '/Users/me/gowork/data-service',
    last: '正在核对 tenant_scope 的三条分支',
    messages: [
      { role: 'user', content: '生产日志里 job 查不到，帮我看下列表查询的租户过滤', timestamp: now - 120 },
      {
        role: 'assistant',
        content:
          '## 结论\n\n不是查询 bug，是这个任务没被打上 `qc_tenant_id` 标。\n\n三条 SQL 分支逐条对不上：\n\n| 分支 | 条件 | 命中 |\n| --- | --- | --- |\n| 无本体视图 | `job_source = LLM` | ❌ |\n| 回流视图 | `qc_tenant_id = 调用方` | ❌ |\n| 本租户 | `tenant_id = 1108` | ❌ |\n\n```go\nif bodyLess {\n    base.Where("tb_job.job_source = ?", "LLM")\n}\n```\n\n- 创建租户 1180，请求者 1108\n- 需要补 `qc_tenant_id` 或走公共质检租户视图',
        timestamp: now - 60,
      },
      { role: 'user', content: '那修复要动哪几层？', timestamp: now - 20 },
    ],
  },
  {
    id: 'a2',
    name: '测试工程师',
    role: '跑用例、复现问题',
    color: '#3ecf8e',
    status: 'blocked',
    blockedReason: '要执行 git push，等你批准',
    createdAt: now - 800,
    startedAt: now - 300,
    lastTool: 'web',
    chars: 1200,
    queueLen: 1,
    backend: 'loop',
    workDir: '/Users/me/gowork/data-service',
    last: '要执行 git push，等你批准',
    messages: [],
  },
  {
    id: 'a3',
    name: '审查助手',
    role: '代码评审',
    color: '#e8a33d',
    status: 'error',
    createdAt: now - 700,
    queueLen: 0,
    backend: 'loop',
    workDir: '',
    last: '上下文超了：26677 > 4096',
    messages: [],
  },
  {
    id: 'a4',
    name: '前端开发',
    role: '界面与交互',
    color: '#b07cf5',
    status: 'idle',
    createdAt: now - 600,
    queueLen: 0,
    backend: 'loop',
    workDir: '/Users/me/gowork/multi-agent-desktop',
    messages: [],
  },
  {
    id: 'a5',
    name: '后端开发的学习',
    role: '整理历史',
    color: '#4dc9e0',
    status: 'idle',
    parentId: 'a1',
    depth: 1,
    createdAt: now - 500,
    queueLen: 0,
    backend: 'loop',
    workDir: '',
    messages: [],
  },
]

const channels = [
  {
    id: 'c1',
    name: '发布评审',
    members: ['a1', 'a2', 'a4'],
    speaker: 'a1',
    policy: 'discuss',
    messages: [
      { id: 'm1', fromId: 'user', fromName: '你', text: '这个改动可以发版了吗？', at: now - 300 },
      { id: 'm2', fromId: 'a1', fromName: '后端开发', text: 'SQL 分支已核对，缺一条回归用例。', at: now - 240 },
      { id: 'm3', fromId: 'a2', fromName: '测试工程师', text: '我补了 3 条，还差 prod 只读验证。', at: now - 120 },
    ],
  },
]

const feishu = [
  {
    id: 'bot_1',
    name: '值班助手',
    enabled: true,
    appId: 'cli_aa1ab7fbdd7912345',
    hasSecret: true,
    hasUser: true,
    agentId: 'a1',
    mentionOnly: true,
    status: 'connected',
    userName: '徐新志',
    userExpire: now + 7200,
    userRead: '用户身份能读到 12 个群、最近 200 条消息',
  },
]

const rooms = [
  {
    id: 'oc_1',
    name: '数采平台值班群',
    kind: 'group',
    agentId: 'a1',
    agentName: '后端开发',
    botId: 'bot_1',
    botName: '值班助手',
    messages: [
      { id: 'l1', chatId: 'oc_1', from: '张晓超', text: 'job-dduxcvryl6qc2roe 在任务管理里查不到', at: now - 600, kind: 'text' },
      { id: 'l2', chatId: 'oc_1', from: '值班助手', self: true, text: '**已定位**：`qc_tenant_id` 没打标，两条视图 SQL 都会过滤掉它。', at: now - 540, kind: 'text' },
      { id: 'l3', chatId: 'oc_1', from: '何宗茂', text: '子任务已建\n负责人：徐新志\n[卡片内容需在飞书客户端查看（card_id: 7xxxx）]', at: now - 300, kind: 'card' },
    ],
  },
]

const notes = [
  { id: 'n1', title: '催办查询慢SQL根因', body: 'approval_node.go:170 未走索引，先按租户收窄再批量查附属表。', tags: ['SQL'], related: ['a1'], at: now - 3000 },
  { id: 'n2', title: 'Web 工具能力边界', body: '内置浏览器是 WebKit，不是 Chrome；弹层选项要优先纳入 snapshot。', tags: ['浏览器'], related: ['a2'], at: now - 2000 },
]

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents, channels, notes, feishu })
  await page.addInitScript((rs) => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const wait = () => {
      if (!w.mygo) return setTimeout(wait, 5)
      const orig = w.mygo.call.bind(w.mygo)
      w.mygo.call = (m: string, ...a: unknown[]) => {
        if (m === 'API.ListFeishuRooms') return Promise.resolve(rs)
        return orig(m, ...a)
      }
    }
    wait()
  }, rooms)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/')
  await page.waitForSelector('.c-shell', { timeout: 10_000 })
})

async function shot(page: import('@playwright/test').Page, name: string) {
  await page.waitForTimeout(350)
  await page.screenshot({ path: `test-results/shots/${name}.png`, fullPage: false })
}

for (const theme of ['dark', 'light'] as const) {
  test(`视觉巡检 ${theme}`, async ({ page }) => {
    await page.evaluate((t) => document.documentElement.setAttribute('data-theme', t), theme)

    // 1. 舰队总览（默认视图）
    await shot(page, `${theme}-1-fleet`)

    // 2. 单会话对话
    await page.getByRole('button', { name: '会话 后端开发', exact: true }).click()
    await expect(page.locator('.markdown-body').first()).toBeVisible()
    await shot(page, `${theme}-2-chat`)

    // 3. 检查器（工具页）
    const inspectorBtn = page.getByRole('button', { name: /检查器|工具/ }).first()
    if (await inspectorBtn.count()) await inspectorBtn.click().catch(() => {})
    await shot(page, `${theme}-3-inspector`)

    // 4. 频道
    await page.getByRole('button', { name: '频道 发布评审' }).click()
    await shot(page, `${theme}-4-channel`)

    // 5. 飞书会话
    await page.getByRole('button', { name: '飞书会话 数采平台值班群' }).click()
    await shot(page, `${theme}-5-feishu`)

    // 6. 设置
    await page.getByRole('button', { name: /设置/ }).first().click()
    await shot(page, `${theme}-6-settings`)
    const feishuTab = page.getByRole('button', { name: '飞书' }).first()
    if (await feishuTab.count()) {
      await feishuTab.click().catch(() => {})
      await shot(page, `${theme}-7-settings-feishu`)
    }
    await page.keyboard.press('Escape')

    // 7. 新建会话弹窗
    await page.getByRole('button', { name: /新建 Agent/ }).first().click()
    await shot(page, `${theme}-8-create`)
    await page.keyboard.press('Escape')
  })
}