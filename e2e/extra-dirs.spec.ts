import { expect, mockRuntime, test } from './mock-runtime'

const agents = [
  { id: 'a1', name: '后端开发', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp/main', extraDirs: ['/tmp/other'] },
  { id: 'a2', name: '空根会话', role: 'Go', color: '#4d8cf0', status: 'idle', createdAt: 2, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp/main', extraDirs: [] },
]

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents, channels: [], notes: [], feishu: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
})

test('工作区根弹层能看已有根，能加能删', async ({ page, callLog }) => {
  await page.getByRole('button', { name: '会话 后端开发', exact: true }).click()
  await page.getByTitle(/工作目录：/).click()

  const roots = page.locator('.c-extra')
  await expect(roots.locator('.c-extra__label')).toHaveText('工作区根')
  await expect(roots.getByText('/tmp/other')).toBeVisible()

  // 浏览… 回填草稿后，「再加一个工作区根」按钮才可点；目录选择复用 DirectoryPicker。
  await roots.getByRole('button', { name: '浏览…' }).click()
  await roots.getByRole('button', { name: '再加一个工作区根' }).click()

  let calls = await callLog()
  let set = calls.filter((c) => c.method === 'API.SetAgentExtraDirs')
  expect(set.length).toBe(1)
  expect(set[0].args).toEqual(['a1', ['/tmp/other', '/tmp/e2e-project']])

  await roots.getByLabel('移除工作区根 /tmp/other').click()
  calls = await callLog()
  set = calls.filter((c) => c.method === 'API.SetAgentExtraDirs')
  expect(set[1].args).toEqual(['a1', []])
})

test('空状态下能看到再加工作区根的入口', async ({ page, callLog }) => {
  await page.getByRole('button', { name: '会话 空根会话', exact: true }).click()
  await page.getByTitle(/工作目录：/).click()

  const roots = page.locator('.c-extra')
  // 没有附加根时给一行空状态提示，入口直接摆在面板里，不必先找到某个隐藏开关。
  await expect(roots.getByText('还没有其他工作区根，可以再加一个。')).toBeVisible()
  const addBtn = roots.getByRole('button', { name: '再加一个工作区根' })
  await expect(addBtn).toBeVisible()
  await expect(addBtn).toBeDisabled()

  await roots.getByRole('button', { name: '浏览…' }).click()
  await expect(addBtn).toBeEnabled()
  await addBtn.click()

  const calls = await callLog()
  const set = calls.filter((c) => c.method === 'API.SetAgentExtraDirs')
  expect(set.length).toBe(1)
  expect(set[0].args).toEqual(['a2', ['/tmp/e2e-project']])
})
