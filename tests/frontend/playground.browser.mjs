// Optional fixture-only check against an already-running frontend. No backend
// calls, real keys, OS clipboard writes, or dev-server lifecycle changes.
// node --experimental-strip-types tests/frontend/playground.browser.mjs [URL ...]
import assert from 'node:assert/strict'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { startBrowser } from './support/browser.mjs'

const urls = process.argv.slice(2)
if (!urls.length) urls.push('http://127.0.0.1:3030/playground')
for (const url of urls) assert.ok(['localhost', '127.0.0.1', '[::1]'].includes(new URL(url).hostname), 'Use a local frontend only')
const output = mkdtempSync(join(tmpdir(), 'relay-playground-'))
const browser = await startBrowser(output)
try {
  const { call, evaluate, waitFor } = browser
  await call('Page.addScriptToEvaluateOnNewDocument', { source: `
    const originalFetch = window.fetch.bind(window);
    window.__inferenceCalls = 0;
    window.fetch = (input, init) => {
      const path = new URL(typeof input === 'string' ? input : input.url, location.origin).pathname
        .replace('/relay/api/', '/api/').replace('/api/relay/', '/api/');
      if (path.startsWith('/v1/')) {
        window.__inferenceCalls++;
        return Promise.reject(new Error('Inference is disabled in this fixture test'));
      }
      if (path.startsWith('/api/')) {
        let data = [];
        if (path === '/api/session') data = { authenticated: true, system: {} };
        if (path === '/api/auth/me') data = {
          user: { id: 'fixture-user', name: 'Fixture User', email: 'fixture@example.test' },
          org: { id: 'fixture-org', name: 'Fixture Team' }, role: 'owner',
          model_relay_permissions: ['relay:request'], platform_permissions: []
        };
        if (path.startsWith('/api/page-data/')) data = {
          catalog: { providers: [], endpoints: [], lanes: [], memberships: [], credentials: [], limit_policies: [], observed_limits: [], pricing_policies: [], guardrails: [], guardrail_bindings: [] },
          settings: [], system: {}, queue_items: [], metrics: {}
        };
        return Promise.resolve(new Response(JSON.stringify(data), { headers: { 'Content-Type': 'application/json' } }));
      }
      return originalFetch(input, init);
    };
    const NativeWebSocket = window.WebSocket;
    window.WebSocket = class extends NativeWebSocket {
      constructor(url, protocols) {
        if (!new URL(url, location.href).pathname.includes('/api/')) { super(url, protocols); return; }
        return Object.assign(new EventTarget(), { readyState: 3, send() {}, close() {} });
      }
    };
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: async text => { window.__copiedCurl = text; }
    } });
  ` })
  for (const url of urls) {
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false })
    await call('Page.navigate', { url })
    await waitFor(`Boolean(document.querySelector('.inference-token-input'))`)
    await waitFor(`Boolean(document.querySelector('.playground-curl-preview'))`)
    const preview = `Array.from(document.querySelectorAll('.playground-curl-preview code > span')).map(line => line.textContent).join('\\n')`
    assert.ok((await evaluate(preview)).includes('${RELAY_API_TOKEN}'))
    const setToken = async value => {
      await evaluate(`(() => {
        const input = document.querySelector('.inference-token-input');
        input.value = ${JSON.stringify(value)};
        input.dispatchEvent(new Event('input', { bubbles: true }));
      })()`)
    }
    await setToken('fixture-relay-api-token')
    await waitFor(`(${preview}).includes('Authorization: Bearer fixture-relay-api-token')`)
    assert.ok(!(await evaluate(preview)).includes('${RELAY_API_TOKEN}'))
    await evaluate(`Array.from(document.querySelectorAll('button')).find(button => button.textContent.trim() === 'Copy').click()`)
    await waitFor(`typeof window.__copiedCurl === 'string'`)
    assert.equal(await evaluate('window.__copiedCurl'), await evaluate(preview))
    assert.deepEqual(await evaluate(`Array.from(document.querySelectorAll('.playground-curl-token--flag')).map(span => span.textContent)`), ['-X', '-H', '-H', '--data-raw'])
    assert.ok(await evaluate(`Array.from(document.querySelectorAll('.playground-curl-token--string')).some(span => span.textContent.includes('queue-first'))`))
    await browser.screenshot(`playground-${new URL(url).port}-desktop`)
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: false })
    // Let the existing responsive sidebar transition settle before capture.
    await delay(400)
    await evaluate(`document.querySelector('.playground-curl-preview').scrollIntoView({ block: 'center' })`)
    await browser.screenshot(`playground-${new URL(url).port}-mobile`)
    await setToken('replacement-fixture')
    await waitFor(`(${preview}).includes('Bearer replacement-fixture')`)
    assert.ok(!(await evaluate(preview)).includes('fixture-relay-api-token'))
    await evaluate(`window.dispatchEvent(new Event('pagehide'))`)
    await waitFor(`document.querySelector('.inference-token-input').value === ''`)
    assert.ok((await evaluate(preview)).includes('${RELAY_API_TOKEN}'))
    assert.equal(await evaluate('window.__inferenceCalls'), 0)
    console.log(`PASS ${url}: reactive key, Copy output, quote-aware highlighting, replacement and lifecycle clearing`)
  }
  console.log(`Screenshots: ${output}`)
} finally {
  await browser.stop()
}
