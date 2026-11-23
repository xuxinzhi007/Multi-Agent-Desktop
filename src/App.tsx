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
import { useBackend } from './lib/useBackend'
import type { AgentView as Agent } from './mygo'
import { API } from './mygo'
import { useTheme } from './lib/useTheme'
import type { LLMConfig } from './lib/useBackend'

type ActiveRef = { kind: 'agent' | 'channel' | 'feishu'; id: string } | null
/** 主区域展示什么：舰队总览，还是单个 agent 的对话 */
type MainView = 'fleet' | 'chat'

export default function App() {
  const s = useBackend()
  useTheme() // 应用主题到 document.documentElement
  const [active, setActive] = useState<ActiveRef>(null)
  const [view, setView] = useState<MainView>('fleet')
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
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

  const createAgent = async (name: string, role: string, backend: string, workDir?: string) => {
    const created = await s.send<Agent | null>(
      backend === 'mock' ? 'agent.create' : 'agent.createCLI',
      backend === 'mock' ? { name, role } : { name, role, backend },
    )
    // 两阶段：先创建，再写入会话级工作目录（避免改 CreateAgentRequest 形状）
    if (created && workDir) {
      await s.send('agent.workDir', { id: created.id, dir: workDir })
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
      <div className="c-shell">
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

      {view === 'fleet' && !activeChannel && !activeFeishu ? (
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
          onSend={(text, _mentions, images) =>
            s.send('channel.send', {
              channelId: activeChannel.id,
              fromId: 'user',
              fromName: '你',
              text,
              images,
            })
          }
          onSetSpeaker={(agentId) =>
            s.send('channel.speaker', { channelId: activeChannel.id, agentId })
          }
          onDelete={() => s.send('channel.delete', { id: activeChannel.id })}
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
          onPrompt={(text, images) => {
            if (!activeAgent) return
            return s.send('agent.prompt', { agentId: activeAgent.id, text, images })
          }}
          onStop={() => activeAgent && s.send('agent.stop', { id: activeAgent.id })}
          onShell={(command) =>
            s.send('shell.run', { agentId: activeAgent?.id ?? '', command })
          }
          shellLogs={activeAgent ? (s.shellByAgent[activeAgent.id] ?? []) : []}
          steps={activeAgent ? s.tools.filter((t) => t.id === activeAgent.id).slice(-8) : []}
          reasoning={activeAgent ? s.reasoning[activeAgent.id] : ''}
          pace={activeAgent ? s.pace?.[activeAgent.id] : undefined}
          queued={activeAgent ? s.outbox[activeAgent.id] : undefined}
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

      <SettingsModal
        open={settingsOpen}
        llm={s.llm}
        version={s.version}
        onClose={() => setSettingsOpen(false)}
        onSave={(cfg: LLMConfig) => s.send('llm.config', cfg)}
      />

      <ApprovalModal items={s.approvals} onDecide={s.decideApproval} />

      <ToastContainer />
      </div>
    </>
  )
}
