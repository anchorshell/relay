import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire, stripTypeScriptTypes } from 'node:module'
import test from 'node:test'

const web = new URL('../../web/', import.meta.url)
const require = createRequire(new URL('package.json', web))
const vue = require('vue')
const { parse, compileScript } = require('@vue/compiler-sfc')
const { renderToString } = require('@vue/server-renderer')
function compile(name, discovery = null) {
  const { descriptor } = parse(readFileSync(new URL(`components/${name}.vue`, web), 'utf8'))
  const compiled = stripTypeScriptTypes(compileScript(descriptor, { id: name, inlineTemplate: true, genDefaultAs: 'component' }).content)
    .replace(/import \{([^}]+)\} from ["']vue["']/g, (_match, names) => `const {${names.replace(/\bas\b/g, ':')}} = vue`)
  return new Function('vue', 'useId', 'useUpgradeDiscovery', `${compiled}; return component`)(vue, vue.useId, () => discovery)
}
test('Community lists four choices and preserves the selected real engine', async () => {
  const Base = compile('CharacterizationEngineSelector')
  const Community = compile('CommunityCharacterizationEngineSelector', { open() {} })
  for (const modelValue of ['anchorshell', 'laya']) {
    const app = vue.createSSRApp({ render: () => vue.h(Community, { modelValue }) })
    app.component('CharacterizationEngineSelector', Base)
    const html = await renderToString(app)
    const labels = ['AnchorShell Classifier Pro + Enhanced (proprietary)', 'AnchorShell Classifier Basic', 'Laya (system one model - open source)', 'Jev (system one model - typesafe.ai)']
    let previous = -1
    for (const label of labels) {
      const index = html.indexOf(label)
      assert.ok(index > previous, label)
      previous = index
    }
    assert.match(html, new RegExp(`value="${modelValue}" checked`))
    assert.match(html, /type="button"[^>]*aria-haspopup="dialog"/)
    assert.match(html, /type="radio" disabled/)
    assert.match(html, /make laya-start/)
    assert.match(html, /href="https:\/\/github.com\/anchorshell\/relay"/)
  }
})
test('inherited selector defaults stay free of Community promotion and coming-soon choices', async () => {
  const Base = compile('CharacterizationEngineSelector')
  const html = await renderToString(vue.createSSRApp({ render: () => vue.h(Base, { modelValue: 'laya' }) }))
  assert.doesNotMatch(html, /Pro \+ Enhanced|Basic|Jev|Coming soon|dialog/)
  assert.match(html, /value="laya" checked/)
})
