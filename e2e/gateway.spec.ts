import { expect, mockRuntime, test, type GatewayMock, type GatewayMockApproval, type Page } from './mock-runtime'

/**
 * 网关页（内嵌形态）：状态来自 API.GatewayStatus / GatewayBindings，
 * 审批操作走 API.GatewayDecideApproval。mock 覆盖三种模式：
 * ok（已接入）/ listenError（端口占用降级）/ disabled（gw 未启用）。
 */

const AGENTS = [
  { id: 'gw-a1', name: '前端助手', status: 'idle', queueLen: 0, lastError: '' },
  { id: 'gw-a2', name: '后端助手', status: 'running', queueLen: 1, lastError: '' },
]

const APPROVAL: GatewayMockApproval = {
  id: 'gw-ap-1',
  agentId: 'gw-a2',
  agentName: '后端助手',
  tool: 'write_file',
  title: '写入 src/index.ts',
  detail: '覆盖 src/index.ts',
  diff: '+const x = 1\n-const x = 0',
  createdAt: Math.floor(Date.now() / 1000),
}

const BINDINGS = [
  { channel: 'web', externalChatId: 'web-1', agentId: 'gw-a1', policy: 'mention', createdAt: 1 },
  { channel: 'web', externalChatId: 'web-2', agentId: 'gw-a2', policy: 'open', createdAt: 2 },
]

async function openGateway(page: Page, gateway: GatewayMock) {
  await mockRuntime(page, { agents: [], channels: [], notes: [], gateway })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '网关' }).click()
  return page.locator('[data-testid="gateway-page"]')
}

test('已接入：概览 / 四端口 / 渠道 / 绑定 / 会话 / 待审批 / SSE 全部渲染', async ({ page }) => {
  const gw = await openGateway(page, {
    mode: 'ok',
    agents: AGENTS,
    approvals: [APPROVAL],
    bindings: BINDINGS,
  })

  await expect(gw.getByText('运行中').first()).toBeVisible()
  await expect(gw.getByText('端口占用')).toHaveCount(0)

  // 四端口全部已接线
  await expect(gw.getByText('已接线')).toHaveCount(4)

  // 接管渠道默认 web；绑定数 2
  await expect(gw.getByText('web', { exact: true }).first()).toBeVisible()
  await expect(gw.getByText('绑定 2')).toBeVisible()

  // Agent 列表
  await expect(gw.getByText('前端助手', { exact: true })).toBeVisible()
  await expect(gw.getByText('后端助手', { exact: true })).toBeVisible()

  // 待审批卡与 SSE 订阅数
  await expect(gw.getByText('写入 src/index.ts')).toBeVisible()
  await expect(gw.getByText('SSE 订阅 1')).toBeVisible()

  // 顶部状态条同步显示运行中
  await expect(page.getByText('网关 运行中')).toBeVisible()
})

test('允许一次：调用 GatewayDecideApproval 且卡片消失', async ({ page, callLog }) => {
  const gw = await openGateway(page, {
    mode: 'ok',
    agents: AGENTS,
    approvals: [APPROVAL],
    bindings: BINDINGS,
  })

  await expect(gw.getByText('写入 src/index.ts')).toBeVisible()
  await gw.getByRole('button', { name: '允许一次' }).click()

  await expect(gw.getByText('写入 src/index.ts')).toHaveCount(0)
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.GatewayDecideApproval'))
    .toEqual([{ method: 'API.GatewayDecideApproval', args: ['gw-ap-1', true, false, ''] }])
})

test('拒绝带原因：确认拒绝把 reason 带回后端', async ({ page, callLog }) => {
  const gw = await openGateway(page, {
    mode: 'ok',
    agents: AGENTS,
    approvals: [APPROVAL],
    bindings: BINDINGS,
  })

  await gw.getByRole('button', { name: '拒绝' }).click()
  const reasonBox = gw.getByPlaceholder('拒绝原因（可选，会带回后端）')
  await expect(reasonBox).toBeVisible()
  await reasonBox.fill('权限不足')
  await gw.getByRole('button', { name: '确认拒绝' }).click()

  await expect(gw.getByText('写入 src/index.ts')).toHaveCount(0)
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.GatewayDecideApproval'))
    .toEqual([{ method: 'API.GatewayDecideApproval', args: ['gw-ap-1', false, false, '权限不足'] }])
})

test('端口占用：显示监听失败原因，不显示运行中', async ({ page }) => {
  const gw = await openGateway(page, { mode: 'listenError', agents: [], approvals: [], bindings: [] })

  await expect(gw.getByText('端口占用')).toBeVisible()
  await expect(gw.getByText('运行中')).toHaveCount(0)
  await expect(gw.getByText('address already in use')).toBeVisible()
  await expect(page.getByText('网关 端口占用')).toBeVisible()
})

test('未启用：gateway:false 时展示未启用空态', async ({ page }) => {
  const gw = await openGateway(page, { mode: 'disabled' })

  await expect(gw.locator('.c-empty').getByText('网关未启用', { exact: true })).toBeVisible()
  await expect(gw.getByText('运行中')).toHaveCount(0)
  await expect(page.getByText('网关 未启用')).toBeVisible()
})
