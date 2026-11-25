import type { ACPConfig } from '../mygo'

/** 运行时取值，契约 §1：native 进程内 loop / acp 外部进程。 */
export type RuntimeKind = 'native' | 'acp'

/** ACP 表单提交给上层的配置（native 时不传）。 */
export type AcpForm = {
  name: string
  command: string
  args: string[]
  env: string[]
}

/** 参数 / 环境变量：每行一条，去空白、去空项（契约 §2 规则）。 */
export function splitLines(text: string): string[] {
  return text
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
}

/** 命令 basename：兼容 / 与 \ 两种路径分隔符。 */
export function acpBasename(command: string): string {
  const seg = command.split(/[\\/]/).filter(Boolean)
  return seg.length ? seg[seg.length - 1] : command
}

export type RuntimeBadge = {
  /** 徽章主文案：原生 / ACP · <名称> / ACP · 未配置 */
  label: string
  /** false 表示 ACP 已选但命令为空，需要提示用户补全 */
  configured: boolean
}

/** 会话徽章文案。runtime 缺省按 native（契约 §3：空值归一 native）。 */
export function runtimeBadge(agent: { runtime?: string; acp?: ACPConfig }): RuntimeBadge {
  if (agent.runtime === 'acp') {
    const cmd = agent.acp?.command?.trim() ?? ''
    if (!cmd) return { label: 'ACP · 未配置', configured: false }
    const name = agent.acp?.name?.trim() || acpBasename(cmd)
    return { label: `ACP · ${name}`, configured: true }
  }
  return { label: '原生', configured: true }
}

/** 连接状态对应的点色与文案，只认真实 runtimeState；空值不编状态。 */
export type RuntimeState = {
  tone: 'none' | 'queued' | 'running' | 'idle' | 'error'
  label: string
}

export function runtimeStateOf(state?: string, error?: string): RuntimeState {
  switch (state) {
    case 'starting':
      return { tone: 'queued', label: '连接中' }
    case 'running':
      return { tone: 'running', label: '运行中' }
    case 'idle':
      return { tone: 'idle', label: '空闲' }
    case 'error':
      return { tone: 'error', label: error?.trim() ? `错误：${error.trim()}` : '错误' }
    default:
      return { tone: 'none', label: '' }
  }
}
