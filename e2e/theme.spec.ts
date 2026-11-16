import { expect, mockRuntime, test } from './mock-runtime'

/**
 * 主题观感护栏。
 *
 * 亮色主题曾经发灰：底色是中性灰 #f2f3f5，玻璃层又继承了暗色的
 * 半透明黑，叠起来整屏像蒙了层灰雾。这里直接量实际渲染色值。
 */

const AGENTS = [
  {
    id: 'a1',
    name: '前端',
    role: 'React',
    color: '#5b9dff',
    status: 'idle',
    createdAt: 1,
    messages: [],
    queueLen: 0,
  },
]

async function setTheme(page: import('./mock-runtime').Page, theme: 'light' | 'dark') {
  await page.evaluate((t) => {
    document.documentElement.dataset.theme = t
  }, theme)
  await page.waitForTimeout(100)
}

/** 解析 rgb(a) 字符串为 [r,g,b] */
function rgb(css: string): [number, number, number] {
  const m = css.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/)
  if (!m) throw new Error(`无法解析颜色: ${css}`)
  return [Number(m[1]), Number(m[2]), Number(m[3])]
}

test.beforeEach(async ({ page }) => {
  await mockRuntime(page, { agents: AGENTS, channels: [], notes: [] })
  await page.goto('/')
  await page.waitForSelector('.c-shell')
})

test('亮色主题底色是近白，不是灰', async ({ page }) => {
  await setTheme(page, 'light')
  const bg = await page.evaluate(
    () => getComputedStyle(document.body).backgroundColor,
  )
  const [r, g, b] = rgb(bg)
  // 中性灰的特征是三个通道几乎相等且明显低于 250
  expect(r, `body 背景 ${bg}`).toBeGreaterThanOrEqual(245)
  expect(g, `body 背景 ${bg}`).toBeGreaterThanOrEqual(246)
  expect(b, `body 背景 ${bg}`).toBeGreaterThanOrEqual(247)
})

test('亮色主题下侧栏不是半透明黑', async ({ page }) => {
  await setTheme(page, 'light')
  const sidebarBg = await page.evaluate(() => {
    const el = document.querySelector('aside')
    return el ? getComputedStyle(el).backgroundColor : ''
  })
  const [r, g, b] = rgb(sidebarBg)
  // 曾经继承暗色玻璃 rgba(16,19,24,.72) → 数值很低
  expect(r, `侧栏背景 ${sidebarBg}`).toBeGreaterThan(200)
  expect(g, `侧栏背景 ${sidebarBg}`).toBeGreaterThan(200)
  expect(b, `侧栏背景 ${sidebarBg}`).toBeGreaterThan(200)
})

test('亮色主题氛围层暗角不是灰雾', async ({ page }) => {
  await setTheme(page, 'light')
  const vars = await page.evaluate(() => {
    const cs = getComputedStyle(document.documentElement)
    return {
      vignette: cs.getPropertyValue('--ambient-vignette').trim(),
      grid: cs.getPropertyValue('--ambient-grid').trim(),
    }
  })
  // 暗角不该等于底色（那样会在四周压出一圈灰）
  expect(vars.vignette).not.toContain('--bg-base')
  expect(vars.vignette.length).toBeGreaterThan(0)
  expect(vars.grid.length).toBeGreaterThan(0)
})

test('两套主题的文字对比度都达标', async ({ page }) => {
  for (const theme of ['light', 'dark'] as const) {
    await setTheme(page, theme)
    const colors = await page.evaluate(() => {
      const cs = getComputedStyle(document.documentElement)
      return {
        primary: cs.getPropertyValue('--text-primary').trim(),
        base: cs.getPropertyValue('--bg-base').trim(),
      }
    })
    const p = rgb(
      colors.primary.startsWith('#')
        ? `rgb(${parseInt(colors.primary.slice(1, 3), 16)},${parseInt(colors.primary.slice(3, 5), 16)},${parseInt(colors.primary.slice(5, 7), 16)})`
        : colors.primary,
    )
    const b = rgb(
      colors.base.startsWith('#')
        ? `rgb(${parseInt(colors.base.slice(1, 3), 16)},${parseInt(colors.base.slice(3, 5), 16)},${parseInt(colors.base.slice(5, 7), 16)})`
        : colors.base,
    )
    // 相对亮度近似
    const lum = (c: number[]) => {
      const [r, g, bl] = c.map((v) => {
        const s = v / 255
        return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
      })
      return 0.2126 * r + 0.7152 * g + 0.0722 * bl
    }
    const l1 = lum(p)
    const l2 = lum(b)
    const ratio = (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
    expect(ratio, `${theme} 主题文字/背景对比度 ${ratio.toFixed(2)}`).toBeGreaterThan(7)
  }
})
