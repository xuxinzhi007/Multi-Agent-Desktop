import type { ReactNode } from 'react'

// 一页消息按文档流往下排。绝对定位会按估算高度叠在长回复上。
export function VirtualList<T>({
  items,
  resetKey,
  render,
}: {
  items: T[]
  resetKey: string
  render: (item: T, index: number) => ReactNode
}) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'stretch', width: '100%' }}>
      {items.map((item, index) => (
        <div key={`${resetKey}:${index}`} style={{ width: '100%' }}>
          {render(item, index)}
        </div>
      ))}
    </div>
  )
}
