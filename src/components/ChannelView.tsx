import { Fragment, useEffect, useRef, useState } from 'react'
import MentionInput, { type MentionInputHandle } from './MentionInput'
import type { MentionTarget } from './MentionText'
import Modal from './Modal'
import PermissionPicker from './PermissionPicker'
import ProfileCard from './ProfileCard'
import type { AgentView as Agent, Channel, ChannelMessage } from '../mygo'
import { API } from '../mygo'
import { bodyOf } from '../lib/message'
import { previewMode } from '../lib/preview-runtime'
import { ImageStrip, readImageFiles, type DraftImage } from './ImageDraft'
import { MessageRow } from './MessageRow'
import { Thinking, type Pace, type ThinkStep } from './ThinkRail'

/** 频道里每个成员最近的工具步骤：与单聊共用同一套思考链渲染。 */
type TrailStep = ThinkStep & { id: string }

type Props = {
  channel: Channel | null
  agents: Agent[]
  /** 各 agent 的流式增量，key 是 agent ID */
  streaming?: Record<string, string>
  /** 各 agent 最近的工具步骤（含 agentId），用来显示和单聊一致的思考链 */
  trail?: TrailStep[]
  /** 各 agent 这一轮请求的等待计时 */
  pace?: Record<string, Pace>
  /** 各 agent 尚未落成消息的思考文本 */
  reasoning?: Record<string, string>
  /** text 已含 @名字；mentions 是结构化的被点名成员名列表 */
  onSend: (text: string, mentions: string[], images?: { mime: string; data: string }[]) => Promise<void> | void
  onSetSpeaker?: (agentId: string) => void
  onDelete?: () => void
  /** 全部频道：个人主页用来算「他在哪些频道 / 最近说过什么」 */
  allChannels?: Channel[]
  onOpenAgent?: (id: string) => void
  onOpenChannel?: (id: string) => void
  /** 后端连接状态：断线时横幅提示并禁用发送 */
  connected?: boolean
  onStopAgent?: (id: string) => void
}

/**
 * ChannelView 是「群聊」视图：一个频道里的消息对所有成员广播。
 * 与 AgentView 的区别是消息带发言人身份，且成员都是 agent。
 */
