import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire, stripTypeScriptTypes } from 'node:module'
import test from 'node:test'

const web = new URL('../../web/', import.meta.url)
const require = createRequire(new URL('package.json', web))
const vue = require('vue')
const { parse, compileScript } = require('@vue/compiler-sfc')
const { renderToString } = require('@vue/server-renderer')

test('unfinished classification displays Pending instead of Unknown or preliminary labels', async () => {
  const source = readFileSync(new URL('components/requests/CharacterizationPills.vue', web), 'utf8')
  const { descriptor } = parse(source)
  const compiled = stripTypeScriptTypes(compileScript(descriptor, { id: 'classification-test', inlineTemplate: true, genDefaultAs: 'component' }).content)
    .replace(/import \{([^}]+)\} from ["']vue["']/g, (_match, names) => `const {${names.replace(/\bas\b/g, ':')}} = vue`)
  const Component = new Function('vue', 'computed', `${compiled}; return component`)(vue, vue.computed)
  for (const status of ['pending', 'complete']) {
    const app = vue.createSSRApp({ render: () => vue.h(Component, {
      primaryAction: 'summarize', characterization: { classifier_status: status, domains: ['general'] }
    }) })
    app.component('UiBadge', { render() { return vue.h('span', this.$slots.default?.()) } })
    const html = await renderToString(app)
    if (status === 'pending') {
      assert.match(html, /Pending/)
      assert.doesNotMatch(html, /Unknown|Summarize|General/)
    } else {
      assert.match(html, /Summarize/)
      assert.match(html, /General/)
    }
  }
})
