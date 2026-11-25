import { expect, mockRuntime, test, type MemoryMock, type Page } from './mock-runtime'

/**
 * 记忆页（M1 / internal/memory）：状态来自 API.MemoryStatus，
 * 搜索走 API.MemorySearch，最近 / 笔记走 MemoryRecent / MemoryNotes，
 * 笔记增删走 MemoryUpsertNote / MemoryDeleteNote。
 *
 * mock 覆盖：ok（已接入）/ disabled（记忆库未启用）/ error（状态异常）。
 * 记忆与知识库是两套数据，本套件不碰 note.* 知识库接口。
 */

const AGENTS = [
  { id: 'a1', name: '前端', role: 'React', color: '#5b9dff', status: 'idle', createdAt: 1, messages: [], queueLen: 0 },
  { id: 'a2', name: '后端', role: 'Go', color: '#3ecf8e', status: 'idle', createdAt: 2, messages: [], queueLen: 0 },
]

const RECENT = [
  { id: 1, kind: 'message', source: 'agent', agentId: 'a1', snippet: '用户问：慢 SQL 为什么没走索引', createdAt: 1700000001 },
  { id: 2, kind: 'message', source: 'agent', agentId: 'a2', snippet: '助手回复：先看 EXPLAIN 的 type 列', createdAt: 1700000002 },
  { id: 3, kind: 'fact', source: 'agent', agentId: 'a1', title: '偏好', snippet: '交付文档用飞书 Markdown 表格', createdAt: 1700000003 },
]

const SEARCH = [
  { id: 101, kind: 'message', source: 'agent', agentId: 'a1', snippet: '慢 SQL 排查：先看 EXPLAIN 的索引命中', createdAt: 1700000101 },
  { id: 102, kind: 'message', source: 'agent', agentId: 'a2', snippet: '周末去爬山，路线选岳麓山', createdAt: 1700000102 },
]

const NOTES = [
  {
    id: 10,
    title: '接口错误约定',
    body: '统一用 wrapError 包一层',
    tags: '约定,后端',
    source: 'user',
    createdAt: 1700000010,
    updatedAt: 1700000010,
  },
]

async function openMemory(page: Page, memory: MemoryMock) {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [], memory })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.getByRole('button', { name: '记忆' }).click()
  return page.locator('[data-testid="memory-page"]')
}

test('已接入：状态计数 / 最近记忆 / 笔记全部渲染', async ({ page }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await expect(mem.getByText('已接入')).toBeVisible()
  // 状态卡：事件 3 / 笔记 1 / 事实 2
  await expect(mem.getByText('事件 3')).toBeVisible()
  await expect(mem.getByText('笔记 1')).toBeVisible()
  await expect(mem.getByText('事实 2')).toBeVisible()

  // 最近记忆三条
  await expect(mem.getByText('慢 SQL 为什么没走索引')).toBeVisible()
  await expect(mem.getByText('先看 EXPLAIN 的 type 列')).toBeVisible()
  await expect(mem.getByText('交付文档用飞书 Markdown 表格')).toBeVisible()

  // 笔记一条
  await expect(mem.getByText('接口错误约定')).toBeVisible()
  await expect(mem.getByText('#约定')).toBeVisible()
})

test('搜索命中：带上关键词与 limit 调 MemorySearch，结果上屏', async ({ page, callLog }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await mem.getByPlaceholder('搜索记忆内容（关键词）…').fill('索引')
  await mem.getByRole('button', { name: '搜索' }).click()

  await expect(mem.getByText('慢 SQL 排查：先看 EXPLAIN 的索引命中')).toBeVisible()
  await expect(mem.getByText('周末去爬山')).toHaveCount(0)

  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.MemorySearch'))
    .toEqual([{ method: 'API.MemorySearch', args: ['索引', '', 20] }])
})

test('搜索空态：无命中时明确提示，不编造结果', async ({ page }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await mem.getByPlaceholder('搜索记忆内容（关键词）…').fill('不存在的关键词 xyz')
  await mem.getByRole('button', { name: '搜索' }).click()

  await expect(mem.getByText('没有找到相关记忆。')).toBeVisible()
})

