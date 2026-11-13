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
  await expect(dialog.locator('select').first()).toBeVisible()
  await expect(dialog.locator('label', { hasText: '后端' })).toBeVisible()

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

  // 名称填了之后「创建」按钮才可用
  const createBtn = dialog.getByRole('button', { name: '创建' })
  await expect(createBtn).toBeDisabled()
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
  expect(calls.some((c) => c.method === 'Sidecar.CreateAgent')).toBe(true)
})

test('设置弹窗能打开且分组可见', async ({ page }) => {
  // 设置入口是侧栏顶部的图标按钮
  await page.locator('aside').first().getByRole('button').filter({ hasText: '⚙' }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  await expect(page.getByText('外观', { exact: true })).toBeVisible()
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
