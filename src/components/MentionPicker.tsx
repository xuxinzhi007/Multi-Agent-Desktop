import { Command } from 'cmdk'

export type MentionTarget = {
  id: string
  name: string
  color: string
  role?: string
}

/**
 * MentionPicker —— @ 提及选择器，单聊与群聊共用。
 *
 * 之前两处各写一套（AgentView / ChannelView），状态和过滤逻辑重复，
 * 且对 @A@B 这类输入行为不同。cmdk 提供键盘导航、无匹配态、无障碍。
 */
export default function MentionPicker({
  open,
  query,
  agents,
  multi,
  selected,
  onPick,
}: {
  open: boolean
  /** 当前 @ 后已输入的字符，用于过滤 */
  query: string
  agents: MentionTarget[]
  /** 是否多选（群聊），单选则选中后关闭 */
  multi?: boolean
  /** 已选中的成员名（多选时显示勾） */
  selected?: string[]
  onPick: (name: string) => void
}) {
  if (!open) return null

  const filtered = query
    ? agents.filter(
        (a) =>
          a.name.toLowerCase().includes(query.toLowerCase()) ||
          a.id.toLowerCase().includes(query.toLowerCase()),
      )
    : agents

  return (
    <div
      className="fade-up"
      style={{
        position: 'absolute',
        bottom: '100%',
        left: 13,
        right: 13,
        marginBottom: 4,
        background: 'var(--bg-elevated)',
        border: '1px solid var(--border-default)',
        borderRadius: 'var(--radius-md)',
        boxShadow: 'var(--shadow-lg)',
        maxHeight: 200,
        overflowY: 'auto',
        zIndex: 50,
      }}
    >
      <Command shouldFilter={false} loop>
        {filtered.length === 0 ? (
          <div
            style={{
              padding: '10px 12px',
              fontSize: 'var(--text-xs)',
              color: 'var(--text-disabled)',
            }}
          >
            没有匹配的成员
          </div>
        ) : (
          <Command.List>
            {filtered.map((a) => {
              const isSelected = selected?.includes(a.name)
              return (
                <Command.Item
                  key={a.id}
                  value={a.name}
                  onSelect={() => onPick(a.name)}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    padding: '8px 12px',
                    fontSize: 'var(--text-sm)',
                    color: 'var(--text-primary)',
                    cursor: 'pointer',
                    borderBottom: '1px solid var(--border-subtle)',
                    outline: 'none',
                  }}
                  onMouseEnter={(e) =>
                    (e.currentTarget.style.background = 'var(--bg-surface-hover)')
                  }
                  onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                >
                  <span
                    style={{
                      width: 16,
                      height: 16,
                      borderRadius: 'var(--radius-sm)',
                      background: a.color,
                      display: 'grid',
                      placeItems: 'center',
                      fontSize: 9,
                      fontWeight: 700,
                      color: 'var(--bg-base)',
                    }}
                  >
                    {a.name.slice(0, 1).toUpperCase()}
                  </span>
                  <span>{a.name}</span>
                  {a.role && (
                    <span
                      style={{
                        marginLeft: 'auto',
                        fontSize: 'var(--text-xs)',
                        color: 'var(--text-disabled)',
                      }}
                    >
                      {a.role.slice(0, 20)}
                    </span>
                  )}
                  {multi && isSelected && (
                    <span style={{ marginLeft: 'auto', color: 'var(--accent-bright)' }}>✓</span>
                  )}
                </Command.Item>
              )
            })}
          </Command.List>
        )}
      </Command>
    </div>
  )
}
