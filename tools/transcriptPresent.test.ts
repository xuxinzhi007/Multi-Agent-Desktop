import assert from 'node:assert/strict'
import { foldAssistantTranscript, groupShellLogs, redactCommand } from '../src/lib/transcriptPresent.ts'

const bearer = redactCommand('curl -H "Authorization: Bearer sk-live-7f3a91c2e9" http://127.0.0.1/v1')
assert.equal(bearer.includes('sk-live-7f3a91c2e9'), false)
assert.match(bearer, /Bearer ••••c2e9/)
const personal = redactCommand('curl -H "x-api-key: pt-live-abcdef" https://example.test')
assert.equal(personal.includes('pt-live-abcdef'), false)
assert.match(personal, /pt-••••cdef/)
console.log('ok redact hides bearer')

const groups = groupShellLogs([
  { id: '1', command: 'cd /tmp/app', at: 1 },
  { id: '2', command: 'cd /tmp/app', at: 2 },
  { id: '3', command: 'cd /tmp/app', at: 3 },
  { id: '4', command: 'ls', at: 4 },
])
assert.equal(groups.length, 2)
assert.equal(groups[0].kind, 'cd')
if (groups[0].kind === 'cd') assert.equal(groups[0].count, 3)
console.log('ok group collapses repeated cd')

const folded = foldAssistantTranscript([
  { role: 'user', content: '抓接口' },
  { role: 'assistant', content: 'Let me count the tables.', reasoning: '先数表格', toolCalls: [{}] },
  { role: 'tool', content: '7 tables', toolId: 't1' },
  { role: 'assistant', content: '接口有 7 张表。', reasoning: '可以回答了' },
  { role: 'assistant', content: '简介已改成前端。' },
])
assert.equal(folded.length, 3)
assert.equal(folded[1].item.content, '接口有 7 张表。')
assert.equal(folded[1].item.reasoning, '先数表格\n\n可以回答了')
assert.equal(folded[1].sources.length, 2)
assert.equal(folded[2].item.content, '简介已改成前端。')
const again = foldAssistantTranscript([
  { role: 'assistant', content: 'Let me count.', reasoning: '先数表格', toolCalls: [{}] },
  { role: 'assistant', content: '接口有 7 张表。', reasoning: '先数表格\n\n可以回答了' },
])
assert.equal(again[0].item.reasoning, '先数表格\n\n可以回答了')
console.log('ok tool-round narration folds into the answer')

const withError = foldAssistantTranscript([
  { role: 'user', content: '去做' },
  { role: 'system', content: '运行错误：这个会话还没有模型' },
  { role: 'system', content: '你在工作' },
])
assert.equal(withError.length, 2)
assert.equal(withError[1].item.role, 'system')
assert.equal(withError[1].item.content, '运行错误：这个会话还没有模型')
console.log('ok runtime error stays visible and the role prompt does not')
