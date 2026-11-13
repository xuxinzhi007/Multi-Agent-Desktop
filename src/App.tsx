import { useEffect, useMemo, useState } from 'react'
import Sidebar from './components/Sidebar'
import AgentView from './components/AgentView'
import ChannelView from './components/ChannelView'
import Inspector, { type InspectorTab } from './components/Inspector'
import KnowledgePanel from './components/KnowledgePanel'
import ShellPanel from './components/ShellPanel'
import SettingsModal from './components/SettingsModal'
import ActivityFeed from './components/ActivityFeed'
import ToastContainer from './components/Toast'
import { useSidecar } from './lib/useSidecar'
import { useTheme } from './lib/useTheme'
import type { LLMConfig } from './lib/useSidecar'

type ActiveRef = { kind: 'agent' | 'channel'; id: string } | null

export default function App() {
  const s = useSidecar()
  useTheme() // 应用主题到 document.documentElement
  const [active, setActive] = useState<ActiveRef>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [shellOpen, setShellOpen] = useState(false)
  const [inspectorOpen, setInspectorOpen] = useState(true)
  const [tab, setTab] = useState<InspectorTab>('changes')

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

  const activeAgent = useMemo(
    () => (active?.kind === 'agent' ? (s.agents.find((a) => a.id === active.id) ?? null) : null),
    [s.agents, active],
  )
  const activeChannel = useMemo(
    () => (active?.kind === 'channel' ? (s.channels.find((c) => c.id === active.id) ?? null) : null),
    [s.channels, active],
  )
  const streaming = activeAgent ? s.streaming[activeAgent.id] ?? '' : ''
  const err = activeAgent ? s.errors[activeAgent.id] : undefined

  const createAgent = async (name: string, role: string, backend: string, workDir?: string) => {
    const created =
      backend === 'mock'
        ? await s.send('agent.create', { name, role })
        : await s.send('agent.createCLI', { name, role, backend })
    // 两阶段：先创建，再写入会话级工作目录（避免改 CreateAgentRequest 形状）
    if (created && workDir) {
      await s.send('agent.workDir', { id: created.id, dir: workDir })
    }
    if (created) setActive({ kind: 'agent', id: created.id })
  }

  return (
    <div className="c-shell">
      <Sidebar
        agents={s.agents}
        channels={s.channels}
        activeId={active?.id ?? null}
        activeKind={active?.kind ?? null}
        connected={s.connected}
        version={s.version}
        provider={s.llm.provider}
        onSelect={(kind, id) => setActive({ kind, id })}
        onCreate={createAgent}
        onDelete={(id) => s.send('agent.delete', { id })}
        onCreateChannel={(name, members) => s.send('channel.create', { name, members })}
        onOpenSettings={() => setSettingsOpen(true)}
      />

      {activeChannel ? (
        <ChannelView
          channel={activeChannel}
          agents={s.agents}
          streaming={s.streaming}
          onSend={(text) =>
            s.send('channel.send', {
              channelId: activeChannel.id,
              fromId: 'user',
              fromName: '你',
              text,
            })
          }
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
          onPrompt={(text) => activeAgent && s.send('agent.prompt', { agentId: activeAgent.id, text })}
          onStop={() => activeAgent && s.send('agent.stop', { id: activeAgent.id })}
          onShell={(command) => {
            setShellOpen(true)
            s.send('shell.run', { agentId: activeAgent?.id ?? '', command })
          }}
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
          inspectorOpen={inspectorOpen}
          onToggleInspector={() => setInspectorOpen((v) => !v)}
        />
      )}

      {shellOpen && <ShellPanel logs={s.shell} onClose={() => setShellOpen(false)} />}

      {inspectorOpen && (
        <Inspector
          tab={tab}
          onTab={setTab}
          changes={s.changes}
          todos={s.todos}
          tools={s.tools}
          onClose={() => setInspectorOpen(false)}
          onApprove={s.approveTool}
          onReject={s.rejectTool}
          knowledge={{
            count: s.notes.length,
            node: (
              <KnowledgePanel
                notes={s.notes}
                agents={s.agents}
                onUpsert={(n) => s.send('note.upsert', n)}
                onDelete={(id) => s.send('note.delete', { id })}
                onAttach={(noteId, agentId) => s.send('note.attach', { noteId, agentId })}
              />
            ),
          }}
        />
      )}

      <SettingsModal
        open={settingsOpen}
        llm={s.llm}
        onClose={() => setSettingsOpen(false)}
        onSave={(cfg: LLMConfig) => s.send('llm.config', cfg)}
      />

      <ActivityFeed activities={s.activities} />
      <ToastContainer />
    </div>
  )
}
