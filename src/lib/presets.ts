export const ROLE_PRESETS = [
  { name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理与渲染性能' },
  { name: '后端', role: 'Go 专家，关注 API 设计、并发安全与查询效率' },
  { name: '测试', role: 'QA 专家，关注边界条件、异常路径与可复现的验证步骤' },
  { name: '架构', role: '系统架构师，关注模块边界、依赖方向与长期演进成本' },
  { name: 'Reviewer', role: '代码审查者，指出风险、代码异味与不符合规范的地方' },
]

export const PROVIDERS = [
  { id: 'mock', label: 'Mock（内置）', hint: '本地规则引擎，无需任何配置' },
  { id: 'ollama', label: 'Ollama', hint: '默认 http://127.0.0.1:11434' },
  { id: 'openai', label: 'OpenAI 兼容', hint: 'OpenAI 或各类兼容网关' },
  { id: 'deepseek', label: 'DeepSeek', hint: 'https://api.deepseek.com' },
  { id: 'claude', label: 'Claude', hint: '需 OpenAI 兼容网关' },
]

export const DEFAULT_BASE: Record<string, string> = {
  ollama: 'http://127.0.0.1:11434',
  openai: 'https://api.openai.com',
  deepseek: 'https://api.deepseek.com',
  claude: 'https://api.anthropic.com',
}

export const DEFAULT_MODEL: Record<string, string> = {
  ollama: 'qwen2.5-coder:7b',
  openai: 'gpt-4o-mini',
  deepseek: 'deepseek-chat',
  claude: 'claude-3-5-sonnet-20241022',
}

export const AGENT_COLORS = [
  'var(--agent-1)',
  'var(--agent-2)',
  'var(--agent-3)',
  'var(--agent-4)',
  'var(--agent-5)',
  'var(--agent-6)',
]

export function prettyTime(ms: number) {
  return new Date(ms).toTimeString().slice(0, 8)
}