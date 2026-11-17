import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 冒烟测试：覆盖最近反复出问题的地方。
 *
 * 浏览器里没有 MyGo runtime，所以先 mock 掉。
 * 这些断言不依赖后端逻辑，只验证 UI 结构 —— 正是最常搞坏的部分。
 */

test.beforeEach(async ({ page }) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell', { timeout: 10_000 })
})

test('应用能渲染出主框架（不是白屏）', async ({ page }) => {
  await expect(page.locator('.c-shell')).toBeVisible()
  await expect(page.locator('aside').first()).toBeVisible()
})

test('新建 Agent 弹窗能打开且表单字段可见', async ({ page }) => {
  await page.getByRole('button', { name: /新建 Agent/ }).click()

  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('新建 Agent')).toBeVisible()

  // 三个字段必须可见 —— 上一次回归成「只剩标题和按钮」
  await expect(dialog.getByPlaceholder('例如：后端开发')).toBeVisible()
  // 角色改为一键点选的预设按钮组（不再藏在下拉里）
  const roleGroup = dialog.getByRole('group', { name: '角色' })
  await expect(roleGroup).toBeVisible()
  await expect(roleGroup.getByRole('button', { name: '后端' })).toBeVisible()
  await expect(dialog.locator('label', { hasText: '工作目录' })).toBeVisible()

  // 弹窗必须在视口内（transform 被动画覆盖时会被推出屏幕）
  const box = await dialog.boundingBox()
  const viewport = page.viewportSize()
  expect(box).not.toBeNull()
  if (box && viewport) {
    expect(box.x).toBeGreaterThanOrEqual(-1)
    expect(box.y).toBeGreaterThanOrEqual(-1)
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 1)
    expect(box.y + box.height).toBeLessThanOrEqual(viewport.height + 1)
  }

  // 名称已被预填，创建按钮直接可用（用户不必先想一个名字）
  const createBtn = dialog.getByRole('button', { name: '创建' })
  await expect(createBtn).toBeEnabled()
  await dialog.getByPlaceholder('例如：后端开发').fill('测试 agent')
  await expect(createBtn).toBeEnabled()
})

test('新建 Agent 弹窗 Esc 可关闭', async ({ page }) => {
  await page.getByRole('button', { name: /新建 Agent/ }).click()
  await expect(page.getByRole('dialog')).toBeVisible()

  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toBeHidden()
})

test('提交新建 Agent 会调用 CreateAgent', async ({ page, callLog }) => {
  await page.getByRole('button', { name: /新建 Agent/ }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByPlaceholder('例如：后端开发').fill('后端一号')
  await dialog.getByRole('button', { name: '创建' }).click()

  await expect(page.getByRole('dialog')).toBeHidden()
  const calls = await callLog()
  expect(calls.some((c) => c.method === 'API.CreateAgent')).toBe(true)
})

test('设置弹窗能打开且分组可见', async ({ page }) => {
  // 设置入口是侧栏顶部的图标按钮
  await page.locator('aside').first().getByRole('button').filter({ hasText: '⚙' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  // 左侧分类导航三个入口都在，默认落在「外观」
  const nav = page.getByRole('navigation', { name: '设置分类' })
  await expect(nav.getByRole('button', { name: '外观' })).toBeVisible()
  await expect(nav.getByRole('button', { name: /模型/ })).toBeVisible()
  await expect(nav.getByRole('button', { name: '关于' })).toBeVisible()
  await expect(page.getByRole('radiogroup', { name: '主题' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toBeHidden()
})

test('没有横向滚动条（布局不溢出）', async ({ page }) => {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  )
  expect(overflow).toBe(false)
})

test('没有未捕获的 JS 异常', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))
  await page.reload()
  await page.waitForSelector('.c-shell')
  await page.waitForTimeout(500)
  expect(errors).toEqual([])
})

test('设置面板告知用户密钥保存在哪', async ({ page }) => {
  await page.locator('aside').first().getByRole('button').filter({ hasText: '⚙' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // 用户有权知道 API Key 落在哪个文件 —— 路径在「关于」页
  await dialog.getByRole('navigation', { name: '设置分类' })
    .getByRole('button', { name: '关于' })
    .click()
  await expect(dialog.getByText(/config\.json/)).toBeVisible()
})

test('设置里有诊断页，能自己看出为什么不动', async ({ page }) => {
  await page.getByRole('button', { name: '设置' }).first().click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()

  // 「发出去一直排队」时，用户需要一个能自查的地方，
  // 而不是反复重启碰运气。
  await dialog.getByRole('button', { name: /诊断/ }).click()
  await expect(dialog.getByText('链路自检')).toBeVisible()
  // 必须显示后端版本 —— 区分「修复无效」和「跑的是旧二进制」
  await expect(dialog.getByText('test-build')).toBeVisible()
})

test('模型列表：点一下就切换，不需要先选 provider 再填凭据', async ({ page }) => {
  await page.getByRole('button', { name: '设置' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: /模型/ }).first().click()

  const list = dialog.getByRole('listbox', { name: '可用模型' })
  await expect(list).toBeVisible()

  // 每一项只显示模型名，不夹带 BaseURL / Key 之类的配置项
  const item = list.getByRole('option').filter({ hasText: 'deepseek-chat' })
  await expect(item).toBeVisible()

  const calls: string[] = []
  await page.exposeFunction('__rec', (m: string) => calls.push(m))
  await page.evaluate(() => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = w.mygo.call
    w.mygo.call = (method: string, ...args: unknown[]) => {
      void (window as unknown as { __rec: (m: string) => void }).__rec(method)
      return orig(method, ...args)
    }
  })

  await item.click()
  await page.waitForTimeout(300)

  // 点一下必须直接落盘，不能再要用户按一次「保存」
  expect(
    calls.some((c) => c === 'API.SetLLMConfig'),
    `点选模型后没有保存，实际调用: ${calls.join(',')}`,
  ).toBe(true)
})
