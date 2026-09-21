import assert from 'node:assert/strict'
import test from 'node:test'
import { useRelayApi } from '../../web/composables/useRelayApi.ts'
import { resolveRelayAPIURL } from '../../web/utils/relayURLs.ts'

test('shared API client preserves credentials, methods, bodies, and queries in both hosts', async (t) => {
  let prefix = '/api'
  let token = 'local-test-token'
  const calls = []
  const response = { fixture: 'unchanged' }
  t.mock.method(globalThis, 'fetch', () => { throw new Error('real network is forbidden') })
  // Define runtime mocks without augmenting Nuxt's ambient TypeScript globals.
  Object.defineProperties(globalThis, {
    useAuthStore: { configurable: true, value: () => ({ token }) },
    useRelayURLs: { configurable: true, value: () => ({ apiURL: path => resolveRelayAPIURL(path, '', prefix) }) },
    $fetch: { configurable: true, value: async (url, options) => { calls.push({ url, options }); return response } }
  })
  t.after(() => {
    for (const name of ['useAuthStore', 'useRelayURLs', '$fetch']) Reflect.deleteProperty(globalThis, name)
  })
  for (prefix of ['/api', '/api/relay']) {
    const api = useRelayApi()
    const body = { name: 'Example', enabled: false }
    assert.equal(await api.post('/api/providers', body), response)
    assert.deepEqual(calls.at(-1), {
      url: `${prefix}/providers`,
      options: { method: 'POST', body: JSON.stringify(body), credentials: 'include', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' } }
    })
    await api.put('/api/settings', body)
    assert.equal(calls.at(-1).options.method, 'PUT')
    await api.del('/api/guardrails/example')
    assert.equal(calls.at(-1).url, `${prefix}/guardrails/example`)
    assert.equal(calls.at(-1).options.method, 'DELETE')
    await api.requestLogs({ limit: 5, user_uuid: ['a', 'b'], guardrail_applied: false })
    assert.equal(calls.at(-1).url, `${prefix}/logs/requests?limit=5&guardrail_applied=false&user_uuid=a&user_uuid=b`)
    assert.equal(calls.at(-1).options.method, 'GET')
    await api.sessionStatus()
    assert.equal(calls.at(-1).url, `${prefix}/session`)
    const file = new Blob(['fixture'])
    await api.post('/api/pro/provider-connections/example/import', file, { 'X-Fixture': 'unchanged' })
    assert.equal(calls.at(-1).options.body, file)
    assert.equal(calls.at(-1).options.headers['X-Fixture'], 'unchanged')
    assert.equal(calls.at(-1).options.headers['Content-Type'], undefined)
  }
  token = ''
  await useRelayApi().get('/api/providers')
  assert.deepEqual(calls.at(-1).options.headers, {})
  assert.equal(calls.at(-1).options.credentials, 'include')
})
