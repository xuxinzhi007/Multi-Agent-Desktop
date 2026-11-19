import assert from 'node:assert/strict'
import { contextLimit, contextMeter, estimateTokens } from './contextMeter.ts'

assert.equal(contextLimit('qoder'), 131072)
assert.equal(contextLimit('qoder', 4096), 4096)
assert.equal(estimateTokens('请求参数'), 4)
assert.equal(estimateTokens('abcd'), 1)

const estimated = contextMeter({
  messages: [{ content: '请求参数' }],
  provider: 'qoder',
})
assert.equal(estimated.estimated, true)
assert.equal(estimated.limit, 131072)
assert.ok(estimated.used > 0)

const measured = contextMeter({
  messages: [{ content: '请求参数' }],
  provider: 'qoder',
  usage: { in: 800, out: 200, lastIn: 500 },
})
assert.equal(measured.estimated, false)
assert.equal(measured.used, 500)
assert.equal(measured.spent, 1000)

console.log('contextMeter ok')
