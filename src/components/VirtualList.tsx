import { useCallback, useEffect, useRef, useState, type ReactNode, type RefObject } from 'react'

const EST = 88
const OVER = 280

export function VirtualList<T>({
  items,
  scrollRef,
  resetKey,
  render,
}: {
  items: T[]
  scrollRef: RefObject<HTMLDivElement | null>
  resetKey: string
  render: (item: T, index: number) => ReactNode
}) {
  const heights = useRef<number[]>([])
  const [range, setRange] = useState({ start: 0, end: 12 })

  useEffect(() => {
    heights.current = []
  }, [resetKey])

  const measure = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const top = el.scrollTop
    const view = el.clientHeight || 640
    let y = 0
    let start = 0
    for (let i = 0; i < items.length; i++) {
      const h = heights.current[i] || EST
      if (y + h >= top - OVER) {
        start = i
        break
      }
      y += h
    }
    let end = start
    let seen = 0
    while (end < items.length && seen < view + OVER * 2) {
      seen += heights.current[end] || EST
      end++
    }
    if (end < items.length) end = Math.min(items.length, end + 2)
    setRange((prev) => (prev.start === start && prev.end === end ? prev : { start, end }))
  }, [items.length, scrollRef])

  useEffect(() => {
    measure()
    const el = scrollRef.current
    if (!el) return
    el.addEventListener('scroll', measure, { passive: true })
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => {
      el.removeEventListener('scroll', measure)
      ro.disconnect()
    }
  }, [measure, scrollRef, resetKey])

  let offset = 0
  for (let i = 0; i < range.start; i++) offset += heights.current[i] || EST
  let total = 0
  for (let i = 0; i < items.length; i++) total += heights.current[i] || EST

  return (
    <div style={{ height: Math.max(total, 0), position: 'relative' }}>
      <div style={{ position: 'absolute', top: offset, left: 0, right: 0 }}>
        {items.slice(range.start, range.end).map((item, i) => {
          const index = range.start + i
          return (
            <RowMeasure
              key={`${resetKey}:${index}`}
              onHeight={(h) => {
                if (heights.current[index] === h) return
                heights.current[index] = h
                measure()
              }}
            >
              {render(item, index)}
            </RowMeasure>
          )
        })}
      </div>
    </div>
  )
}

function RowMeasure({ onHeight, children }: { onHeight: (n: number) => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const cb = useRef(onHeight)
  cb.current = onHeight
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const report = () => cb.current(el.offsetHeight)
    report()
    const ro = new ResizeObserver(report)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return <div ref={ref}>{children}</div>
}
