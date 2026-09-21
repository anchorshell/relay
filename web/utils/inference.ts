import { DEFAULT_RELAY_ORIGIN } from './relayDefaults.ts'

export const INFERENCE_AUTH_HELP = 'Enter your RELAY_API_TOKEN in the Relay API Key field and try again. The management token does not authorize inference.'

// Examples point at the backend in standalone development. Preserve explicitly
// configured origins and deployed origins rather than forcing localhost there.
export function inferenceExampleURL(path: string, browserOrigin = '', backendOrigin = ''): string {
  if (/^https?:\/\//i.test(path)) return path
  let origin = browserOrigin || DEFAULT_RELAY_ORIGIN
  const browser = new URL(origin)
  if (['localhost', '127.0.0.1'].includes(browser.hostname) && browser.port === '3030') {
    origin = backendOrigin ? backendOrigin.replace(/^ws:/, 'http:').replace(/^wss:/, 'https:') : DEFAULT_RELAY_ORIGIN
  }
  return new URL(path, origin).toString()
}

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`
}

// Static examples keep the shell reference. Playground may explicitly pass its
// user-entered, memory-only token for a ready-to-copy command. Quote it literally
// so shell substitutions and apostrophes cannot change the copied command.
export function buildInferenceCurl(body: string, url = `${DEFAULT_RELAY_ORIGIN}/v1/chat/completions`, apiToken = ''): string {
  const token = apiToken.trim()
  const authorization = token
    ? shellQuote(`Authorization: Bearer ${token}`)
    : '"Authorization: Bearer ${RELAY_API_TOKEN}"'
  return [
    `curl -X POST ${shellQuote(url)}`,
    `  -H ${authorization}`,
    "  -H 'Content-Type: application/json'",
    `  --data-raw ${shellQuote(body)}`
  ].map((line, index, lines) => index < lines.length - 1 ? `${line} \\` : line).join('\n')
}

export function inferenceHeaders(apiToken: string): Record<string, string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const token = apiToken.trim()
  if (token) {
    if (/[\s\x00-\x1f\x7f]/.test(token)) throw new Error('Relay API Key must not contain whitespace or control characters.')
    headers.Authorization = `Bearer ${token}`
  }
  return headers
}
