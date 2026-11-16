import { useEffect, useRef, useState } from 'react'
import { onFileDrop } from 'mygo-runtime'

/**
 * DropZone —— 接收从 Finder / Explorer 拖进来的文件或目录。
 *
 * 为什么需要它：目录选择只给一个「浏览…」按钮，用户还得多点一层系统对话框。
 * 直接把手上的项目文件夹拖进来是更自然的手势。
 *
 * 用 mygo 的原生 onFileDrop 而不是 DOM 的 drop 事件：
 * 浏览器出于安全不会把本地文件的绝对路径给网页，而我们要的正是路径。
 * mygo 在 Go 侧拿到真实路径后转发过来，这一点 DOM 事件做不到。
 */
export default function DropZone({
  onPath,
  children,
  style,
  activeStyle,
  hint = '拖入文件夹',
}: {
  onPath: (path: string) => void
  children: React.ReactNode
  style?: React.CSSProperties
  activeStyle?: React.CSSProperties
  hint?: string
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [over, setOver] = useState(false)
  // onPath 每次渲染都是新函数，用 ref 避免反复订阅
  const cb = useRef(onPath)
  cb.current = onPath

  useEffect(() => {
    const unsub = onFileDrop(({ paths, x, y }) => {
      const el = ref.current
      if (!el) return
      // mygo 会给出落点坐标；只有落在本区域内才处理，
      // 否则窗口里每个 DropZone 都会响应同一次拖放。
      const hit = document.elementFromPoint(x, y)
      if (!hit || !el.contains(hit)) return
      const first = paths?.[0]
      if (first) cb.current(first)
      setOver(false)
    })
    return unsub
  }, [])

  return (
    <div
      ref={ref}
      style={{
        position: 'relative',
        border: `1px dashed ${over ? 'var(--accent-primary)' : 'var(--border-default)'}`,
        borderRadius: 'var(--radius-md)',
        background: over ? 'var(--accent-dim)' : 'transparent',
        transition:
          'border-color var(--dur-fast) var(--ease), background var(--dur-fast) var(--ease)',
        ...style,
        ...(over ? activeStyle : null),
      }}
      onDragEnter={() => setOver(true)}
      onDragLeave={() => setOver(false)}
      onDragOver={(e) => {
        e.preventDefault()
        setOver(true)
      }}
      onDrop={(e) => e.preventDefault()}
    >
      {children}
      {over && (
        <div
          style={{
            position: 'absolute',
            inset: 0,
            display: 'grid',
            placeItems: 'center',
            fontSize: 'var(--text-xs)',
            fontWeight: 600,
            color: 'var(--accent-bright)',
            background: 'color-mix(in srgb, var(--accent-dim) 70%, transparent)',
            borderRadius: 'inherit',
            pointerEvents: 'none',
          }}
        >
          {hint}
        </div>
      )}
    </div>
  )
}
