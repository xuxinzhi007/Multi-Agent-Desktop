import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 飞书房间视图：机器人输出（text/post/card）渲染 markdown，用户消息保持纯文本。
 * 修的是 post 类机器人消息此前纯文本直出、markdown 符号裸露的缺口。
 */

const ROOMS = [
  {
    id: 'chat1',
    name: '测试群',
    kind: 'group',
    botId: 'bot1',
    messages: [
      { id: 'l1', chatId: 'chat1', from: '机器人', fromId: 'bot1', self: true, kind: 'post', text: '## 评估结论\n\n| 项 | 结果 |\n|---|---|\n| 方案 | 需调整 |', at: 100 },
      { id: 'l2', chatId: 'chat1', from: '张三', fromId: 'u1', self: false, kind: 'post', text: '## 这是我打的标题符号', at: 101 },
      { id: 'l3', chatId: 'chat1', from: '机器人', fromId: 'bot1', self: true, kind: 'text', text: '### 文本回复\n\n**加粗**', at: 102 },
    ],
  },
]

async function openRoom(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '飞书会话 测试群' }).click()
  await page.waitForTimeout(400)
}

test('飞书 post 类机器人消息渲染 markdown', async ({ page }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [], feishu: ROOMS })
  await openRoom(page)
  await expect(page.locator('.markdown-body h2', { hasText: '评估结论' })).toBeVisible()
  await expect(page.locator('.markdown-body table')).toBeVisible()
})

test('飞书 post 类用户消息保持纯文本', async ({ page }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [], feishu: ROOMS })
  await openRoom(page)
  // 用户那条 post 不该被渲染成 h2
  await expect(page.locator('.markdown-body h2', { hasText: '这是我打的标题符号' })).toHaveCount(0)
  await expect(page.getByText('## 这是我打的标题符号')).toBeVisible()
})

test('飞书 text 类机器人消息仍渲染 markdown', async ({ page }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [], feishu: ROOMS })
  await openRoom(page)
  await expect(page.locator('.markdown-body h3', { hasText: '文本回复' })).toBeVisible()
})

test('飞书每条消息正文都落在带边框的消息卡片里', async ({ page }) => {
  await mockRuntime(page, { agents: [], channels: [], notes: [], feishu: ROOMS })
  await openRoom(page)
  const cards = page.locator('.c-transcript .c-msg-card')
  await expect(cards).toHaveCount(ROOMS[0].messages.length)
  const border = await cards.first().evaluate((el) => getComputedStyle(el).borderTopWidth)
  expect(border).toBe('1px')
  await expect(cards.first()).toContainText('评估结论')
})