import assert from 'node:assert/strict'
import test from 'node:test'
import { resolveRelayAPIPath, resolveRelayAPIURL, resolveRelayWebSocketURL } from '../../web/utils/relayURLs.ts'

test('all management families use direct or product-namespaced paths', () => {
  for (const resource of ['session', 'ws', 'system', 'page-data/providers', 'capacity-snapshot', 'health', 'providers', 'credentials', 'endpoints', 'routing-lanes', 'lane-memberships', 'limit-policies', 'observed-limits', 'pricing-policies', 'guardrails', 'guardrail-presets', 'guardrail-bindings', 'settings', 'stats/usage', 'logs/requests', 'queue/items', 'suggestions/ranks', 'simulate/lane', 'preview/live-flow/sessions', 'pro/smart-groups', 'pro/provider-connections', 'pro/user-limit-policies', 'pro/api-key-limit-policies', 'pro/self-limit-policies', 'pro/characterization/settings', 'pro/request-characterization-feedback']) {
    assert.equal(resolveRelayAPIURL(`/api/${resource}`), `/api/${resource}`)
    assert.equal(resolveRelayAPIURL(`/api/${resource}`, '', '/api/relay'), `/api/relay/${resource}`)
    assert.equal(resolveRelayAPIURL(`/api/${resource}`, 'https://api.example.test/', '/api/relay/'), `https://api.example.test/api/relay/${resource}`)
  }
})

test('query strings, encoded IDs, and inference paths are preserved', () => {
  const path = '/api/pro/provider-connections/connection/models/model%2Fname/enable?value=a%2Fb&value=c'
  assert.equal(resolveRelayAPIPath(path, '/api/relay'), path.replace('/api/', '/api/relay/'))
  assert.equal(resolveRelayAPIPath('/api', '/api/relay'), '/api/relay')
  assert.equal(resolveRelayAPIPath('/v1/chat/completions', '/api/relay'), '/v1/chat/completions')
  assert.equal(resolveRelayAPIPath('/apiary', '/api/relay'), '/apiary')
})

test('telemetry and preview sockets retain the correct host, prefix, and transport', () => {
  for (const path of ['/api/ws?limit_visibility=organization', '/api/preview/live-flow/sessions/id%2Fencoded/ws']) {
    assert.equal(resolveRelayWebSocketURL(path, '', 'https://relay.example.test'), `wss://relay.example.test${path}`)
    assert.equal(resolveRelayWebSocketURL(path, 'http://127.0.0.1:8091', 'http://localhost:3040', '/api/relay'), `ws://localhost:8091${path.replace('/api/', '/api/relay/')}`)
    assert.equal(resolveRelayWebSocketURL(path, 'wss://api.example.test', 'https://app.example.test', '/api/relay'), `wss://api.example.test${path.replace('/api/', '/api/relay/')}`)
    assert.equal(resolveRelayWebSocketURL(path, 'invalid', 'https://app.example.test', '/api/relay'), `wss://app.example.test${path.replace('/api/', '/api/relay/')}`)
  }
})