export default function ChannelView({ channel, agents, streaming, trail, pace, reasoning, onSend, onSetSpeaker, onDelete, allChannels, onOpenAgent, onOpenChannel, connected = true, onStopAgent }: Props) {
  const [input, setInput] = useState('')
  const [images, setImages] = useState<DraftImage[]>([])
  const [imageNote, setImageNote] = useState('')
  // 正文里的胶囊 @：由 MentionInput 序列化时一并给出
  const [mentioned, setMentioned] = useState<string[]>([])
  const [profileId, setProfileId] = useState<string | null>(null)
  const [membersOpen, setMembersOpen] = useState(false)
  const scrollRef = useRef<HTMLDivElement>(null)
  const taRef = useRef<MentionInputHandle>(null)
  // 阅读权优先：用户上翻即解锁自动滚底，只有回底胶囊/贴底时才跟随
  const stickRef = useRef(true)
  const jumpingRef = useRef(false)
  const [showJump, setShowJump] = useState(false)
  const stopAllRef = useRef<() => void>(() => {})

  const handleScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    if (jumpingRef.current) {
      if (nearBottom) {
        jumpingRef.current = false
        stickRef.current = true
        setShowJump(false)
      }
      return
    }
    stickRef.current = nearBottom
    setShowJump(!nearBottom)
  }

  const jumpToBottom = () => {
    const el = scrollRef.current
    if (!el) return
    jumpingRef.current = true
    el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') stopAllRef.current()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  const streamLen = Object.values(streaming ?? {}).reduce((n, t) => n + (t?.length ?? 0), 0)

  useEffect(() => {
    const el = scrollRef.current
    if (el && stickRef.current) el.scrollTop = el.scrollHeight
  }, [channel?.messages.length, streamLen])

  useEffect(() => {
    stickRef.current = true
    setShowJump(false)
    taRef.current?.focus()
  }, [channel?.id])

  if (!channel) {
    return (
      <div className="c-col" style={{ flex: 1 }}>
        <div className="c-empty" style={{ flex: 1 }}>
          <div style={{ fontSize: 28, marginBottom: 'var(--space-3)' }}>#</div>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 8 }}>
            还没有频道
          </div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            在左侧点「+ 新建频道」，把多个 Agent 拉进同一个群聊
          </div>
        </div>
      </div>
    )
  }

  const members = agents.filter((a) => (channel.members ?? []).includes(a.id))
  // 用全部 agent 而非仅成员：历史消息里被 @ 的人可能已退出频道
  const mentionTargets: MentionTarget[] = [
    { id: 'all', name: '所有人', color: 'var(--accent-bright)' },
    ...agents.map((a) => ({
      id: a.id,
      name: a.name,
      color: a.color,
    })),
  ]
  const memberById = (id: string) => agents.find((a) => a.id === id)

  const msgs = channel.messages ?? []
  let lastUserAt = -1
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].fromId === 'user') {
      lastUserAt = i
      break
    }
  }
  // 防「已读不回」：最后一条用户消息里 delivered 的成员，还没回复、没在流式，
  // 且回合确实还活着（running/queued/blocked）才显示状态行。
  // 回合以停止/出错/空回复结束时 agent 回 idle，回复永远不会来——
  // 不过滤就会永远挂在「正在接入」。事件顺序保证：postChannel 先 Prompt
  // （发 agent.status）后回填 deliveries，收到回执时状态已就位。
  const replied = new Set(msgs.slice(lastUserAt + 1).map((m) => m.fromId))
  const pending =
    lastUserAt >= 0
      ? (msgs[lastUserAt].deliveries ?? [])
          .filter((d) => d.status === 'delivered' && !replied.has(d.agentId) && !streaming?.[d.agentId])
          .map((d) => memberById(d.agentId))
          .filter((a): a is Agent => !!a)
          .filter((a) => a.status === 'running' || a.status === 'queued' || a.status === 'blocked')
      : []
  const errored = members.filter((a) => a.status === 'error')
  // 正在回复的：还没开口的 + 正在流式输出的，去重后给「停止全部」
  const activeIds = [
    ...new Set([
      ...pending.map((a) => a.id),
      ...Object.entries(streaming ?? {})
        .filter(([id, t]) => t && (channel.members ?? []).includes(id))
        .map(([id]) => id),
    ]),
  ]
  const stopAll = () => activeIds.forEach((id) => onStopAgent?.(id))
  // Esc 全局急停；弹层开着时让弹层自己处理 Esc
  stopAllRef.current = () => {
    if (!membersOpen && !profileId && activeIds.length > 0) stopAll()
  }
  // ≥3 人并发：本轮长回复限高折叠，防瀑布流淹没主时间轴
  const burst = activeIds.length >= 3

  const addImages = async (files: File[]) => {
    const got = await readImageFiles(files)
    if (got.error) setImageNote(got.error)
    if (got.images.length === 0) return
    setImages((prev) => [...prev, ...got.images].slice(0, 8))
  }

  const submit = () => {
    if (!connected) return
    const text = input.trim()
    if (!text && images.length === 0) return
    // @名字 已在正文里（胶囊序列化），后端 ParseMentions 直接读文本
    const shots = images.map((img) => ({ mime: img.mime, data: img.data }))
    Promise.resolve(onSend(text, mentioned, shots.length ? shots : undefined))
      .then(() => {
        setInput('')
        setMentioned([])
        setImages([])
        setImageNote('')
      })
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : String(err)
        setImageNote(msg || '发送失败')
      })
  }

  return (
    <div className="c-col" style={{ flex: 1 }}>
      <header
        style={{
          height: 'var(--header-h)',
          display: 'flex',
          alignItems: 'center',
          gap: 'var(--space-2)',
          padding: '0 var(--space-4)',
          borderBottom: '1px solid var(--border-subtle)',
          flexShrink: 0,
        }}
      >
        <span
          style={{
            width: 24,
            height: 24,
            borderRadius: 'var(--radius-sm)',
            background: 'var(--accent-dim)',
            color: 'var(--accent-bright)',
            display: 'grid',
            placeItems: 'center',
            fontSize: 'var(--text-base)',
            fontWeight: 700,
            flexShrink: 0,
          }}
        >
          #
        </span>
        <div style={{ minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-md)', fontWeight: 600, lineHeight: 1.3 }}>
            {channel.name}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {members.length} 个成员
          </div>
        </div>

        <label
          style={{
            marginLeft: 12,
            display: 'flex',
            alignItems: 'center',
            gap: 6,
            fontSize: 'var(--text-xs)',
            color: 'var(--text-tertiary)',
          }}
        >
          默认接话
          <select
            aria-label="默认接话人"
            value={channel.speaker ?? ''}
            onChange={(e) => onSetSpeaker?.(e.target.value)}
            style={{
              fontSize: 'var(--text-xs)',
              color: 'var(--text-primary)',
              background: 'var(--bg-inset)',
              border: '1px solid var(--border-subtle)',
              borderRadius: 'var(--radius-sm)',
              padding: '2px 6px',
            }}
          >
            <option value="">未指定</option>
            {members.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        </label>

        {/* 成员头像：点开成员名单 */}
        <button
          onClick={() => setMembersOpen(true)}
          title="查看成员名单"
          style={{ display: 'flex', alignItems: 'center', marginLeft: 12, background: 'none', border: 'none', cursor: 'pointer', padding: 0 }}
        >
          {members.slice(0, 6).map((m) => (
            <span
              key={m.id}
              title={m.name}
              style={{
                width: 18,
                height: 18,
                borderRadius: 'var(--radius-full)',
                background: m.color,
                color: 'var(--bg-base)',
                display: 'grid',
                placeItems: 'center',
                fontSize: 'var(--text-xs)',
                fontWeight: 700,
                marginLeft: -4,
                border: '1.5px solid var(--bg-chrome)',
              }}
            >
              {m.name.slice(0, 1).toUpperCase()}
            </span>
          ))}
          {members.length > 6 && (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginLeft: 4 }}>
              +{members.length - 6}
            </span>
          )}
        </button>

        {onDelete && (
          <button
            className="c-icon-btn c-icon-btn--danger"
            style={{ marginLeft: 'auto' }}
            onClick={() => {
              if (confirm(`删除频道「${channel.name}」？消息将一并清除。`)) onDelete()
            }}
            title="删除频道"
          >
            删除
          </button>
        )}
      </header>

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        style={{ flex: 1, overflowY: 'auto', padding: 'var(--space-4) var(--space-6) var(--space-6)' }}
      >
        {/* 和单聊、飞书会话共用同一个阅读宽度 */}
        <div className="c-transcript">
        {(channel.messages ?? []).length === 0 && (
          <div className="c-empty" style={{ paddingTop: 'var(--space-6)' }}>
            {previewMode() ? '这是预览' : '频道已创建，发第一条消息吧'}
            <div style={{ fontSize: 'var(--text-xs)', marginTop: 8, color: 'var(--text-disabled)' }}>
              没 @ 人时，由默认接话人回答。输入 @ 选人变胶囊，谁被 @ 谁回，@所有人每人一次；Backspace 整块删除。
            </div>
          </div>
        )}
        {(channel.messages ?? []).map((m, i) => {
          const agent = memberById(m.fromId)
          const isUser = m.fromId === 'user'
          const name = m.fromName || agent?.name || m.fromId || '?'
          const color = agent?.color ?? 'var(--text-tertiary)'
          const forwardTargets = members.filter((x) => x.id !== m.fromId).map((x) => ({ id: x.name, label: `@${x.name}` }))
          return (
            <MessageRow
              key={m.id}
              side={isUser ? 'user' : 'agent'}
              name={name}
              color={color}
              blocks={m.blocks}
              text={bodyOf(m.text)}
              mentionTargets={mentionTargets}
              timestamp={m.at}
              clamp={burst && i > lastUserAt && !isUser && bodyOf(m.text).length > 200}
              copyable={!isUser}
              forwardItems={forwardTargets}
              onForward={(target, text) => onSend(`@${target} 请跟进这条：\n${text}`, [target])}
              onProfile={!isUser ? () => setProfileId(m.fromId) : undefined}
              onRefill={
                isUser
                  ? (t) => {
                      setInput(t)
                      taRef.current?.focus()
                    }
                  : undefined
              }
              tableScroll
              footer={<DeliveryRow msg={m} agents={agents} total={members.length} />}
            />
          )
        })}

        {/* 流式输出：与落定消息同一套 MessageRow（live 走节流 Markdown），≥3 并发时限高折叠 */}
        {Object.entries(streaming ?? {})
          .filter(([id, text]) => text && (channel.members ?? []).includes(id))
          .map(([id, text]) => {
            const a = memberById(id)
            const body = bodyOf(text)
            const long = body.length > 180 || body.split('\n').length > 6
            const steps = (trail ?? []).filter((t) => t.id === id).slice(-8)
            const p = pace?.[id]
            const r = reasoning?.[id]
            // 思考链和单聊共用 ThinkRail：同一个 agent 在两个视图里说同一句话，
            // 不该一个显示工具步骤、一个只显示「正在思考中」。
            const showRail = steps.length > 0 || !!p || !!r
            return (
              <Fragment key={`streaming-${id}`}>
                {showRail && <Thinking steps={steps} pace={p} reasoning={r} />}
                <MessageRow
                  side="agent"
                  name={a?.name ?? id}
                  color={a?.color ?? 'var(--text-tertiary)'}
                  text={body}
                  live
                  clamp={burst && long}
                  onStop={onStopAgent ? () => onStopAgent(id) : undefined}
                  tableScroll
                />
              </Fragment>
            )
          })}

        {/* 接入了但还没开口：送达 ≠ 已回复，把状态摆出来，别像已读不回 */}
        {pending.map((a) => (
          <div
            key={`pending-${a.id}`}
            style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 'var(--space-3)', fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}
          >
            <span
              style={{ width: 22, height: 22, borderRadius: 'var(--radius-sm)', background: a.color, color: 'var(--bg-base)', display: 'grid', placeItems: 'center', fontSize: 'var(--text-xs)', fontWeight: 700, flexShrink: 0, opacity: 0.7 }}
            >
              {a.name.slice(0, 1).toUpperCase()}
            </span>
            <span style={{ fontWeight: 600, color: a.color }}>{a.name}</span>
            <span className="blink">●</span>
            <span>
              {a.status === 'running' && a.busy === channel.name
                ? '正在思考中…'
                : a.status === 'blocked'
                  ? '等你审批后继续…'
                  : a.status === 'running'
                    ? `在 #${a.busy || '别处'} 回答，本条排队中…`
                    : '排队中，前面的回合结束后接入…'}
            </span>
            {onStopAgent && (
              <button className="c-btn c-btn--sm" onClick={() => onStopAgent(a.id)}>
                停止
              </button>
            )}
          </div>
        ))}
        </div>
      </div>

      {/* 回底胶囊：用户上翻时浮在输入框正上方，不拽视口 */}
      {showJump && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '0 var(--space-6) 6px', flexShrink: 0 }}>
          <button className="c-btn c-btn--sm" onClick={jumpToBottom} style={{ boxShadow: 'var(--shadow-lg)' }}>
            ↓ {activeIds.length > 0 ? '有新回复生成中' : '回到底部'}
          </button>
        </div>
      )}

      <div style={{ padding: 'var(--space-3) var(--space-6) var(--space-5)', flexShrink: 0 }}>
        {!connected && (
          <div
            style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8, padding: '8px 12px', background: 'var(--danger-dim)', border: '1px solid var(--danger-border)', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--danger)' }}
          >
            ⚠ 与后端的连接已断开，正在重连…消息暂不可发送，已显示的内容不会丢失
          </div>
        )}
        {errored.length > 0 && (
          <div style={{ marginBottom: 8, fontSize: 'var(--text-xs)', color: 'var(--danger)' }}>
            {errored.map((a) => (
              <span key={a.id} style={{ marginRight: 12 }}>
                ⚠ {a.name} 出错，这一轮没有完成
                {lastUserAt >= 0 && (
                  <button
                    className="c-btn c-btn--sm"
                    style={{ marginLeft: 4 }}
                    title="只重发给这个人：以 @他 + 原消息重新投递，不影响其他人"
                    onClick={() => onSend(`@${a.name} ${bodyOf(msgs[lastUserAt].text)}`, [a.name])}
                  >
                    重试
                  </button>
                )}
                {onOpenAgent && (
                  <button className="c-btn c-btn--sm" style={{ marginLeft: 4 }} onClick={() => onOpenAgent(a.id)}>
                    去单聊看
                  </button>
                )}
              </span>
            ))}
          </div>
        )}
        <div
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            void addImages(Array.from(e.dataTransfer.files))
          }}
          style={{
            position: 'relative',
            background: 'var(--bg-surface)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius-lg)',
          }}
        >
          <ImageStrip images={images} onRemove={(i) => setImages((prev) => prev.filter((_, at) => at !== i))} />
          <MentionInput
            ref={taRef}
            text={input}
            onChange={(t, ms) => {
              setInput(t)
              setMentioned(ms)
            }}
            targets={[{ id: 'all', name: '所有人', color: 'var(--accent-bright)' }, ...members.map((m) => ({ id: m.id, name: m.name, color: m.color }))]}
            placeholder={`发消息到 #${channel.name}…  输入 @ 提及成员`}
            onSubmit={submit}
            onPasteFiles={(files) => void addImages(files)}
          />

          <div style={{ display: 'flex', alignItems: 'center', padding: '5px 8px 7px' }}>
            <label className="c-icon-btn" title="选择图片" style={{ cursor: 'pointer', marginRight: 8 }}>
              图
              <input
                type="file"
                accept="image/png,image/jpeg,image/gif,image/webp"
                hidden
                onChange={(e) => {
                  void addImages(Array.from(e.target.files ?? []))
                  e.target.value = ''
                }}
              />
            </label>
            <span
              style={{
                fontSize: 'var(--text-xs)',
                color: 'var(--text-disabled)',
                display: 'flex',
                gap: 6,
                alignItems: 'center',
                flexWrap: 'wrap',
              }}
            >
              {mentioned.length === 0 ? (
                channel.speaker ? (
                  <>没 @ 人时，由默认接话人回答</>
                ) : (
                  <span style={{ color: 'var(--warning)' }}>
                    未指定默认接话人：这条消息将无人接话。先 @ 成员，或在上方选「默认接话」
                  </span>
                )
              ) : (
                <>
                  将点名：{mentioned.map((n) => `@${n}`).join(' ')}，被 @ 的都会回
                  {mentioned.includes('所有人') && (
                    <span style={{ color: 'var(--warning)' }}>
                      ；每人各回一条，简单确认建议只 @ 默认接话人
                    </span>
                  )}
                </>
              )}
            </span>
            {/* 控制权收口输入区：空输入时发送键直接变停止全部；有内容时发送+紧凑停止 */}
            {activeIds.length > 0 && !input.trim() && images.length === 0 ? (
              <button
                className="c-btn c-btn--danger c-btn--sm"
                style={{ marginLeft: 'auto' }}
                onClick={stopAll}
                title="停止本频道所有正在回复的成员（Esc）"
              >
                ⏹ 停止全部 ({activeIds.length})
              </button>
            ) : (
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
                {activeIds.length > 0 && (
                  <button
                    className="c-btn c-btn--danger c-btn--sm"
                    onClick={stopAll}
                    title="停止本频道所有正在回复的成员（Esc）"
                  >
                    ⏹ 停止全部
                  </button>
                )}
                <button
                  className="c-btn c-btn--primary c-btn--sm"
                  onClick={submit}
                  disabled={!connected || (!input.trim() && images.length === 0)}
                >
                  发送
                </button>
              </span>
            )}
          </div>
        </div>
        <div
          style={{
            marginTop: 6,
            fontSize: 'var(--text-xs)',
            color: 'var(--text-disabled)',
          }}
        >
          {imageNote || 'Enter 发送 · Shift+Enter 换行'}
        </div>
      </div>

      {/* 成员名单：本次只做查看 + 主页入口；增删成员需要后端新接口，不重启落不了 */}
      <Modal open={membersOpen} onClose={() => setMembersOpen(false)} title={`成员 · ${channel.name}`} width={420}>
        <div style={{ padding: '12px 20px 18px' }}>
          {members.map((m) => (
            <div
              key={m.id}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                width: '100%',
                padding: '8px 6px',
                borderBottom: '1px solid var(--border-subtle)',
              }}
            >
              <button
                onClick={() => {
                  setMembersOpen(false)
                  setProfileId(m.id)
                }}
                title="看他的个人主页"
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: 10,
                  flex: 1,
                  minWidth: 0,
                  background: 'none',
                  border: 'none',
                  padding: 0,
                  cursor: 'pointer',
                  textAlign: 'left',
                }}
              >
              <span
                style={{
                  width: 26,
                  height: 26,
                  borderRadius: 'var(--radius-sm)',
                  background: m.color,
                  color: 'var(--bg-base)',
                  display: 'grid',
                  placeItems: 'center',
                  fontSize: 'var(--text-xs)',
                  fontWeight: 700,
                  flexShrink: 0,
                }}
              >
                {m.name.slice(0, 1).toUpperCase()}
              </span>
              <span style={{ minWidth: 0, flex: 1 }}>
                <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600, color: 'var(--text-primary)' }}>
                  {m.name}
                  {channel.speaker === m.id && (
                    <span style={{ marginLeft: 6, fontSize: 'var(--text-xs)', fontWeight: 400, color: 'var(--accent-bright)' }}>
                      默认接话
                    </span>
                  )}
                </span>
                <span style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {m.role || '没写职责'}
                </span>
              </span>
              </button>
              <span style={{ fontSize: 'var(--text-xs)', color: m.status === 'running' ? 'var(--success)' : 'var(--text-disabled)' }}>
                {m.status === 'running' ? '干活中' : m.status === 'blocked' ? '等你处理' : '空闲'}
              </span>
              <PermissionPicker agentId={m.id} placement="down" />
            </div>
          ))}

          <div style={{ marginTop: 12, display: 'flex', justifyContent: 'flex-end' }}>
            <button
              className="c-btn c-btn--sm"
              title="把所有成员的干活权限切为只读，写操作直接拒绝"
              onClick={() => {
                if (confirm(`把 ${members.length} 位成员全部切为只读？写操作将被直接拒绝。`)) {
                  members.forEach((m) => API.setAgentPermission(m.id, 'readonly').catch(() => {}))
                }
              }}
            >
              全员只读安全模式
            </button>
          </div>

          {agents.filter((a) => !(channel.members ?? []).includes(a.id)).length > 0 && (
            <div style={{ marginTop: 14 }}>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>
                其他同事
              </div>
              {agents
                .filter((a) => !(channel.members ?? []).includes(a.id))
                .map((a) => (
                  <div key={a.id} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px' }}>
                    <span
                      style={{
                        width: 22,
                        height: 22,
                        borderRadius: 'var(--radius-sm)',
                        background: a.color,
                        color: 'var(--bg-base)',
                        display: 'grid',
                        placeItems: 'center',
                        fontSize: 'var(--text-xs)',
                        fontWeight: 700,
                        flexShrink: 0,
                        opacity: 0.6,
                      }}
                    >
                      {a.name.slice(0, 1).toUpperCase()}
                    </span>
                    <span style={{ flex: 1, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{a.name}</span>
                    <button
                      className="c-btn c-btn--sm"
                      disabled
                      title="邀请入频道需要后端新接口，本次不重启后端，暂不可用"
                    >
                      邀请
                    </button>
                  </div>
                ))}
            </div>
          )}
        </div>
      </Modal>

      <ProfileCard
        agent={profileId ? agents.find((a) => a.id === profileId) ?? null : null}
        channels={allChannels ?? [channel]}
        onClose={() => setProfileId(null)}
        onOpenChat={onOpenAgent}
        onOpenChannel={(id) => {
          setProfileId(null)
          onOpenChannel?.(id)
        }}
      />
    </div>
  )
}

