export type TreeAgent = {
  id: string
  name: string
  parentId?: string
  messages?: { content?: string; toolCalls?: { name?: string; input?: unknown }[] }[]
}

export type AgentNode<T> = { agent: T; children: AgentNode<T>[] }

const createdName = /已创建「([^」]+)」/g

function parentOfMap(agents: { id: string; parentId?: string }[]): Map<string, string> {
  const ids = new Set(agents.map((a) => a.id))
  const out = new Map<string, string>()
  for (const a of agents) {
    if (a.parentId && ids.has(a.parentId) && a.parentId !== a.id) out.set(a.id, a.parentId)
  }
  return out
}

function cycles(childId: string, parentId: string, parentOf: Map<string, string>): boolean {
  let cur = parentId
  const seen = new Set<string>()
  while (cur) {
    if (cur === childId || seen.has(cur)) return true
    seen.add(cur)
    cur = parentOf.get(cur) ?? ''
  }
  return false
}

/** 已有 parentId 优先。没有时，从「已创建「名字」」把子会话挂回创建它的会话。 */
export function linkParents<T extends TreeAgent>(agents: T[]): T[] {
  const byName = new Map<string, string>()
  for (const a of agents) {
    if (a.name && !byName.has(a.name)) byName.set(a.name, a.id)
  }
  const parentOf = parentOfMap(agents)
  const inferred = new Map<string, string>()
  for (const a of agents) {
    const names = new Set<string>()
    for (const m of a.messages ?? []) {
      const text = m.content ?? ''
      if (!text.includes('已创建')) continue
      for (const hit of text.matchAll(createdName)) {
        if (hit[1]) names.add(hit[1])
      }
    }
    for (const name of names) {
      const childId = byName.get(name)
      if (!childId || childId === a.id || parentOf.has(childId) || inferred.has(childId)) continue
      if (cycles(childId, a.id, parentOf)) continue
      inferred.set(childId, a.id)
      parentOf.set(childId, a.id)
    }
  }
  if (inferred.size === 0) return agents
  return agents.map((a) => (a.parentId || !inferred.has(a.id) ? a : { ...a, parentId: inferred.get(a.id) }))
}

export function agentTree<T extends { id: string; parentId?: string }>(agents: T[]): AgentNode<T>[] {
  const ids = new Set(agents.map((a) => a.id))
  const children = new Map<string, T[]>()
  for (const a of agents) {
    if (!a.parentId || !ids.has(a.parentId) || a.parentId === a.id) continue
    children.set(a.parentId, [...(children.get(a.parentId) ?? []), a])
  }
  const seen = new Set<string>()
  const walk = (a: T): AgentNode<T> => {
    seen.add(a.id)
    const kids: AgentNode<T>[] = []
    for (const child of children.get(a.id) ?? []) {
      if (seen.has(child.id)) continue
      kids.push(walk(child))
    }
    return { agent: a, children: kids }
  }
  const out: AgentNode<T>[] = []
  for (const a of agents) {
    if (seen.has(a.id)) continue
    if (a.parentId && ids.has(a.parentId) && a.parentId !== a.id) continue
    out.push(walk(a))
  }
  for (const a of agents) {
    if (!seen.has(a.id)) out.push(walk(a))
  }
  return out
}
