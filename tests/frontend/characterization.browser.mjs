// Fixture-only browser check; no Relay, database, worker or provider connection.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFileSync, mkdirSync, statSync } from 'node:fs'
import { resolve, extname } from 'node:path'
import { startBrowser } from './support/browser.mjs'

const root = resolve('web/.output/public')
const output = resolve('tmp/characterization-browser')
mkdirSync(output, { recursive: true })
let engine = 'anchorshell'
const writes = []
const settings = () => [{ key: 'characterization_engine', value_json: JSON.stringify(engine) }]
const server = createServer(async (req, res) => {
  const path = new URL(req.url, 'http://127.0.0.1').pathname
  if (path.startsWith('/api/')) {
    let payload = []
    if (path === '/api/session') payload = { authenticated: true, system: {} }
    if (path.startsWith('/api/page-data/')) payload = { catalog: {}, settings: settings(), system: {}, metrics: {} }
    if (path === '/api/system') payload = { laya: { configured: false, ready: false } }
    if (path === '/api/settings') {
      if (req.method === 'PUT') {
        let body = ''; for await (const part of req) body += part
        const values = JSON.parse(body); writes.push(values)
        if (values.characterization_engine) engine = values.characterization_engine
      }
      payload = settings()
    }
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify(payload)); return
  }
  let file = resolve(root, `.${path}`)
  if (!file.startsWith(root + '/')) file = resolve(root, 'index.html')
  try { if (!statSync(file).isFile()) file = resolve(root, 'index.html') } catch { file = resolve(root, 'index.html') }
  const type = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml', '.woff2': 'font/woff2', '.png': 'image/png' }[extname(file)] || 'application/octet-stream'
  res.writeHead(200, { 'Content-Type': type }).end(readFileSync(file))
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
let browser
try {
  browser = await startBrowser(output)
  const { call, evaluate, waitFor } = browser
  await call('Page.navigate', { url: `http://127.0.0.1:${server.address().port}/settings` })
  await waitFor('Boolean(document.querySelector("input[value=laya]"))')
  assert.equal(await evaluate('document.querySelector("input[value=anchorshell]").checked'), true)
  await evaluate('document.querySelector("input[value=laya]").click()')
  await waitFor('Boolean(document.querySelector("fieldset [role=status]"))')
  await evaluate('Array.from(document.querySelectorAll("button")).find(b => b.textContent.trim() === "Save").click()')
  await waitFor('Array.from(document.querySelectorAll("button")).some(b => b.textContent.trim() === "Save" && b.disabled)')
  assert.equal(engine, 'laya')
  assert.deepEqual(writes, [{ characterization_engine: 'laya' }])
  for (const width of [1280, 390]) {
    await call('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 500 })
    for (const theme of ['light', 'dark']) {
      await evaluate(`document.querySelector('[aria-label="Switch to ${theme} mode"]')?.click()`)
      await evaluate('document.querySelector("input[value=laya]").scrollIntoView({block:"center"})')
      assert.equal(await evaluate('document.documentElement.scrollWidth <= innerWidth'), true)
      await browser.screenshot(`settings-${width}-${theme}`)
    }
  }
  await call('Page.reload')
  await waitFor('Boolean(document.querySelector("input[value=laya]")?.checked)')
  console.log('PASS: default, selection, warning, save, reload, and responsive theme screenshots')
} finally {
  await browser?.stop()
  await new Promise(resolve => server.close(resolve))
}
