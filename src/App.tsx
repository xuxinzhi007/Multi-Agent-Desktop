import { useEffect, useMemo, useState } from 'react'
import Sidebar from './components/Sidebar'
import AgentView from './components/AgentView'
import ShellPanel from './components/ShellPanel'
import SettingsModal from './components/SettingsModal'
import { useSidecar } from './lib/useSidecar'
import type { LLMConfig } from './lib/ws'

export default function App() {
  const s = useSidecar()
  const [activeId, setActiveId] = useState<string | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [shellOpen, setShellOpen] = useState(false)

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
    <div style={{ display: 'flex', height: '100vh', width: '100vw', overflow: 'hidden' }}>
      <Sidebar
        agents={s.agents}
        activeId={activeId}
        connected={s.connected}
        version={s.version}
        onSelect={setActiveId}
        onCreate={(name, role) => s.send('agent.create', { name, role })}
        onDelete={(id) => s.send('agent.delete', { id })}
        onOpenSettings={() => setSettingsOpen(true)}
      />

      <AgentView
        agent={active}
        streaming={streaming}
        error={err}
        workDir={s.workDir}
        onPrompt={(text) => active && s.send('agent.prompt', { agentId: active.id, text })}
        onStop={() => active && s.send('agent.stop', { id: active.id })}
        onShell={(command) => {
          setShellOpen(true)
          s.send('shell.run', { agentId: active?.id ?? '', command })
        }}
      />

      {shellOpen && (
        <ShellPanel logs={s.shell} onClose={() => setShellOpen(false)} />
      )}

      <SettingsModal
        open={settingsOpen}
        llm={s.llm}
        workDir={s.workDir}
        onClose={() => setSettingsOpen(false)}
        onSave={(cfg: LLMConfig) => s.send('llm.config', cfg)}
        onWorkDir={(dir) => s.send('workdir.set', { dir })}
      />
    </div>
  )
}