/** 渲染投递结果：送达只留汇总，失败突出，跳过折叠；点开看名单。 */
function DeliveryRow({ msg, agents, total }: { msg: ChannelMessage; agents: Agent[]; total?: number }) {
  const [expandSkipped, setExpandSkipped] = useState(false)
  const [expandDelivered, setExpandDelivered] = useState(false)
  const deliveries = msg.deliveries ?? []
  if (deliveries.length === 0) return null

  // ID 是 agent-<unix秒>，前 8 位永远是 agent-17，必须解析成名字
  const nameOf = (id: string) => agents.find((a) => a.id === id)?.name ?? id
  const delivered = deliveries.filter((d) => d.status === 'delivered')
  const failed = deliveries.filter((d) => d.status !== 'delivered' && d.status !== 'skipped')
  const skipped = deliveries.filter((d) => d.status === 'skipped')

  return (
    <div style={{ marginTop: 5, fontSize: 'var(--text-xs)' }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
        {/* 送达只留一条汇总，逐个名字悬停/点开看；状态行里不再重复「已送达」 */}
        {delivered.length === 1 && (
          <span style={{ color: 'var(--success)', display: 'inline-flex', alignItems: 'center', gap: 3 }}>
            <span>✓</span>
            <span style={{ fontWeight: 600 }}>{nameOf(delivered[0].agentId)}</span>
            <span style={{ color: 'var(--text-disabled)' }}>已送达</span>
          </span>
        )}
        {delivered.length > 1 && (
          <span
            onClick={() => setExpandDelivered((v) => !v)}
            title={delivered.map((d) => nameOf(d.agentId)).join('、')}
            style={{ color: 'var(--success)', display: 'inline-flex', alignItems: 'center', gap: 3, cursor: 'pointer' }}
          >
            <span>✓</span>
            <span>
              {total && delivered.length === total
                ? `全员已送达 (${delivered.length}/${total})`
                : `${delivered.length} 人已送达`}{' '}
              {expandDelivered ? '▾' : '▸'}
            </span>
          </span>
        )}
        {failed.map((d) => (
          <span
            key={d.agentId}
            title={d.reason || '失败'}
            style={{ color: 'var(--danger)', display: 'inline-flex', alignItems: 'center', gap: 3 }}
          >
            <span>✕</span>
            <span style={{ fontWeight: 600 }}>{nameOf(d.agentId)}</span>
            <span style={{ color: 'var(--text-disabled)' }}>失败：{d.reason ?? '未知原因'}</span>
          </span>
        ))}
        {skipped.length > 0 && (
          <span
            onClick={() => setExpandSkipped((v) => !v)}
            title={expandSkipped ? '收起' : '展开看是谁、为什么'}
            style={{ color: 'var(--text-disabled)', display: 'inline-flex', alignItems: 'center', gap: 3, cursor: 'pointer' }}
          >
            <span>○</span>
            <span>
              {skipped.length} 人未接话 {expandSkipped ? '▾' : '▸'}
            </span>
          </span>
        )}
      </div>
      {expandDelivered && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 4 }}>
          {delivered.map((d) => (
            <span key={d.agentId} style={{ color: 'var(--text-tertiary)' }}>
              {nameOf(d.agentId)}
            </span>
          ))}
        </div>
      )}
      {expandSkipped && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginTop: 4 }}>
          {skipped.map((d) => (
            <span key={d.agentId} style={{ color: 'var(--text-disabled)' }}>
              {nameOf(d.agentId)}（{d.reason || '已跳过'}）
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
