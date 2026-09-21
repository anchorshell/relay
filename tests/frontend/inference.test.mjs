import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { buildInferenceCurl, inferenceExampleURL, inferenceHeaders } from '../../web/utils/inference.ts'
import { useInferenceToken } from '../../web/composables/useInferenceToken.ts'

test('inference curl examples use the backend and a shell API-token reference', () => {
  const body = JSON.stringify({ model: 'Api.Airforce/glm-5', messages: [{ role: 'user', content: "What's next? $(not-executed)" }] }, null, 2)
  for (const browser of ['http://localhost:3030', 'http://127.0.0.1:3030', '']) {
    const url = inferenceExampleURL('/v1/chat/completions', browser)
    const curl = buildInferenceCurl(body, url)
    assert.ok(curl.startsWith("curl -X POST 'http://localhost:11730/v1/chat/completions'"))
    assert.ok(curl.includes('-H "Authorization: Bearer ${RELAY_API_TOKEN}"'))
    assert.ok(!curl.includes('RELAY_ADMIN_TOKEN'))
    assert.ok(curl.includes("What'\\''s next? $(not-executed)"))
  }
  assert.ok(buildInferenceCurl('{}').includes('localhost:11730'))
  assert.equal(inferenceExampleURL('/v1/models', 'http://localhost:3030', 'http://127.0.0.1:9000'), 'http://127.0.0.1:9000/v1/models')
  assert.equal(inferenceExampleURL('/v1/models', 'http://localhost:3030', 'ws://127.0.0.1:9000'), 'http://127.0.0.1:9000/v1/models')
  assert.equal(inferenceExampleURL('/v1/models', 'https://relay.example.test'), 'https://relay.example.test/v1/models')
  assert.equal(inferenceExampleURL('https://backend.example.test/v1/models', 'http://localhost:3030'), 'https://backend.example.test/v1/models')
})

test('live inference headers contain authentication and content type only', () => {
  assert.deepEqual(inferenceHeaders('example-inference-token'), {
    'Content-Type': 'application/json',
    Authorization: 'Bearer example-inference-token'
  })
  assert.deepEqual(inferenceHeaders(''), { 'Content-Type': 'application/json' })
  assert.throws(() => inferenceHeaders('secret\nvalue'), error => !error.message.includes('secret'))
})

test('Playground curl includes an explicitly supplied key and shell-quotes it literally', () => {
  const token = 'fixture-\'${CURL_TEST_VAR}-$(printf expanded)-`printf expanded`-"'
  const body = JSON.stringify({ model: 'fixture', messages: [{ role: 'user', content: "What's next? $(printf expanded)" }] }, null, 2)
  const curl = buildInferenceCurl(body, 'http://localhost:3040/v1/chat/completions', `  ${token}  `)
  // Override curl with a shell function: exercise real shell parsing without
  // executing curl, opening a connection, or using any actual credential.
  const args = execFileSync('/bin/sh', ['-c', `curl() { printf '%s\\0' "$@"; }\n${curl}`], {
    encoding: 'utf8', env: { PATH: '/usr/bin:/bin', CURL_TEST_VAR: 'must-not-expand' }
  }).split('\0').slice(0, -1)
  assert.deepEqual(args, ['-X', 'POST', 'http://localhost:3040/v1/chat/completions', '-H', `Authorization: Bearer ${token}`, '-H', 'Content-Type: application/json', '--data-raw', body])
  assert.ok(!curl.includes('${RELAY_API_TOKEN}'))
  assert.ok(buildInferenceCurl('{}', undefined, '   ').includes('${RELAY_API_TOKEN}'))
})

test('Playground and setup tokens are component-local and clear on page exit, lock, or unmount', t => {
  let lockCallback
  let mounted
  let unmount
  const events = new EventTarget()
  const globals = {
    ref: value => ({ value }),
    useAuthStore: () => ({ authenticated: true }),
    watch: (_getter, callback) => { lockCallback = callback },
    onMounted: callback => { mounted = callback },
    onBeforeUnmount: callback => { unmount = callback },
    window: events
  }
  for (const [name, value] of Object.entries(globals)) Object.defineProperty(globalThis, name, { value, configurable: true })
  t.after(() => { for (const name of Object.keys(globals)) Reflect.deleteProperty(globalThis, name) })
  const token = useInferenceToken()
  assert.equal(token.value, '')
  mounted()
  token.value = 'not-persisted'
  events.dispatchEvent(new Event('pagehide'))
  assert.equal(token.value, '')
  token.value = 'not-persisted'
  lockCallback(false)
  assert.equal(token.value, '')
  token.value = 'not-persisted'
  unmount()
  assert.equal(token.value, '')
  // The disposed callback no longer receives browser lifecycle events.
  token.value = 'disposal-fixture'
  events.dispatchEvent(new Event('pagehide'))
  assert.equal(token.value, 'disposal-fixture')
  assert.equal(useInferenceToken().value, '')
})

test('all live/public generators use the shared inference helpers', () => {
  const source = path => readFileSync(new URL(`../../web/${path}`, import.meta.url), 'utf8')
  for (const path of ['pages/playground.vue', 'components/setup/SetupWizard.vue']) {
    assert.ok(source(path).includes('useInferenceToken()'))
    assert.ok(source(path).includes('inferenceHeaders('))
    assert.ok(source(path).includes('<InferenceTokenField'))
    assert.ok(source(path).includes('response.status === 401'))
    assert.doesNotMatch(source(path), /inferenceHeaders\([^)]*,/)
    assert.doesNotMatch(source(path), /X-(?:Relay|Bouncer)-(?:Lane|Endpoint|Max-Wait-Ms|Allow-Fallback|Priority|Max-Cost-Micros)/i)
  }
  for (const path of ['pages/playground.vue', 'components/groups/GroupEditDrawer.vue', 'composables/useRelayUi.ts']) {
    assert.ok(source(path).includes('buildInferenceCurl('))
  }
  assert.match(source('components/InferenceTokenField.vue'), /type="password"/)
  assert.match(source('components/InferenceTokenField.vue'), /autocomplete="off"/)
  assert.ok(!source('nuxt.config.ts').includes('RELAY_API_TOKEN'))
  const setup = source('components/setup/SetupWizard.vue')
  assert.ok(setup.indexOf('<InferenceTokenField') > setup.indexOf('v-model="testRequest.prompt"'))
  const playground = source('pages/playground.vue')
  assert.match(playground, /buildInferenceCurl\(form\.body \|\| buildRequestBody\(\), curlURL\.value, apiToken\.value\)/)
  assert.match(playground, /clipboard\.writeText\(curlEquivalent\.value\)/)
  assert.match(playground, /highlightCurl\(curlEquivalent\.value\)/)
  assert.match(playground, /keep copied commands private/)
  assert.match(playground, /body: form\.body/)
  assert.match(playground, /model: selectedRequestModel\.value/)
  assert.match(setup, /model: createdLane\.value\.name/)
  assert.doesNotMatch(playground, /v-html|localStorage|sessionStorage/)
})
