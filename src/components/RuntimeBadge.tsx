import type { ACPConfig } from '../mygo'
import { runtimeBadge, runtimeStateOf } from '../lib/runtime'

type RuntimeBadgeSource = {
  runtime?: string
  acp?: ACPConfig
  runtimeState?: string
  runtimeError?: string
}

/**
 * 会话运行时徽章：原生 / ACP · <名称> / ACP · 未配置。
 *
 * 连接状态只认真实的 runtimeState + runtimeError（契约 §3）；
 * 状态为空时不显示点，不伪造 running/exited。
 */
export default function RuntimeBadge({ agent }: { agent: RuntimeBadgeSource }) {
  const badge = runtimeBadge(agent)
  const state = runtimeStateOf(agent.runtimeState, agent.runtimeError)

  const variant = !badge.configured
    ? 'c-fleet-pill--danger'
    : agent.runtime === 'acp'
      ? 'c-fleet-pill--accent'
      : 'c-fleet-pill--muted'

  const title = [badge.label, state.label, agent.runtimeError?.trim()].filter(Boolean).join(' · ')

  return (
    <span
      className={`c-fleet-pill ${variant}`}
      title={title}
      style={{ flexShrink: 0, fontWeight: 500, maxWidth: 120 }}
    >
      {state.tone !== 'none' && (
        <span className={`c-dot c-dot--${state.tone}`} style={{ width: 5, height: 5 }} />
      )}
      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{badge.label}</span>
    </span>
  )
}
