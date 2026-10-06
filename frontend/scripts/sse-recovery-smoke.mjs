import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { mergeLogEvents } from '../src/logStream.ts'

const wait = ms => new Promise(resolve => setTimeout(resolve, ms))
const waitFor = async predicate => {
  for (let i = 0; i < 100; i++) {
    if (predicate()) return
    await wait(5)
  }
  assert.fail('timed out waiting for SSE recovery state')
}
const event = (cursor, overrides = {}) => ({
  id: `log-${cursor}`,
  cursor,
  timestamp: new Date().toISOString(),
  type: 'SOCKET', method: 'FRAME', path: '/channel', status: 200, duration_ms: 0,
  ...overrides,
})

const helpers = ts.transpileModule(readFileSync(new URL('../src/logStream.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext },
}).outputText
const helpersURL = `data:text/javascript;base64,${Buffer.from(helpers).toString('base64')}`
const reactURL = `data:text/javascript,${encodeURIComponent('export const useRef = current => ({ current }); export const useEffect = effect => { globalThis.__sseCleanup = effect(); };')}`
const hookSource = readFileSync(new URL('../src/hooks/useSSE.ts', import.meta.url), 'utf8')
  .replace("from '../logStream'", `from '${helpersURL}'`)
  .replace("from 'react'", `from '${reactURL}'`)
const hook = ts.transpileModule(hookSource, {
  compilerOptions: { target: ts.ScriptTarget.ES2020, module: ts.ModuleKind.ESNext },
}).outputText
const hookURL = `data:text/javascript;base64,${Buffer.from(hook).toString('base64')}`
const { useSSE } = await import(hookURL)

globalThis.document = {
  visibilityState: 'visible',
  addEventListener() {},
  removeEventListener() {},
}
class FakeEventSource {
  static instances = []
  constructor(url) { this.url = url; FakeEventSource.instances.push(this); queueMicrotask(() => this.onopen?.()) }
  send(value) { this.onmessage?.({ data: JSON.stringify(value), lastEventId: value.cursor || '' }) }
  close() { this.closed = true }
}
globalThis.EventSource = FakeEventSource

const recoveries = []
let rows = []
const notices = []
useSSE(batch => { rows = mergeLogEvents(rows, batch, 5000) }, () => {}, () => {}, () => {}, (since, marker) => new Promise(resolve => recoveries.push({ since, marker, resolve })), (gap, marker) => {
  if (gap?.reason === 'server_reset') rows = []
  notices.push(gap?.reason || marker.gap_reason)
})
const stream = FakeEventSource.instances.at(-1)
stream.send(event('session:1'))
await wait(65)
stream.send(event('', { id: 'gap-1', type: 'GAP', method: 'GAP', stream_gap: true, gap_reason: 'slow_client' }))
await waitFor(() => recoveries.length === 1)
assert.equal(recoveries[0].since, 'session:1')
stream.send(event('session:5'))
stream.send(event('', { id: 'gap-2', type: 'GAP', method: 'GAP', stream_gap: true, gap_reason: 'slow_client' }))
await wait(65)
assert.deepEqual(rows.map(row => row.cursor), ['session:1'])
recoveries[0].resolve({ events: [event('session:2')], latest_cursor: 'session:2', has_more: false })
await waitFor(() => recoveries.length === 2)
assert.equal(recoveries[1].since, 'session:2', 'a queued GAP must not advance the cursor to later pending events')
recoveries[1].resolve({ events: [event('session:3'), event('session:4'), event('session:5')], latest_cursor: 'session:5', has_more: false })
await wait(70)
assert.deepEqual(rows.map(row => row.cursor), ['session:1', 'session:2', 'session:3', 'session:4', 'session:5'])
globalThis.__sseCleanup?.()

const resetRecoveries = []
rows = []
useSSE(batch => { rows = mergeLogEvents(rows, batch, 5000) }, () => {}, () => {}, () => {}, since => new Promise(resolve => resetRecoveries.push({ since, resolve })), (gap) => {
  if (gap?.reason === 'server_reset') rows = []
})
const resetStream = FakeEventSource.instances.at(-1)
resetStream.send(event('old:1'))
await wait(65)
resetStream.send(event('', { id: 'reset-gap', type: 'GAP', method: 'GAP', stream_gap: true, gap_reason: 'server_reset' }))
await waitFor(() => resetRecoveries.length === 1)
resetStream.send(event('old:2'))
resetStream.send(event('new:1', { id: 'log-new:1' }))
await wait(65)
resetRecoveries[0].resolve({ events: [event('new:1', { id: 'log-new:1' })], latest_cursor: 'new:1', has_more: false, gap: { reason: 'server_reset' } })
await wait(70)
assert.deepEqual(rows.map(row => row.cursor), ['new:1'], 'server reset must discard pending events from the old session')
globalThis.__sseCleanup?.()

const lateRecoveries = []
let lateEvents = 0
useSSE(batch => { lateEvents += batch.length }, () => {}, () => {}, () => {}, () => new Promise(resolve => lateRecoveries.push(resolve)), () => {})
const lateStream = FakeEventSource.instances.at(-1)
lateStream.send(event('', { id: 'late-gap', type: 'GAP', method: 'GAP', stream_gap: true }))
await waitFor(() => lateRecoveries.length === 1)
globalThis.__sseCleanup?.()
lateRecoveries[0]({ events: [event('late:1')], latest_cursor: 'late:1', has_more: false })
await wait(10)
assert.equal(lateEvents, 0, 'late recovery after cleanup must not mutate state')

globalThis.__sseCleanup?.()
console.log('SSE recovery smoke passed')
