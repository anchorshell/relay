// Optional local browser checks using Chrome's built-in DevTools protocol.
// No browser download, framework dependency, or existing browser profile.
import { spawn } from 'node:child_process'
import { mkdtempSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

export async function startBrowser(outputDir) {
  const profile = mkdtempSync(resolve(outputDir, 'chrome-'))
  const binary = process.env.CHROME_BIN || (process.platform === 'darwin'
    ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : 'chromium')
  const chrome = spawn(binary, ['--headless=new', '--disable-gpu', '--disable-background-networking', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: ['ignore', 'ignore', 'pipe'] })
  let socket
  const stop = async () => {
    socket?.close()
    if (!chrome.pid || chrome.exitCode !== null || chrome.signalCode !== null) return
    const exited = new Promise(resolve => chrome.once('exit', resolve))
    chrome.kill('SIGTERM')
    const timer = setTimeout(() => chrome.kill('SIGKILL'), 5000)
    await exited
    clearTimeout(timer)
  }
  try {
    const endpoint = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Chrome startup timed out')), 10000)
      chrome.once('error', error => { clearTimeout(timer); reject(error) })
      chrome.stderr.on('data', chunk => {
        const match = String(chunk).match(/DevTools listening on (ws:\/\/\S+)/)
        if (match) { clearTimeout(timer); resolve(match[1]) }
      })
    })
    socket = new WebSocket(endpoint)
    await new Promise((resolve, reject) => {
      socket.addEventListener('open', resolve, { once: true })
      socket.addEventListener('error', reject, { once: true })
    })
    const pending = new Map()
    const runtimeErrors = []
    let nextID = 0
    socket.addEventListener('message', event => {
      const message = JSON.parse(event.data)
      if (message.method === 'Runtime.exceptionThrown') runtimeErrors.push(message.params.exceptionDetails.exception?.description || message.params.exceptionDetails.text)
      if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') runtimeErrors.push(message.params.args.map(arg => arg.description || arg.value).join(' '))
      if (!pending.has(message.id)) return
      const { resolve, reject, timer } = pending.get(message.id)
      clearTimeout(timer)
      pending.delete(message.id)
      message.error ? reject(new Error(message.error.message)) : resolve(message.result)
    })
    const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => {
      const id = ++nextID
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)) }, 10000)
      pending.set(id, { resolve, reject, timer })
      socket.send(JSON.stringify({ id, method, params, sessionId }))
    })
    const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
    const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
    const call = (method, params) => send(method, params, sessionId)
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text)
      return result.result.value
    }
    const waitFor = async (expression, label = expression) => {
      for (let attempt = 0; attempt < 100; attempt++) {
        if (await evaluate(expression)) return
        await delay(100)
      }
      throw new Error(`Timed out: ${label}\n${runtimeErrors.join('\n')}`)
    }
    await call('Page.enable')
    await call('Runtime.enable')
    await call('Network.enable')
    await call('Network.setBlockedURLs', { urls: ['https://*'] })
    return {
      call, evaluate, waitFor, stop,
      async screenshot(name, fullPage = false) {
        const image = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: fullPage })
        writeFileSync(resolve(outputDir, `${name}.png`), Buffer.from(image.data, 'base64'))
      },
      async key(key, modifiers = 0) {
        const event = { key, code: key, modifiers, windowsVirtualKeyCode: key === 'Tab' ? 9 : key === 'Escape' ? 27 : 13 }
        await call('Input.dispatchKeyEvent', { type: 'keyDown', ...event, ...(key === 'Enter' ? { text: '\r' } : {}) })
        await call('Input.dispatchKeyEvent', { type: 'keyUp', ...event })
      }
    }
  } catch (error) {
    await stop()
    throw error
  }
}
