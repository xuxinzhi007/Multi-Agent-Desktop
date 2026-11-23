import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 写操作审批卡：后端发 tool.approval，界面必须拦住并让人决定。
 * 决定送达后卡片立刻收起（后端随后的 done 是幂等的）。
 */

test('审批卡出现、展示 diff、允许一次后送达并收起', async ({ page, callLog }) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('tool.approval', {
      id: 'ap-1',
      agentId: 'a1',
      agentName: '前端',
      tool: 'write_file',
      title: '写入文件',
      detail: '覆盖 src/app.ts：10 字节 → 20 字节',
      diff: '--- a/src/app.ts\n+++ b/src/app.ts\n@@\n-old\n+hello',
      createdAt: Date.now() / 1000,
    })
  })

  const dialog = page.getByRole('dialog', { name: '写操作审批' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('前端 想执行一次写操作')).toBeVisible()
  await expect(dialog.getByText('改动预览')).toBeVisible()
  await expect(dialog.getByText('需要你的批准')).toBeVisible()

  await dialog.getByRole('button', { name: '允许一次' }).click()
  await expect(dialog).toBeHidden()

  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.DecideApproval'))
    .toEqual([{ method: 'API.DecideApproval', args: ['ap-1', true, false] }])
})

test('拒绝也是显式决定，不是关掉弹窗', async ({ page, callLog }) => {
  await mockRuntime(page)
  await page.goto('/')
  await page.waitForSelector('.c-shell')

  await page.evaluate(() => {
    const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
    mygo.emit('tool.approval', {
      id: 'ap-2',
      agentId: 'a1',
      agentName: '后端',
      tool: 'bash',
      title: '运行命令',
      detail: '目录：/tmp\nrm -rf build',
      createdAt: Date.now() / 1000,
    })
  })

  const dialog = page.getByRole('dialog', { name: '写操作审批' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('这条命令没有文件 diff，执行结果会写进审计日志。')).toBeVisible()
  await dialog.getByRole('button', { name: '拒绝' }).click()
  await expect(dialog).toBeHidden()

  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.DecideApproval'))
    .toEqual([{ method: 'API.DecideApproval', args: ['ap-2', false, false] }])
})
