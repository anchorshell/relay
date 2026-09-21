import assert from 'node:assert/strict'
import test from 'node:test'
import { buildInferenceCurl } from '../../web/utils/inference.ts'
import { highlightCurl } from '../../web/utils/curlHighlight.ts'

const reconstruct = lines => lines.map(line => line.map(token => token.text).join('')).join('\n')

test('curl highlighting distinguishes flags from quoted headers and multiline JSON values', () => {
  const body = JSON.stringify({
    model: 'queue-first',
    messages: [{ role: 'user', content: 'Explain queue-first routing and Content-Type. curl POST --data-raw ${RELAY_API_TOKEN} "fake-key": true' }]
  }, null, 2)
  const curl = buildInferenceCurl(body)
  const lines = highlightCurl(curl)
  assert.equal(reconstruct(lines), curl)
  assert.deepEqual(lines.flat().filter(token => token.tone === 'flag').map(token => token.text), ['-X', '-H', '-H', '--data-raw'])
  assert.deepEqual(lines.flat().filter(token => token.tone === 'key').map(token => token.text), ['"model"', '"messages"', '"role"', '"content"'])
  assert.deepEqual(lines.flat().filter(token => token.tone === 'variable').map(token => token.text), ['${RELAY_API_TOKEN}'])
  assert.ok(lines.flat().some(token => token.tone === 'string' && token.text.includes('queue-first routing')))
})

test('escaped apostrophes and literal tokens keep quote context and exact text', () => {
  const token = 'fixture-\'${RELAY_API_TOKEN}-"-Type-$(not-executed)'
  const body = JSON.stringify({ "user's-key": 'It\'s queue-first, with "quotes", \\slashes and <script>not HTML</script>.' }, null, 2)
  const curl = buildInferenceCurl(body, 'http://localhost:11730/v1/chat/completions', token)
  const lines = highlightCurl(curl)
  assert.equal(reconstruct(lines), curl)
  assert.equal(lines.flat().filter(token => token.tone === 'variable').length, 0)
  assert.deepEqual(lines.flat().filter(token => token.tone === 'flag').map(token => token.text), ['-X', '-H', '-H', '--data-raw'])
  assert.equal(lines.flat().filter(token => token.tone === 'key').length, 1)
})

test('highlighting stays lossless for blank lines and incomplete JSON while editing', () => {
  for (const body of ['', '\n\n', '{\n "content": "unfinished -first', "'", 'trailing\n']) {
    const curl = buildInferenceCurl(body)
    assert.equal(reconstruct(highlightCurl(curl)), curl)
  }
  assert.deepEqual(highlightCurl(''), [[]])
})