test('按 Agent 过滤：下拉选中后 MemorySearch 带上 agentId', async ({ page, callLog }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await mem.getByPlaceholder('搜索记忆内容（关键词）…').fill('SQL')
  await mem.getByRole('button', { name: '全部 Agent' }).click()
  await mem.getByRole('option', { name: '前端' }).click()
  await mem.getByRole('button', { name: '搜索' }).click()

  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.MemorySearch'))
    .toEqual([{ method: 'API.MemorySearch', args: ['SQL', 'a1', 20] }])
})

test('新增笔记：调 MemoryUpsertNote 并出现在笔记列表', async ({ page, callLog }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await mem.getByPlaceholder('笔记标题…').fill('前端排版约定')
  await mem.getByPlaceholder('笔记内容…').fill('卡片间距统一用 token')
  await mem.getByPlaceholder('标签，逗号分隔…').fill('前端, 约定')
  await mem.getByRole('button', { name: '保存笔记' }).click()

  await expect(mem.getByText('前端排版约定')).toBeVisible()
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.MemoryUpsertNote'))
    .toEqual([{ method: 'API.MemoryUpsertNote', args: ['前端排版约定', '卡片间距统一用 token', '前端, 约定'] }])
})

test('删除笔记：调 MemoryDeleteNote 且条目消失', async ({ page, callLog }) => {
  const mem = await openMemory(page, { mode: 'ok', recent: RECENT, notes: NOTES, search: SEARCH })

  await expect(mem.getByText('接口错误约定')).toBeVisible()
  await mem.getByTitle('删除笔记').click()

  await expect(mem.getByText('接口错误约定')).toHaveCount(0)
  await expect
    .poll(async () => (await callLog()).filter((c) => c.method === 'API.MemoryDeleteNote'))
    .toEqual([{ method: 'API.MemoryDeleteNote', args: [10] }])
})

test('笔记保存失败：后端错误原文上屏', async ({ page }) => {
  const mem = await openMemory(page, {
    mode: 'ok',
    recent: RECENT,
    notes: NOTES,
    search: SEARCH,
    upsertError: '笔记写入失败：数据库已锁定',
  })

  await mem.getByPlaceholder('笔记标题…').fill('标题')
  await mem.getByPlaceholder('笔记内容…').fill('正文')
  await mem.getByRole('button', { name: '保存笔记' }).click()

  await expect(mem.getByText('笔记写入失败：数据库已锁定')).toBeVisible()
})

test('未启用：disabled 时展示未接入空态，保留跳知识库入口', async ({ page }) => {
  const mem = await openMemory(page, { mode: 'disabled' })

  await expect(mem.getByText('记忆后端尚未接入')).toBeVisible()
  await expect(mem.getByRole('button', { name: '打开知识库' })).toBeVisible()
  await expect(mem.getByText('事件 3')).toHaveCount(0)
})

test('状态异常：error 模式把后端错误原因带到页面', async ({ page }) => {
  const mem = await openMemory(page, { mode: 'error' })

  await expect(mem.getByText('打开记忆库失败：disk I/O error')).toBeVisible()
})

test('状态读取报错：进入错误态并可重试', async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
  await page.evaluate(() => {
    const w = window as unknown as { mygo: { call: (m: string, ...a: unknown[]) => Promise<unknown> } }
    const orig = w.mygo.call
    w.mygo.call = (method: string, ...args: unknown[]) => {
      if (method === 'API.MemoryStatus') return Promise.reject(new Error('记忆库连接断开'))
      return orig(method, ...args)
    }
  })
  await page.getByRole('button', { name: '记忆' }).click()
  const mem = page.locator('[data-testid="memory-page"]')

  await expect(mem.getByText('记忆库连接断开')).toBeVisible()
  await expect(mem.getByRole('button', { name: '重试' })).toBeVisible()
})
