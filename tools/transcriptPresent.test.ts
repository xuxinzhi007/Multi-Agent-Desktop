import assert from 'node:assert/strict'
import { groupShellLogs, redactCommand } from '../src/lib/transcriptPresent.ts'

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
