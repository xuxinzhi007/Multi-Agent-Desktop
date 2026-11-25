import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 审批卡归属（总纲 §3.5）：子会话的卡标明「子会话 X · 属于主会话 Y」，
 * 按钮文案「本子会话允许」；主会话保持原文案。安全校验是后端，这里只验显示。
 */

const AGENTS = [
  { id: 'root', name: '主会话', role: 'r', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0 },
  { id: 'sub', name: '后端协助', role: 'r', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0, parentId: 'root' },
]

function emitApproval(page: import('@playwright/test').Page, agentId: string, agentName: string) {
  return page.evaluate(
    ({ agentId, agentName }) => {
      const mygo = (window as unknown as { mygo: { emit: (n: string, d: unknown) => void } }).mygo
      mygo.emit('tool.approval', {
        id: 'ap-x',
        agentId,
        agentName,
        tool: 'write_file',
        title: '写入文件',
        detail: '覆盖 a.ts',
        createdAt: Date.now() / 1000,
      })
    },
    { agentId, agentName },
  )
}

test('子会话审批卡标明归属与本子会话允许', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await emitApproval(page, 'sub', '后端协助')

  const dialog = page.getByRole('dialog', { name: '写操作审批' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByText('子会话', { exact: true })).toBeVisible()
  await expect(dialog.getByText('属于主会话 主会话')).toBeVisible()
  await expect(dialog.getByRole('button', { name: '本子会话允许' })).toBeVisible()
})

test('主会话审批卡保持原文案', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await emitApproval(page, 'root', '主会话')

  const dialog = page.getByRole('dialog', { name: '写操作审批' })
  await expect(dialog.getByText('主会话 想执行一次写操作')).toBeVisible()
  await expect(dialog.getByRole('button', { name: '本会话允许', exact: true })).toBeVisible()
  await expect(dialog.getByText('子会话', { exact: true })).toHaveCount(0)
})