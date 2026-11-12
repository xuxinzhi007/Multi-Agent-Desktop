import { useEffect, useMemo, useState } from 'react'
import Sidebar from './components/Sidebar'
import AgentView from './components/AgentView'
import Inspector, { type InspectorTab } from './components/Inspector'
import ShellPanel from './components/ShellPanel'
import SettingsModal from './components/SettingsModal'
import ActivityFeed from './components/ActivityFeed'
import ToastContainer from './components/Toast'
import { useSidecar } from './lib/useSidecar'
import { useTheme } from './lib/useTheme'
import type { LLMConfig } from './lib/useSidecar'

export default function App() {
  const s = useSidecar()
  useTheme() // 应用主题到 document.documentElement
  const [activeId, setActiveId] = useState<string | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [shellOpen, setShellOpen] = useState(false)
  const [inspectorOpen, setInspectorOpen] = useState(true)
  const [tab, setTab] = useState<InspectorTab>('changes')

  useEffect(() => {
    if (!activeId && s.agents.length > 0) setActiveId(s.agents[0].id)
    if (activeId && !s.agents.some((a) => a.id === activeId)) {
      setActiveId(s.agents[0]?.id ?? null)
    }
  }, [s.agents, activeId])

  const active = useMemo(() => s.agents.find((a) => a.id === activeId) ?? null, [s.agents, activeId])
  const streaming = activeId ? (s.streaming[activeId] ?? '') : ''
  const err = activeId ? s.errors[activeId] : undefined

  return (
    <div className="c-shell">
      <Sidebar
        agents={s.agents}
        activeId={activeId}
        connected={s.connected}
        version={s.version}
        provider={s.llm.provider}
        onSelect={setActiveId}
        onCreate={(name, role, backend) =>
          backend === 'mock'
            ? s.send('agent.create', { name, role })
            : s.send('agent.createCLI', { name, role, backend })
        }
        onDelete={(id) => s.send('agent.delete', { id })}
        onOpenSettings={() => setSettingsOpen(true)}
      />

      <AgentView
        agent={active}
        streaming={streaming}
        error={err}
        workDir={s.workDir}
        tokenUsage={activeId ? s.tokenUsage[activeId] : undefined}
        agents={s.agents}
        onPrompt={(text) => active && s.send('agent.prompt', { agentId: active.id, text })}
        onStop={() => active && s.send('agent.stop', { id: active.id })}
        onShell={(command) => {
          setShellOpen(true)
          s.send('shell.run', { agentId: active?.id ?? '', command })
        }}
        onRoute={(targetName, text) => {
          if (!active) return
          const target = s.agents.find((a) => a.name === targetName || a.id === targetName)
          if (target) {
            s.send('agent.route', { fromId: active.id, toId: target.id, text })
          }
        }}
        inspectorOpen={inspectorOpen}
        onToggleInspector={() => setInspectorOpen((v) => !v)}
      />

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
        />
      )}

      <SettingsModal
        open={settingsOpen}
        llm={s.llm}
        workDir={s.workDir}
        onClose={() => setSettingsOpen(false)}
        onSave={(cfg: LLMConfig) => s.send('llm.config', cfg)}
        onWorkDir={(dir) => s.send('workdir.set', { dir })}
      />

      <ActivityFeed activities={s.activities} />
      <ToastContainer />
    </div>
  )
}