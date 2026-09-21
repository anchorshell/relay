// Run from the repository root after `cd web && npm run generate`:
// node --experimental-strip-types tests/frontend/upgrade.browser.mjs
// Fixtures only: never connects to Relay, a database, or a model provider.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFileSync, mkdirSync, statSync } from 'node:fs'
import { dirname, extname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { paidFeatures, DEFAULT_RELAY_UPGRADE_URL } from '../../web/utils/paidFeatures.ts'
import { startBrowser } from './support/browser.mjs'

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const publicRoot = resolve(repo, 'web/.output/public')
const outputDir = resolve(repo, 'tmp/upgrade-browser')
mkdirSync(outputDir, { recursive: true })
const requests = []
const catalog = {
  providers: [{ id: 'provider', name: 'Example provider', slug: 'example', enabled: true }],
  endpoints: [{ id: 'model', provider_id: 'provider', name: 'Example model', upstream_model: 'example', route_kind: 'chat', enabled: true, health_status: 'healthy', metadata_json: '{}' }],
  lanes: [{ id: 'group', name: 'support', enabled: true, default_max_wait_ms: 60000 }],
  memberships: [{ id: 'membership', lane_id: 'group', endpoint_id: 'model', rank: 1, enabled: true }],
  credentials: [], limit_policies: [], observed_limits: [], pricing_policies: [], guardrails: [], guardrail_bindings: []
}
const log = {
  id: 'request', request_id: 'request', incoming_model: 'support', selected_upstream_model: 'example',
  route_kind: 'chat', task_state: 'completed', status_code: 200, provider_id: 'provider', endpoint_id: 'model',
  created_at: new Date().toISOString(), updated_at: new Date().toISOString(), finished_at: new Date().toISOString(),
  wait_ms: 10, latency_ms: 100, streaming: false, fallback_count: 0,
  actual_input_tokens: 30, actual_output_tokens: 20, actual_total_tokens: 50, actual_cost_micros: 100,
  characterization_json: JSON.stringify({ primary_action: 'answer', confidence: 0.92, classifier_status: 'complete' }),
  primary_action: 'answer', action_confidence: 0.92
}
const server = createServer((req, res) => {
  const path = new URL(req.url, 'http://127.0.0.1').pathname
  if (path.startsWith('/api/') || path.startsWith('/v1/')) {
    requests.push({ path, method: req.method })
    req.resume()
    let payload = []
    if (path === '/api/session') payload = { authenticated: true, system: {} }
    else if (path.startsWith('/api/page-data/')) payload = { catalog, settings: [], queue_items: [], system: {}, metrics: {} }
    else if (path === '/api/logs/requests') payload = { items: [log], total: 1, limit: 50, offset: 0, has_more: false }
    else if (path === '/api/logs/requests/request') payload = log
    else if (path === '/api/stats/usage-analytics') payload = { buckets: [], series: [], totals: {}, providers: [], models: [] }
    else if (path === '/api/stats/characterization-intents') payload = { items: [], totals: {}, buckets: [] }
    else if (path === '/api/routing-lanes' && req.method === 'POST') {
      payload = { id: 'new-group', name: 'Browser-created group', enabled: true }
      catalog.lanes.push(payload)
    }
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify(payload))
    return
  }
  let file = resolve(publicRoot, `.${decodeURIComponent(path)}`)
  if (!file.startsWith(`${publicRoot}/`)) file = resolve(publicRoot, 'index.html')
  try { if (!statSync(file).isFile()) file = resolve(publicRoot, 'index.html') } catch { file = resolve(publicRoot, 'index.html') }
  const mime = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' }[extname(file)] || 'application/octet-stream'
  res.writeHead(200, { 'Content-Type': mime }).end(readFileSync(file))
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = `http://127.0.0.1:${server.address().port}`
let browser
try {
  browser = await startBrowser(outputDir)
  const { evaluate, waitFor, call, key } = browser
  const navigate = async (path, theme = 'dark') => {
    await call('Page.navigate', { url: `${origin}${path}` })
    await waitFor(`Boolean(document.querySelector('.app-shell'))`, path)
    await evaluate(`document.querySelector('[aria-label="Switch to ${theme} mode"]')?.click()`)
    await waitFor(`document.documentElement.dataset.siteTheme === ${JSON.stringify(theme)}`)
  }
  const openFeature = async (id, selector = `[data-upgrade-feature="${id}"]`) => {
    await waitFor(`Boolean(document.querySelector(${JSON.stringify(selector)}))`)
    await evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); el.scrollIntoView({ block: 'center' }); el.focus(); })()`)
    assert.equal(await evaluate(`document.activeElement?.dataset.upgradeFeature`), id, 'trigger did not receive focus')
    // Keyboard activation verifies this is a real, enabled action.
    await key('Enter')
    await waitFor(`Boolean(document.querySelector('[role="dialog"]'))`, `${id} dialog; active=${await evaluate('document.activeElement?.outerHTML.slice(0, 300)')}`)
    const data = await evaluate(`(() => {
      const dialog = document.querySelector('[role="dialog"]');
      const cta = dialog.querySelector('a');
      const rect = dialog.getBoundingClientRect();
      return { text: dialog.textContent, title: document.getElementById(dialog.getAttribute('aria-labelledby'))?.textContent,
        description: document.getElementById(dialog.getAttribute('aria-describedby'))?.textContent,
        href: cta.href, target: cta.target, rel: cta.rel,
        fit: rect.x >= 0 && rect.right <= innerWidth && rect.y >= 0 && rect.bottom <= innerHeight,
        overflow: dialog.scrollWidth > dialog.clientWidth,
        logo: Boolean(dialog.querySelector('img')?.naturalWidth),
        closeIcon: Boolean(dialog.querySelector('.upgrade-close svg path')),
        theme: document.documentElement.dataset.siteTheme };
    })()`)
    assert.equal(data.title.trim(), paidFeatures[id].title)
    assert.equal(data.description.trim(), paidFeatures[id].description)
    for (const field of ['headline', 'description', 'availability', 'ossNote', 'ctaLabel']) assert.ok(data.text.includes(paidFeatures[id][field]), `${id}: ${field}`)
    assert.equal(data.href, DEFAULT_RELAY_UPGRADE_URL)
    assert.equal(data.target, '_blank')
    assert.ok(data.rel.includes('noopener') && data.rel.includes('noreferrer'))
    assert.equal(data.fit, true, `${id}: dialog outside viewport`)
    assert.equal(data.overflow, false, `${id}: horizontal dialog overflow`)
    assert.equal(data.logo, true, `${id}: logo did not load`)
    assert.equal(data.closeIcon, true, 'close control must work without remote icons')
    for (let index = 0; index < 5; index++) {
      await key('Tab')
      assert.ok(await evaluate(`document.querySelector('[role="dialog"]').contains(document.activeElement)`), 'focus escaped dialog')
    }
    await key('Tab', 8)
    assert.ok(await evaluate(`document.querySelector('[role="dialog"]').contains(document.activeElement)`))
  }
  const closeFeature = async id => {
    await key('Escape')
    await waitFor(`!document.querySelector('[role="dialog"]')`)
    assert.equal(await evaluate(`document.activeElement?.getAttribute('data-upgrade-feature')`), id, 'focus did not return to trigger')
  }

  await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false })
  await navigate('/groups')
  await waitFor(`Boolean(document.querySelector('[data-upgrade-feature="smartGroups"]'))`)
  assert.equal(await evaluate(`document.querySelectorAll('.upgrade-badge').length`), 0)
  assert.equal(await evaluate(`(() => {
    const smart = document.querySelector('[data-upgrade-feature="smartGroups"]');
    const ordinary = Array.from(document.querySelectorAll('button')).find(el => el.textContent.trim() === 'Add Group');
    const properties = ['backgroundColor', 'color', 'borderRadius', 'fontSize', 'paddingTop', 'paddingLeft'];
    return properties.every(property => getComputedStyle(smart)[property] === getComputedStyle(ordinary)[property]);
  })()`), true, 'Smart Group must use the ordinary primary action styling')
  await browser.screenshot('groups-desktop')
  await openFeature('teams')
  await closeFeature('teams')
  await openFeature('apiKeys')
  await closeFeature('apiKeys')
  await openFeature('smartGroups')
  await browser.screenshot('modal-desktop')
  // Explicit dismiss works as well as Escape.
  await evaluate(`Array.from(document.querySelectorAll('[role="dialog"] button')).find(el => el.textContent.trim() === 'Maybe later').click()`)
  await waitFor(`!document.querySelector('[role="dialog"]')`)
  assert.equal(await evaluate(`document.activeElement?.dataset.upgradeFeature`), 'smartGroups')
  await evaluate(`Array.from(document.querySelectorAll('button')).find(el => el.textContent.trim() === 'Add Group').click()`)
  await waitFor(`Boolean(document.querySelector('input[placeholder="Customer support"]'))`)
  await evaluate(`(() => { const input = document.querySelector('input[placeholder="Customer support"]'); input.value = 'Browser-created group'; input.dispatchEvent(new Event('input', { bubbles: true })); Array.from(document.querySelectorAll('button')).find(el => el.textContent.trim() === 'Create Group').click(); })()`)
  await waitFor(`document.body.textContent.includes('Browser-created group')`)
  assert.ok(requests.some(req => req.path === '/api/routing-lanes' && req.method === 'POST'), 'ordinary create group stopped working')

  for (const [path, feature, name] of [
    ['/providers', 'connectedProviders', 'providers-desktop'],
    ['/limits', 'budgets', 'limits-desktop'],
    ['/usage', 'usageAttribution', 'usage-desktop'],
    ['/settings', 'permissions', 'settings-desktop']
  ]) {
    await navigate(path)
    await waitFor(`Boolean(document.querySelector('[data-upgrade-feature="${feature}"]'))`)
    await evaluate(`document.querySelector('[data-upgrade-feature="${feature}"]').scrollIntoView({ block: 'center' })`)
    await browser.screenshot(name)
    await openFeature(feature)
    await closeFeature(feature)
  }
  await openFeature('apiKeys', 'main [data-upgrade-feature="apiKeys"]')
  await closeFeature('apiKeys')
  await navigate('/requests')
  await waitFor(`Array.from(document.querySelectorAll('button')).some(el => el.textContent.trim() === 'Inspect')`)
  await evaluate(`Array.from(document.querySelectorAll('button')).find(el => el.textContent.trim() === 'Inspect').click()`)
  await openFeature('classificationFeedback')
  await closeFeature('classificationFeedback')

  await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: false })
  await navigate('/groups', 'light')
  await openFeature('smartGroups')
  await browser.screenshot('modal-mobile')
  await closeFeature('smartGroups')
  await evaluate(`document.querySelector('[aria-label="Open navigation menu"]').click()`)
  await waitFor(`document.querySelector('aside').getBoundingClientRect().x >= -1`)
  await evaluate(`document.querySelector('[data-upgrade-feature="teams"]').scrollIntoView({ block: 'center' })`)
  await browser.screenshot('navigation-mobile')
  await openFeature('teams')
  await closeFeature('teams')
  assert.equal(await evaluate(`document.documentElement.scrollWidth > innerWidth`), false)
  assert.equal(requests.filter(req => /\/api\/(pro|team|keys|permissions)/.test(req.path)).length, 0, 'promotional action called a paid API')
  assert.deepEqual(requests.filter(req => !['GET', 'HEAD'].includes(req.method)).map(req => req.path), ['/api/routing-lanes'])
  console.log('PASS: eight feature dialogs, correct copy/CTA, dismiss/Escape/focus trap/return, mobile navigation, ordinary group creation, and no paid API calls.')
  console.log(`Screenshots: ${outputDir}`)
} finally {
  await browser?.stop()
  server.closeAllConnections()
  await new Promise(resolve => server.close(resolve))
}
