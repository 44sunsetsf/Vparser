import { test } from 'node:test'
import assert from 'node:assert/strict'

// minimal localStorage for node
const store = new Map()
globalThis.localStorage = {
  getItem: (k) => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: (k) => store.delete(k)
}
const { rememberTaskStart, forgetTaskStart, taskStartKey } = await import('./taskStart.js')

test('a task keeps its first start time until it is forgotten', () => {
  const key = taskStartKey(7, 'ai', 'GENERAL:goal')
  const first = rememberTaskStart(key, Date.now() - 30_000)
  assert.equal(rememberTaskStart(key), first)                  // reopening the panel: same start
  const again = Date.now()
  assert.equal(rememberTaskStart(key, again, true), again)     // submitted again: new start
  forgetTaskStart(key)
  assert.ok(rememberTaskStart(key) >= again)                   // finished and forgotten: a fresh clock
})

test('stale entries are dropped', () => {
  const key = taskStartKey(8, 'text', '')
  rememberTaskStart(key, Date.now() - 7 * 60 * 60 * 1000, true)   // 7 h ago, older than the 6 h TTL
  assert.ok(Date.now() - rememberTaskStart(key) < 1000)
})
