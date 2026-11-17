import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 诊断用户首次上手体验。
 *
 * 这里不追求"通过"，而是把用户必须经历的点击路径显式写出来，
 * 让摩擦点变成可度量的数字。任何一步需要多余的操作，都会体现为断言数字变大。
 */

test.beforeEach(async ({ page }) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell', { timeout: 10_000 })
})

test('首次启动：空状态必须提供可直接点击的下一步', async ({ page }) => {
  // 空状态文案存在
  await expect(page.getByText('组建你的 agent 舰队')).toBeVisible()

  // 空状态区域内是否有可点击动作？这是首次使用的唯一出口
  const actionable = page
    .locator('.c-empty')
    .getByRole('button')
  const count = await actionable.count()
  expect(count, '空状态里没有任何按钮，用户只能自己去找左下角的「+ 新建 Agent」').toBeGreaterThan(0)
})

test('首次上手：从打开应用到能发出第一条消息的点击数', async ({ page }) => {
  let clicks = 0
  page.on('click', () => clicks++)

  // 用户看到空状态后的唯一路径：找到并点击创建入口
  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // 创建时必须至少手填名称 —— 用户此刻还不知道该起什么名字
  const nameInput = dialog.getByPlaceholder('例如：后端开发')
  await expect(nameInput).toBeVisible()
  const nameValue = await nameInput.inputValue()
  expect(nameValue, '名称未被预填，用户必须自己想一个名字').not.toBe('')

  await dialog.getByRole('button', { name: '创建' }).click()
  await expect(dialog).toBeHidden()

  // 到能输入消息为止的点击次数
  expect(clicks, `首次上手需要 ${clicks} 次点击才能开始对话`).toBeLessThanOrEqual(3)
})

test('创建表单：角色预设应当可以一键选，而不是只能靠下拉', async ({ page }) => {
  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')

  // 角色预设若以按钮/卡片形式呈现，用户一眼可选
  // 角色卡片带图标与一句话描述，用 role 组定位
  const presetButtons = dialog.getByRole('group', { name: '角色' }).getByRole('button')
  const n = await presetButtons.count()
  expect(n, '角色只能通过下拉选择，无法一键点选常用预设').toBeGreaterThan(0)
})

test('新建 agent 时能看到 Agent Loop 这个 backend', async ({ page }) => {
  // 已有 beforeEach 导航，不要再 goto
  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  // 自持循环是唯一能真正中断的 backend，不能从界面上消失
  await expect(dialog.getByText('Agent Loop')).toBeVisible()
})
