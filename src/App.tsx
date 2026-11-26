import { useCallback, useEffect, useMemo, useState } from 'react'
import Sidebar from './components/Sidebar'
import AgentView from './components/AgentView'
import ChannelView from './components/ChannelView'
import FeishuRoomView from './components/FeishuRoomView'
import FleetView from './components/FleetView'
import AmbientBackground from './components/AmbientBackground'
import Inspector, { type InspectorTab } from './components/Inspector'
import ApprovalModal from './components/ApprovalModal'
import KnowledgePanel from './components/KnowledgePanel'
import SettingsModal from './components/SettingsModal'
import ToastContainer from './components/Toast'
import QuickSwitch from './components/QuickSwitch'
import CapabilityPanel from './components/CapabilityPanel'
import StatusBar from './components/StatusBar'
import GatewayPage from './components/pages/GatewayPage'
import MemoryPage from './components/pages/MemoryPage'
import PlanPage from './components/pages/PlanPage'
import { useBackend } from './lib/useBackend'
import { useGatewayStatus } from './lib/gateway'
import type { AgentView as Agent } from './mygo'
import { API } from './mygo'
import { useTheme } from './lib/useTheme'
import type { LLMConfig } from './lib/useBackend'
import type { MainView } from './lib/nav'
import type { AcpForm, RuntimeKind } from './lib/runtime'
import { toastError } from './components/Toast'

type ActiveRef = { kind: 'agent' | 'channel' | 'feishu'; id: string } | null

