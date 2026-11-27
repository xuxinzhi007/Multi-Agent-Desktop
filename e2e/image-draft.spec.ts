import { expect, mockRuntime, test } from './mock-runtime'

const AGENT = {
  id: 'a1',
  name: '前端',
  role: 'React',
  color: '#5b9dff',
  status: 'idle',
  createdAt: 1,
  messages: [],
  queueLen: 0,
  backend: 'loop',
  runtime: 'native',
  workDir: '/tmp/project',
}

test('图片草稿可渲染并删除', async ({ page }) => {
  await mockRuntime(page, { agents: [AGENT], channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  const tab = page.getByRole('button', { name: /对话/ })
  if (await tab.count()) await tab.first().click()

  await page.locator('input[type="file"]').first().setInputFiles({
    name: 'pixel.png',
    mimeType: 'image/png',
    buffer: Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=', 'base64'),
  })

  await expect(page.locator('.c-image__telemetry')).toHaveText('1/8')
  const image = page.locator('.c-image__strip img')
  await expect(image).toBeVisible()
  await expect(image).toHaveAttribute('title', /1×1 · 约 .*KB · image\/png/)
  await page.getByRole('button', { name: '移除图片' }).click()
  await expect(page.locator('.c-image__strip')).toHaveCount(0)
  await expect(page.locator('.c-image__telemetry')).toHaveCount(0)
})
