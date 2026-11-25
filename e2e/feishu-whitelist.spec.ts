import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 飞书审批白名单设置页护栏（SettingsModal · FeishuBoard）：
 *  - ownerOpenId 只读展示，来自 FeishuView.ownerOpenId；
 *  - approverOpenIds 每行一个 open_id 编辑，随 SaveFeishu 提交；
 *  - 去空行、去重；后端错误原文展示。
 */

const BOT = {
  id: 'b1',
  name: '默默',
  enabled: true,
  appId: 'cli_a9ad56c361ba9bc6',
  hasSecret: true,
  hasUser: true,
  agentId: '',
  mentionOnly: false,
  status: 'connected',
  ownerOpenId: 'ou_owner_123',
  approverOpenIds: ['ou_one', 'ou_two'],
  userName: '小新',
}

async function openFeishuBoard(page: import('./mock-runtime').Page) {
  await page.locator('aside').first().getByRole('button').filter({ hasText: '⚙' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('navigation', { name: '设置分类' }).getByRole('button', { name: '飞书' }).click()
  await dialog.getByRole('button', { name: '默默' }).click()
  return dialog
}

test('Owner 只读展示；白名单去空行去重后随 SaveFeishu 提交', async ({ page, callLog }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [], feishu: [BOT] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  const dialog = await openFeishuBoard(page)

  const owner = dialog.getByLabel('审批 Owner OpenID')
  await expect(owner).toBeVisible()
  await expect(owner).toHaveValue('ou_owner_123')
  await expect(owner).toBeDisabled()

  const approvers = dialog.getByLabel('审批白名单 open_id')
  await expect(approvers).toHaveValue('ou_one\nou_two')

  // 空行、重复项都要被清掉；顺序保留首次出现
  await approvers.fill('ou_one\n\nou_two\nou_one\nou_three')
  await dialog.getByRole('button', { name: '保存' }).click()

  await expect
    .poll(async () => {
      const calls = await callLog()
      return calls.some((c) => c.method === 'API.SaveFeishu')
    })
    .toBe(true)

  const calls = await callLog()
  const req = calls.find((c) => c.method === 'API.SaveFeishu')!.args[0] as {
    ownerOpenId?: string
    approverOpenIds?: string[]
  }
  expect(req.ownerOpenId).toBe('ou_owner_123')
  expect(req.approverOpenIds).toEqual(['ou_one', 'ou_two', 'ou_three'])
})

test('保存失败时后端错误原文直接显示在面板上', async ({ page }) => {
  await mockRuntime(page, {
    agents: [],
    channels: [],
    notes: [],
    feishu: [BOT],
    feishuError: '保存失败：AppSecret 校验未通过',
  })
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  const dialog = await openFeishuBoard(page)
  await dialog.getByLabel('审批白名单 open_id').fill('ou_three')
  await dialog.getByRole('button', { name: '保存' }).click()

  await expect(dialog.getByText('保存失败：AppSecret 校验未通过')).toBeVisible()
})