export default function App() {
  const s = useBackend()
  // 网关状态摘要：只喂给顶部状态条，低速轮询；明细由网关页自己再拉一次。
  const gw = useGatewayStatus(6000)
  useTheme() // 应用主题到 document.documentElement
  const [active, setActive] = useState<ActiveRef>(null)
  const [view, setView] = useState<MainView>('fleet')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  // 审批队列弹窗开关：审批到达时自动弹出（现有行为），顶部待审批数也可手动打开
  const [approvalOpen, setApprovalOpen] = useState(false)
  // 默认视图是舰队，检查器会把它挤窄，所以初始收起。
  // 进对话视图时 onViewChange 会把它展开。
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [tab, setTab] = useState<InspectorTab>('tools')
  const openKnowledge = useCallback(() => {
    setTab('knowledge')
    setInspectorOpen(true)
  }, [])

  // 选中项失效时自动回落到第一个可用项
  useEffect(() => {
    if (active?.kind === 'agent' && !s.agents.some((a) => a.id === active.id)) {
      setActive(s.agents[0] ? { kind: 'agent', id: s.agents[0].id } : null)
      return
    }
    if (active?.kind === 'channel' && !s.channels.some((c) => c.id === active.id)) {
      setActive(s.channels[0] ? { kind: 'channel', id: s.channels[0].id } : null)
      return
    }
    if (!active && s.agents.length > 0) {
      setActive({ kind: 'agent', id: s.agents[0].id })
    }
  }, [s.agents, s.channels, active])

  useEffect(() => {
    if (inspectorOpen && tab === 'knowledge') s.refreshNotes()
  }, [inspectorOpen, tab, s.refreshNotes])

  // 有新审批待决时自动弹出队列；队列清空后弹窗随 items 为空自然收起
  useEffect(() => {
    if (s.approvals.length > 0) setApprovalOpen(true)
  }, [s.approvals.length])

  // 顶部状态条数字：全部由现有 state 实时计算，不接后端推测值
  const runningCount = s.agents.filter((a) => a.status === 'running').length
  const queueTotal = s.agents.reduce((n, a) => n + (a.queueLen ?? 0), 0)

  useEffect(() => {
    if (view === 'chat' && active?.kind === 'agent') s.openAgent(active.id)
    else s.parkAgents()
  }, [view, active?.kind, active?.id, s.openAgent, s.parkAgents])

  const activeAgent = useMemo(
    () => (active?.kind === 'agent' ? (s.agents.find((a) => a.id === active.id) ?? null) : null),
    [s.agents, active],
  )
  const activeChannel = useMemo(
    () => (active?.kind === 'channel' ? (s.channels.find((c) => c.id === active.id) ?? null) : null),
    [s.channels, active],
  )
  const activeFeishu = useMemo(
    () => (active?.kind === 'feishu' ? (s.feishuRooms.find((r) => r.id === active.id) ?? null) : null),
    [s.feishuRooms, active],
  )
  const streaming = activeAgent ? s.streaming[activeAgent.id] ?? '' : ''
  const err = activeAgent ? s.errors[activeAgent.id] : undefined

  const createAgent = async (
    name: string,
    role: string,
    backend: string,
    workDir?: string,
    runtime: RuntimeKind = 'native',
    acp?: AcpForm,
  ) => {
    const created = await s.send<Agent | null>(
      backend === 'mock' ? 'agent.create' : 'agent.createCLI',
      backend === 'mock' ? { name, role } : { name, role, backend },
    )
    // 两阶段：先创建，再写入会话级工作目录（避免改 CreateAgentRequest 形状）
    if (created && workDir) {
      await s.send('agent.workDir', { id: created.id, dir: workDir })
    }
    // 运行时同样两阶段：CreateAgentRequest 不带 runtime，创建后用 setAgentRuntime 落盘 ACP 配置。
    // 失败时会话仍是 native 兜底，错误原文走 toast，不假装已切到 ACP。
    if (created && runtime === 'acp' && acp?.command) {
      API.setAgentRuntime({
        agentId: created.id,
        runtime: 'acp',
        name: acp.name,
        command: acp.command,
        args: acp.args,
        env: acp.env,
      }).catch((e: unknown) => {
        toastError(`保存运行时配置失败：${e instanceof Error ? e.message : String(e)}`)
      })
    }
    // 评审/审查/规划类角色默认只读，降低多 Agent 并发写文件冲突
    if (created && /review|审查|评审|规划|planner/i.test(`${created.name} ${created.role}`)) {
      API.setAgentPermission(created.id, 'readonly').catch(() => {})
    }
    if (created) setActive({ kind: 'agent', id: created.id })
  }

  // 自动拆任务：创建 Supervisor 会话后立刻把目标派进去，剩下交给后端。
  const createSupervisor = async (req: { name: string; role: string; goal: string; workDir: string }) => {
    const created = await s.send<Agent | null>('agent.createSupervisor', req)
    if (created) {
      setActive({ kind: 'agent', id: created.id })
      setView('chat')
    }
  }

  return (
    <>
      <AmbientBackground />
      <div className="c-shell" style={{ flexDirection: 'column' }}>
      <StatusBar
        running={runningCount}
        approvals={s.approvals.length}
        queue={queueTotal}
        gateway={{ enabled: gw.status.gateway, running: gw.status.running, listenError: gw.status.listenError ?? '' }}
        onOpenApprovals={() => setApprovalOpen(true)}
      />
      {(() => {
        // 写意图提示条：别的会话正在改什么，在覆盖发生前就让人看见。
        // 过滤用 60 秒窗口；App 本身有 6 秒一次的网关状态刷新，过期条目会自动消失。
        const now = Date.now() / 1000
        const live = (s.intents ?? []).filter((i) => now - i.at < 60).slice(-3)
        if (live.length === 0) return null
        return (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '4px 14px',
              fontSize: 12,
              color: 'var(--tx-2, #b9bdc9)',
              borderBottom: '1px solid rgba(255,255,255,0.06)',
              background: 'rgba(255,255,255,0.03)',
            }}
          >
            <span style={{ width: 6, height: 6, borderRadius: 999, background: '#e5b454' }} />
            {live.map((i, idx) => (
              <span key={`${i.agentId}-${i.paths[0]}-${idx}`}>
                {idx > 0 ? ' · ' : ''}
                <strong style={{ fontWeight: 600 }}>{i.agentName || i.agentId}</strong> 正在修改{' '}
                {i.paths[0]}
                {i.paths.length > 1 ? ` 等 ${i.paths.length} 个文件` : ''}
              </span>
            ))}
          </div>
        )
      })()}
      <div
        style={{
          display: 'flex',
          flex: 1,
          minWidth: 0,
          minHeight: 0,
        }}
      >
      <Sidebar
        agents={s.agents}
        channels={s.channels}
        feishuRooms={s.feishuRooms}
        activeId={active?.id ?? null}
        activeKind={active?.kind ?? null}
        connected={s.connected}
        version={s.version}
        provider={s.llm.provider}
        onSelect={(kind, id) => {
          setActive({ kind, id })
          setView('chat')
          // 和「对话」tab 一致：进对话就展开检查器，别让用户再点一次
          setInspectorOpen(true)
          // 进飞书会话时静默补一页历史；失败不打扰，「从飞书拉取」按钮可手动重试。
          if (kind === 'feishu') API.syncFeishuChat(id).catch(() => {})
        }}
        view={view}
        onViewChange={(v) => {
          setView(v)
          // 舰队的价值是「一眼看全」；检查器会把监控台挤窄，切过去时自动收起。
          // 回到对话再展开，因为 diff / 工具面板是看对话时才用得上的。
          setInspectorOpen(v === 'chat')
        }}
        onCreate={createAgent}
        onCreateSupervisor={createSupervisor}
        onDelete={(id) => s.send('agent.delete', { id })}
        onCreateChannel={(name, members) => s.send('channel.create', { name, members })}
        onOpenSettings={() => setSettingsOpen(true)}
        onOpenBrowser={() => {
          API.openWorkbench('').catch(() => {})
        }}
        onBindFeishu={(chatId, agentId) => s.send('feishu.bind', { chatId, agentId })}
        createOpen={createOpen}
        onCreateOpenChange={setCreateOpen}
      />

      {view === 'gateway' ? (
        <GatewayPage />
      ) : view === 'memory' ? (
        <MemoryPage agents={s.agents} onOpenKnowledge={openKnowledge} />
      ) : view === 'plan' ? (
        <PlanPage agents={s.agents} />
      ) : view === 'fleet' && !activeChannel && !activeFeishu ? (
        <FleetView
          agents={s.agents}
          streaming={s.streaming}
          onOpen={(id) => {
            setActive({ kind: 'agent', id })
            setView('chat')
          }}
          onStop={(id) => s.send('agent.stop', { id })}
          onQuickCreate={(name, role) => createAgent(name, role, 'loop')}
          onCreateRequest={() => setCreateOpen(true)}
        />
      ) : activeFeishu ? (
        <FeishuRoomView room={activeFeishu} />
      ) : activeChannel ? (
        <ChannelView
          channel={activeChannel}
          agents={s.agents}
          streaming={s.channelStreaming}
          trail={s.tools}
          pace={s.pace}
          reasoning={s.reasoning}
          onSend={async (text, mentions, images) => {
            // 同对象抢占：新消息再 @ 正在本群输出的人 → 先停旧回合再投新指令；
            // 异对象并行：空闲成员不受影响。精确只断群回合需后端，见交接单。
            const targets = mentions.includes('所有人')
              ? s.agents.filter((a) => (activeChannel.members ?? []).includes(a.id))
              : s.agents.filter((a) => mentions.includes(a.name))
            const busyHere = targets.filter((a) => a.status === 'running' && a.busy === activeChannel.name)
            if (busyHere.length > 0) {
              await Promise.all(busyHere.map((a) => s.send('agent.stop', { id: a.id }).catch(() => {})))
            }
            return s.send('channel.send', {
              channelId: activeChannel.id,
              fromId: 'user',
              fromName: '你',
              text,
              images,
            })
          }}
          onSetSpeaker={(agentId) =>
            s.send('channel.speaker', { channelId: activeChannel.id, agentId })
          }
          onDelete={() => s.send('channel.delete', { id: activeChannel.id })}
          allChannels={s.channels}
          onOpenAgent={(id) => {
            setActive({ kind: 'agent', id })
            setView('chat')
          }}
          onOpenChannel={(id) => {
            setActive({ kind: 'channel', id })
            setView('chat')
          }}
          connected={s.connected}
          onStopAgent={(id) => s.send('agent.stop', { id })}
        />
      ) : (
        <AgentView
          agent={activeAgent}
          streaming={streaming}
          error={err}
          workDir={s.workDir}
          tokenUsage={activeAgent ? s.tokenUsage[activeAgent.id] : undefined}
          agents={s.agents}
          disableRoute  // 单聊模式：禁用 @ 路由
          channels={s.channels}
          onOpenChannel={(id) => {
            setActive({ kind: 'channel', id })
            setView('chat')
          }}
          onPrompt={(text, images) => {
            if (!activeAgent) return
            return s.send('agent.prompt', { agentId: activeAgent.id, text, images })
          }}
          onStop={() => activeAgent && s.send('agent.stop', { id: activeAgent.id })}
          hasOlder={activeAgent ? !!s.hasOlder[activeAgent.id] : false}
          onLoadOlder={() => activeAgent && s.loadOlder(activeAgent.id)}
          onShell={(command) =>
            s.send('shell.run', { agentId: activeAgent?.id ?? '', command })
          }
          shellLogs={activeAgent ? (s.shellByAgent[activeAgent.id] ?? []) : []}
          steps={activeAgent ? s.tools.filter((t) => t.id === activeAgent.id).slice(-8) : []}
          reasoning={activeAgent ? s.reasoning[activeAgent.id] : ''}
          pace={activeAgent ? s.pace?.[activeAgent.id] : undefined}
          onRoute={(targetName, text) => {
            if (!activeAgent) return
            const target = s.agents.find((a) => a.name === targetName || a.id === targetName)
            if (target) {
              s.send('agent.route', { fromId: activeAgent.id, toId: target.id, text })
            }
          }}
          onConfig={(patch) => {
            if (!activeAgent) return
            if (patch.workDir !== undefined) {
              s.send('agent.workDir', { id: activeAgent.id, dir: patch.workDir })
            }
            if (patch.extraDirs !== undefined) {
              API.setAgentExtraDirs(activeAgent.id, patch.extraDirs).catch(() => {})
            }
            if (patch.llm !== undefined) {
              s.send('agent.llm', { id: activeAgent.id, cfg: patch.llm })
            }
          }}
          connection={{
            provider: s.llm.provider,
            baseUrl: s.llm.baseUrl,
            apiKey: s.llm.apiKey,
            model: s.llm.model,
          }}
          inspectorOpen={inspectorOpen}
          onToggleInspector={() => setInspectorOpen((v) => !v)}
          onOpenChanges={() => {
            setTab('changes')
            setInspectorOpen(true)
          }}
          onQuickCreate={async (name, role) => {
            const created = await s.send<Agent | null>('agent.create', { name, role })
            if (created) setActive({ kind: 'agent', id: created.id })
          }}
          onCreateRequest={() => setCreateOpen(true)}
          onOpenKnowledge={openKnowledge}
          onProfile={(name, role) => {
            if (!activeAgent) return
            s.send('agent.profile', { id: activeAgent.id, name, role })
          }}
        />
      )}

      {inspectorOpen && (
        <Inspector
          tab={tab}
          onTab={setTab}
          tools={s.tools}
          onClose={() => setInspectorOpen(false)}
          caps={{ node: <CapabilityPanel agentId={activeAgent?.id ?? null} /> }}
          knowledge={{
            count: s.notes.length,
            node: (
              <KnowledgePanel
                notes={s.notes}
                agents={s.agents}
                onUpsert={(n) => s.send('note.upsert', n)}
                onDelete={(id) => s.send('note.delete', { id })}
                onAttach={(noteId, agentId) => s.send('note.attach', { noteId, agentId })}
                onDetach={(noteId, agentId) => s.send('note.detach', { noteId, agentId })}
              />
            ),
          }}
        />
      )}
      </div>

      <SettingsModal
        open={settingsOpen}
        llm={s.llm}
        version={s.version}
        onClose={() => setSettingsOpen(false)}
        onSave={(cfg: LLMConfig) => s.send('llm.config', cfg)}
      />

      <ApprovalModal
        open={approvalOpen}
        items={s.approvals}
        agents={s.agents}
        onDecide={s.decideApproval}
      />

      <ToastContainer />

      <QuickSwitch
        agents={s.agents}
        channels={s.channels}
        rooms={s.feishuRooms}
        onSelect={(kind, id) => {
          setActive({ kind, id })
          setView('chat')
        }}
      />
      </div>
    </>
  )
}
