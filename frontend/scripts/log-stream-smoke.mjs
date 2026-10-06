import assert from 'node:assert/strict'
import { keepCursorSession, mergeLogEvents, recoveryCursor } from '../src/logStream.ts'
import { virtualRange } from '../src/virtual.ts'

const event = (seq, overrides = {}) => ({
  id: `log-session-${seq}`,
  cursor: `session:${seq}`,
  type: 'SOCKET',
  method: 'FRAME',
  path: '/channel',
  timestamp: new Date(seq).toISOString(),
  status: 200,
  duration_ms: 0,
  ...overrides,
})

const first = [event(1), event(2), event(3)]
const recovered = mergeLogEvents(first, [event(2), event(3), event(4), event(5), event(5), event(0, { type: 'GAP' })], 4)
assert.deepEqual(recovered.map(item => item.id), ['log-session-2', 'log-session-3', 'log-session-4', 'log-session-5'])
assert.deepEqual(mergeLogEvents(recovered, [event(4), event(5)], 4), recovered)
assert.equal(mergeLogEvents(recovered, Array.from({ length: 5001 }, (_, i) => event(i + 6)), 5000).length, 5000)
assert.equal(recoveryCursor('session:2', { events: [event(3)], latest_cursor: 'session:8', has_more: true }), 'session:3')
assert.equal(recoveryCursor('old:9', { events: [event(1)], latest_cursor: 'session:5', has_more: false }), 'session:5')
assert.equal(recoveryCursor('', { events: [], latest_cursor: '', has_more: false }), '')
assert.deepEqual(keepCursorSession([event(8), event(1, { id: 'new:2', cursor: 'new:2' })], 'new:1').map(item => item.id), ['new:2'])
assert.deepEqual(keepCursorSession([event(8)], undefined), [])

assert.deepEqual(virtualRange(1_000_000, 300, 36, 0), { start: 0, end: 0, top: 0, bottom: 0 })
assert.deepEqual(virtualRange(1_000_000, 300, 36, 10), { start: 10, end: 10, top: 360, bottom: 0 })
assert.deepEqual(virtualRange(72, 108, 36, 100), { start: 0, end: 13, top: 0, bottom: 3132 })
assert.deepEqual(virtualRange(72, 180, 36, 100), { start: 0, end: 15, top: 0, bottom: 3060 })
console.log('log stream and virtual window smoke passed')
