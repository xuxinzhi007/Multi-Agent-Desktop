import { useEffect, useRef } from 'react'

/**
 * AmbientBackground —— 背景氛围层。
 *
 * 纯色深背景看着"平"。这里用两个缓慢漂移的径向渐变光斑 + 一层极细网格，
 * 让界面有空间纵深，接近 WebGPU / 创意作品集站点那种"有光的深色"。
 *
 * 为什么用 CSS 而不是 canvas/WebGL：
 *   这是桌面应用，GPU 资源要留给用户的真实工作负载（agent、终端、diff）。
 *   两个 transform 动画由合成器处理，不占主线程，滚动和流式输出都不掉帧。
 *   真需要粒子时再上 WebGL，不值得为氛围光引入一整个渲染管线。
 */
export default function AmbientBackground({ tint }: { tint?: string }) {
  const ref = useRef<HTMLDivElement>(null)

  // 用鼠标位置轻微推动光斑，产生"视差"手感；rAF 合帧避免高频 setState
  useEffect(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const el = ref.current
    if (!el) return

    let raf = 0
    let tx = 0
    let ty = 0
    let cx = 0
    let cy = 0

    const onMove = (e: PointerEvent) => {
      tx = (e.clientX / window.innerWidth - 0.5) * 2
      ty = (e.clientY / window.innerHeight - 0.5) * 2
      if (!raf) raf = requestAnimationFrame(step)
    }

    const step = () => {
      // 缓动追踪，避免光斑跟着鼠标抖动
      cx += (tx - cx) * 0.05
      cy += (ty - cy) * 0.05
      el.style.setProperty('--px', cx.toFixed(3))
      el.style.setProperty('--py', cy.toFixed(3))
      if (Math.abs(tx - cx) > 0.001 || Math.abs(ty - cy) > 0.001) {
        raf = requestAnimationFrame(step)
      } else {
        raf = 0
      }
    }

    window.addEventListener('pointermove', onMove, { passive: true })
    return () => {
      window.removeEventListener('pointermove', onMove)
      if (raf) cancelAnimationFrame(raf)
    }
  }, [])

  return (
    <div
      ref={ref}
      className="c-ambient"
      aria-hidden
      style={tint ? ({ ['--ambient-tint' as string]: tint } as React.CSSProperties) : undefined}
    >
      <div className="c-ambient__grid" />
      <div className="c-ambient__blob c-ambient__blob--a" />
      <div className="c-ambient__blob c-ambient__blob--b" />
      <div className="c-ambient__vignette" />
    </div>
  )
}
