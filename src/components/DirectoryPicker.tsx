import { API } from '../mygo'
import DropZone from './DropZone'

/**
 * DirectoryPicker —— 目录选择，全项目唯一实现。
 *
 * 之前 AgentView 和 CreateAgentModal 各自写了一遍「DropZone + 浏览按钮 +
 * 路径展示」，样式和行为已经开始分叉。抽出来统一：
 *   - 拖入文件夹（走 mygo 原生 onFileDrop，DOM 事件拿不到绝对路径）
 *   - 点「浏览」调系统对话框
 *   - 路径只展示不可编辑（已提供两种选择方式，再让人手敲绝对路径是多余的）
 *   - 有值时给一个清除按钮
 */
export default function DirectoryPicker({
  value,
  onChange,
  placeholder = '把文件夹拖到这里',
  compact = false,
}: {
  value: string
  onChange: (dir: string) => void
  placeholder?: string
  /** 紧凑模式：单行布局，用于塞进表单里 */
  compact?: boolean
}) {
  const browse = async (e: React.MouseEvent) => {
    e.stopPropagation()
    const picked = await API.pickDirectory()
    if (picked) onChange(picked)
  }

  if (compact) {
    return (
      <DropZone onPath={onChange} style={{ padding: '7px 9px' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <span
            aria-hidden
            className="mono"
            style={{ fontSize: 'var(--text-xs)', color: 'var(--text-disabled)', flexShrink: 0 }}
          >
            📁
          </span>
          <span
            className="mono"
            style={{
              flex: 1,
              minWidth: 0,
              fontSize: 'var(--text-xs)',
              color: value ? 'var(--text-secondary)' : 'var(--text-disabled)',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
              textAlign: 'left',
            }}
            title={value || undefined}
          >
            {value || placeholder}
          </span>
          {value && (
            <button
              type="button"
              className="c-icon-btn"
              style={{ width: 18, height: 18, fontSize: 'var(--text-xs)', flexShrink: 0 }}
              title="清除"
              onClick={(e) => {
                e.stopPropagation()
                onChange('')
              }}
            >
              ×
            </button>
          )}
          <button type="button" className="c-hint-chip" onClick={browse}>
            浏览…
          </button>
        </div>
      </DropZone>
    )
  }

  return (
    <DropZone
      onPath={onChange}
      style={{ padding: '14px 12px', textAlign: 'center' }}
      hint="松开即设为工作目录"
    >
      <div
        style={{
          fontSize: 18,
          marginBottom: 6,
          opacity: value ? 0.5 : 1,
        }}
        aria-hidden
      >
        📁
      </div>
      <div
        className="mono"
        style={{
          fontSize: 'var(--text-xs)',
          color: value ? 'var(--text-secondary)' : 'var(--text-disabled)',
          wordBreak: 'break-all',
          lineHeight: 1.6,
          marginBottom: 8,
        }}
      >
        {value || placeholder}
      </div>
      <div style={{ display: 'flex', gap: 6, justifyContent: 'center' }}>
        <button type="button" className="c-btn c-btn--secondary c-btn--sm" onClick={browse}>
          浏览…
        </button>
        {value && (
          <button
            type="button"
            className="c-btn c-btn--ghost c-btn--sm"
            onClick={(e) => {
              e.stopPropagation()
              onChange('')
            }}
          >
            清除
          </button>
        )}
      </div>
    </DropZone>
  )
}
