import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'
import { DEFAULT_RELAY_ORIGIN, DEFAULT_RELAY_PORT } from '../../web/utils/relayDefaults.ts'
import { resolveRelayWebSocketURL } from '../../web/utils/relayURLs.ts'

const repoFile = path => new URL(`../../${path}`, import.meta.url)
const source = path => readFileSync(repoFile(path), 'utf8')

test('Go, the example environment, OpenAPI, and frontend share one default', () => {
  assert.equal(DEFAULT_RELAY_PORT, 11730)
  assert.equal(DEFAULT_RELAY_ORIGIN, 'http://localhost:11730')
  assert.match(source('internal/config/config.go'), /DefaultHTTPAddr\s+= ":11730"/)
  assert.match(source('.env.example'), /^RELAY_HTTP_ADDR=:11730$/m)
  assert.equal(JSON.parse(source('docs/openapi.json')).servers[0].url, `${DEFAULT_RELAY_ORIGIN}/v1`)
  assert.match(source('web/nuxt.config.ts'), /const devApiTarget = process\.env\.NUXT_DEV_API_TARGET \|\| `http:\/\/127\.0\.0\.1:\$\{DEFAULT_RELAY_PORT\}`/)
  assert.match(source('web/nuxt.config.ts'), /process\.env\.NODE_ENV === 'development' \? devApiTarget : ''/)
})

test('standalone telemetry and preview use the backend port, preserving loopback hosts and overrides', () => {
  for (const path of ['/api/ws', '/api/preview/live-flow/sessions/example/ws']) {
    for (const hostname of ['localhost', '127.0.0.1']) {
      assert.equal(resolveRelayWebSocketURL(path, DEFAULT_RELAY_ORIGIN, `http://${hostname}:3030`), `ws://${hostname}:11730${path}`)
      assert.equal(resolveRelayWebSocketURL(path, 'http://127.0.0.1:9000', `http://${hostname}:3030`), `ws://${hostname}:9000${path}`)
    }
  }
})

test('make dev derives backend, HTTP proxy, and realtime targets together without changing the frontend port', t => {
  // Dry-run in an isolated directory: never load the developer's .env or
  // start Air/Nuxt. Exercise GNU Make's actual include/override semantics.
  const cwd = mkdtempSync(join(tmpdir(), 'relay-port-test-'))
  t.after(() => rmSync(cwd, { recursive: true, force: true }))
  const makefile = fileURLToPath(repoFile('Makefile'))
  const dryRun = (...args) => execFileSync('make', ['--no-print-directory', '-n', '-f', makefile, 'dev-go', 'dev-frontend', 'AIR=air', ...args], {
    cwd,
    env: { PATH: process.env.PATH },
    encoding: 'utf8'
  })
  const check = (output, port) => {
    assert.ok(output.includes(`RELAY_HTTP_ADDR=":${port}"`))
    assert.ok(output.includes(`NUXT_DEV_API_TARGET="http://127.0.0.1:${port}"`))
    assert.ok(output.includes(`NUXT_PUBLIC_RELAY_WS_TARGET="http://127.0.0.1:${port}"`))
    assert.ok(output.includes('NUXT_DEV_PORT="3030"'))
    assert.ok(output.includes('--port 3030'))
  }
  check(dryRun(), DEFAULT_RELAY_PORT)
  writeFileSync(join(cwd, '.env'), 'RELAY_HTTP_ADDR=:9001\n')
  check(dryRun(), 9001)
  check(dryRun('RELAY_HTTP_ADDR=:9000'), 9000)
  check(dryRun('DEV_BACKEND_ADDR=:9002'), 9002)
})
