import { useEffect, useMemo, useState } from 'react'
import { Command } from 'cmdk'
import type { AgentView as Agent, Channel, Room } from '../mygo'

/**
 * QuickSwitch —— ⌘K / Ctrl+K 打开，输入几个字直接跳到某个会话。
 *
 * 侧栏把会话、频道、飞书三段垂直堆起来，条目一多就要滑很久。
 * 折叠只能缓解，真正的解法是「按名字找」而不是「用眼睛扫」。
 * 三类目标放进同一个列表，因为它们对用户是同一件事：我要去哪个对话。
 */

type Target = { kind: 'agent' | 'channel' | 'feishu'; id: string; name: string; hint: string }

export default function QuickSwitch({
  agents,
  channels,
  rooms,
  onSelect,
}: {
  agents: Agent[]
  channels: Channel[]
  rooms: Room[]
  onSelect: (kind: 'agent' | 'channel' | 'feishu', id: string) => void
}) {
  const [open, setOpen] = useState(false)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setOpen((v) => !v)
      } else if (e.key === 'Escape') {
        setOpen(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const targets = useMemo<Target[]>(() => {
    const out: Target[] = []
    for (const a of agents) {
      out.push({
        kind: 'agent',
        id: a.id,
        name: a.name,
        hint: a.status === 'running' ? '运行中' : a.status === 'blocked' ? '等你处理' : a.role || '会话',
      })
    }
    for (const c of channels) {
      out.push({ kind: 'channel', id: c.id, name: c.name, hint: `频道 · ${(c.members ?? []).length} 成员` })
    }
    for (const r of rooms) {
      out.push({ kind: 'feishu', id: r.id, name: r.name || '飞书会话', hint: `飞书 · ${r.agentName || '未绑定'}` })
    }
    return out
  }, [agents, channels, rooms])

  if (!open) return null

  const go = (t: Target) => {
    onSelect(t.kind, t.id)
    setOpen(false)
  }

  return (
    <div className="c-quick" role="dialog" aria-label="快速跳转" onClick={() => setOpen(false)}>
      <div className="c-quick__panel" onClick={(e) => e.stopPropagation()}>
        <Command label="快速跳转" loop>
          <Command.Input autoFocus placeholder="跳到会话、频道或飞书群…" className="c-quick__input" />
          <Command.List className="c-quick__list">
            <Command.Empty className="c-quick__empty">没有匹配的目标</Command.Empty>
            {targets.map((t) => (
              <Command.Item
                key={`${t.kind}:${t.id}`}
                value={`${t.name} ${t.hint}`}
                onSelect={() => go(t)}
                className="c-quick__item"
              >
                <span className={`c-quick__kind c-quick__kind--${t.kind}`} aria-hidden>
                  {t.kind === 'agent' ? '◧' : t.kind === 'channel' ? '#' : '✉'}
                </span>
                <span className="c-quick__name">{t.name}</span>
                <span className="c-quick__hint">{t.hint}</span>
              </Command.Item>
            ))}
          </Command.List>
        </Command>
        <div className="c-quick__foot">↑↓ 选择 · Enter 打开 · Esc 关闭</div>
      </div>
    </div>
  )
}