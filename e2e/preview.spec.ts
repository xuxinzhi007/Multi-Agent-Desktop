import { expect, test } from '@playwright/test'

// 这条不 mock runtime，模拟用户直接用浏览器打开 npm run dev
test('浏览器直接打开不白屏（无 MyGo runtime 时降级）', async ({ page }) => {
  const errors: string[] = []
  page.on('pageerror', (e) => errors.push(e.message))

  await page.goto('/')
  await page.waitForTimeout(1500)

  // 不应该是错误页
  const body = await page.locator('body').innerText()
  expect(body, '页面渲染成了错误页').not.toContain('runtime not found')
  expect(body).not.toContain('Something went wrong')

  // 主框架应该在
  await expect(page.locator('.c-shell')).toBeVisible()
  expect(errors.filter((e) => e.includes('runtime not found'))).toHaveLength(0)
})
