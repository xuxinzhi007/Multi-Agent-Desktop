export type Message = {
  role: 'system' | 'user' | 'assistant'
  content: string
}

export const ROLE_PRESETS = [
  { name: '前端', role: 'React / TypeScript 专家，关注组件拆分、状态管理和渲染性能' },
  { name: '后端', role: 'Go 专家，关注 API 设计、并发安全和数据库查询效率' },
  { name: '测试', role: 'QA 专家，关注边界条件、异常路径和可复现的验证步骤' },
  { name: '架构', role: '系统架构师，关注模块边界、依赖方向和长期演进成本' },
  { name: 'Reviewer', role: '代码审查者，指出风险、异味和不符合规范的地方' },
]

export const PROVIDERS = [
  { id: 'mock', label: 'Mock（内置，无需配置）', hint: '本地规则引擎，用来体验多 agent 并行' },
  { id: 'ollama', label: 'Ollama（本地）', hint: '默认 http://127.0.0.1:11434' },
  { id: 'openai', label: 'OpenAI 兼容', hint: 'OpenAI / 各类网关' },
  { id: 'deepseek', label: 'DeepSeek', hint: 'https://api.deepseek.com' },
  { id: 'claude', label: 'Claude（OpenAI 兼容网关）', hint: '需提供兼容网关地址' },
]

export function prettyTime(ms: number) {
  const d = new Date(ms)
  return d.toTimeString().slice(0, 8)
}