import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 行内 @ 提及：胶囊插入 + 后置空格、Backspace 原子删除、多段指令、发送文本即正文。
 */

const AGENTS = [
  { id: 'a1', name: '前端', role: 'React', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
  { id: 'a2', name: '后端', role: 'Go', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0, backend: 'loop', workDir: '/tmp' },
]

const CHANNELS = [
  {
    id: 'ch1',
    name: '评审群',
    members: ['a1', 'a2'],
    policy: 'discuss',
    speaker: 'a2',
    messages: [],
  },
]

function suggest(page: import('@playwright/test').Page) {
  return page.getByRole('listbox', { name: '提及成员' })
}

async function pick(page: import('@playwright/test').Page, name: string) {
  await suggest(page).getByRole('option', { name: new RegExp(name) }).click()
}

async function openChannel(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.locator('div').filter({ hasText: /^评审群/ }).first().click()
  await page.waitForTimeout(400)
  return page.getByLabel(/发消息到 #评审群/)
}

test('选中成员后正文出现胶囊且无残留@', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  const editor = await openChannel(page)

  await editor.click()
  await editor.pressSequentially('@后')
  await pick(page, '后端')

  const chip = page.locator('.c-mention-chip')
  await expect(chip).toHaveCount(1)
  await expect(chip).toHaveText('@后端')
  // 胶囊 + 一个后置空格，没有多余的 @ 残留
  await expect(editor).toHaveText('@后端')
  const html = await editor.innerHTML()
  expect(html).toContain('data-type="mention"')
})

test('Backspace 一次删掉整个胶囊', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  const editor = await openChannel(page)

  await editor.click()
  await editor.pressSequentially('@后')
  await pick(page, '后端')
  await expect(page.locator('.c-mention-chip')).toHaveCount(1)

  // 第一下删后置空格，第二下整块删胶囊（不是逐字删 6 个字符）
  await editor.press('Backspace')
  await expect(page.locator('.c-mention-chip')).toHaveCount(1)
  await editor.press('Backspace')
  await expect(page.locator('.c-mention-chip')).toHaveCount(0)
  await expect(editor).toHaveText('')
})

test('一句话多段@各自成胶囊并原样发送', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  const editor = await openChannel(page)

  await editor.click()
  await editor.pressSequentially('@前')
  await pick(page, '前端')
  await editor.pressSequentially('看代码，@后')
  await pick(page, '后端')
  await editor.pressSequentially('跑测试')

  await expect(page.locator('.c-mention-chip')).toHaveCount(2)
  await expect(page.getByText('将点名：@前端 @后端', { exact: false })).toBeVisible()

  await page.getByRole('button', { name: '发送' }).click()
  await page.waitForTimeout(300)
  const calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const sent = calls.filter((c) => c.method === 'API.SendToChannel')
  expect(sent.length).toBe(1)
  expect(sent[0].args[3]).toBe('@前端 看代码，@后端 跑测试')
  // 发送后输入框清空
  await expect(page.locator('.c-mention-chip')).toHaveCount(0)
})

test('键盘 Enter 在弹层开着时选人、关着时发送', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: CHANNELS, notes: [] })
  const editor = await openChannel(page)

  await editor.click()
  await editor.pressSequentially('@后')
  await expect(suggest(page).getByRole('option', { name: /后端/ })).toBeVisible()
  await editor.press('Enter') // 选中，不发送
  await expect(page.locator('.c-mention-chip')).toHaveCount(1)
  let calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  expect(calls.filter((c) => c.method === 'API.SendToChannel').length).toBe(0)

  await editor.pressSequentially('在吗')
  await editor.press('Enter') // 发送
  await page.waitForTimeout(300)
  calls = await page.evaluate(() => (window as unknown as { __mockCalls: { method: string; args: unknown[] }[] }).__mockCalls)
  const sent = calls.filter((c) => c.method === 'API.SendToChannel')
  expect(sent.length).toBe(1)
  expect(sent[0].args[3]).toBe('@后端 在吗')
})

test('回填的历史消息里 @名字 还原成胶囊', async ({ page }) => {
  await mockRuntime(page, {
    agents: AGENTS,
    channels: [
      {
        ...CHANNELS[0],
        messages: [
          { id: 'm1', channelId: 'ch1', fromId: 'user', fromName: '你', text: '@前端 看下这个', at: 100 },
        ],
      },
    ],
    notes: [],
  })
  const editor = await openChannel(page)

  await page.getByTitle('点击回填到输入框，修改后重发').click()
  await expect(page.locator('.c-mention-chip', { hasText: '@前端' })).toHaveCount(1)
  await expect(editor).toContainText('看下这个')
})