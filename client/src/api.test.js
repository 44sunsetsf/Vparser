import assert from 'node:assert/strict'
import test from 'node:test'
import { apiRequest } from './api.js'

test('API network failures produce an actionable message', async () => {
  globalThis.localStorage = { getItem: () => null }
  globalThis.fetch = async () => {
    throw new TypeError('fetch failed')
  }

  await assert.rejects(apiRequest('/health'), /请确认后端已启动且地址配置正确/)
})

test('link-import refusals are shown in the page language, not as a raw server error', async () => {
  globalThis.localStorage = { getItem: () => null }
  globalThis.fetch = async () => new Response(JSON.stringify({ code: 42202, message: '该网站拒绝了服务器的下载请求', data: null }), {
    status: 422, headers: { 'content-type': 'application/json' }
  })
  const res = await apiRequest('/media/upload-url', { method: 'POST' })
  assert.equal(res.ok, false)
  assert.match(await res.text(), /保存到本地|Save the video|Spara videon/)
})
