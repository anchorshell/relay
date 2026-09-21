import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { createRequire, stripTypeScriptTypes } from 'node:module'
import test from 'node:test'

const web = new URL('../../web/', import.meta.url)
const source = path => readFileSync(new URL(path, web), 'utf8')
const require = createRequire(new URL('package.json', web))
const vue = require('vue')
const { parse, compileScript } = require('@vue/compiler-sfc')
const { renderToString } = require('@vue/server-renderer')

test('shared Nuxt UI cursor variants cover action slots and preserve disabled states', () => {
  const config = new Function('defineAppConfig', source('app.config.ts').replace('export default', 'return'))(value => value)
  const controls = {
    button: ['base'], dropdownMenu: ['item'], selectMenu: ['base', 'item'],
    tabs: ['trigger'], switch: ['base', 'label'], calendar: ['cellTrigger'], stepper: ['trigger']
  }
  for (const [component, slots] of Object.entries(controls)) {
    for (const slot of slots) {
      const classes = config.ui[component].slots[slot].split(' ')
      assert.ok(classes.includes('cursor-pointer'), `${component}.${slot}`)
      // Switch labels use Nuxt UI's existing disabled variant, not attributes
      // that are only present on its sibling switch button.
      if (slot !== 'label') assert.ok(classes.some(value => value.endsWith(':cursor-not-allowed')), `${component}.${slot} disabled`)
      assert.ok(classes.every(value => /^(?:[\w-]+:)?cursor-(pointer|not-allowed)$/.test(value)), 'cursor-only configuration')
    }
  }
  assert.equal(config.ui.input, undefined)
  assert.equal(config.ui.textarea, undefined)
  assert.equal(config.ui.card, undefined)
  assert.equal(config.ui.tooltip, undefined)
})

test('real shared buttons and switches keep disabled semantics alongside cursor styling', async () => {
  for (const file of ['components/ui/UiButton.vue', 'components/ui/UiSwitch.vue']) {
    const { descriptor } = parse(source(file))
    const compiled = stripTypeScriptTypes(compileScript(descriptor, { id: 'cursor-test', inlineTemplate: true, genDefaultAs: 'component' }).content)
      .replace(/import \{([^}]+)\} from ["']vue["']/g, (_match, names) => `const {${names.replace(/\bas\b/g, ':')}} = vue`)
    const Component = new Function('vue', `${compiled}; return component`)(vue)
    for (const disabled of [false, true]) {
      const app = vue.createSSRApp({ render: () => vue.h(Component, { disabled, modelValue: false, label: 'Example control' }) })
      app.component('UIcon', { render: () => null })
      const html = await renderToString(app)
      assert.match(html, /cursor-pointer/)
      assert.equal(/<button[^>]* disabled(?:[ >])/.test(html), disabled)
      if (file.endsWith('UiButton.vue')) assert.match(html, /disabled:cursor-not-allowed/)
      else {
        assert.match(html, /role="switch"/)
        assert.match(source(file), /\.ui-switch:disabled\s*\{\s*cursor: not-allowed;/)
      }
    }
    if (file.endsWith('UiButton.vue')) {
      const app = vue.createSSRApp({ render: () => vue.h(Component, { loading: true }) })
      app.component('UIcon', { render: () => null })
      const html = await renderToString(app)
      assert.match(html, /<button[^>]* disabled[ >]/)
      assert.match(html, /aria-busy="true"/)
    }
  }
})

test('native OSS action controls have an explicit or shared cursor owner', () => {
  const sharedClasses = /\b(app-sidebar-link|app-mobile-menu-button|app-sidebar-close|app-sidebar-backdrop|site-theme-toggle|ui-action-menu-button)\b/
  let inspected = 0
  function* files(directory) {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (entry.name.startsWith('.') || entry.name === 'node_modules') continue
      const url = new URL(entry.name, directory)
      if (entry.isDirectory()) yield* files(new URL(`${entry.name}/`, directory))
      else if (entry.name.endsWith('.vue')) yield url
    }
  }
  for (const url of files(web)) {
    const file = url.pathname
    const { descriptor } = parse(readFileSync(url, 'utf8'), { filename: file })
    const walk = node => {
      if (node.type === 1 && node.tag === 'button') {
        inspected++
        const classes = node.props.filter(prop => prop.name === 'class' || prop.arg?.content === 'class')
          .map(prop => prop.loc.source).join(' ')
        const scopedRecentLink = file.endsWith('/BaseRealtimePage.vue') && classes.includes('live-flow-recent-link')
        assert.ok(classes.includes('cursor-pointer') || sharedClasses.test(classes) || scopedRecentLink, `${file}:${node.loc.start.line}`)
      }
      for (const child of node.children || []) walk(child)
    }
    if (descriptor.template?.ast) walk(descriptor.template.ast)
  }
  assert.ok(inspected >= 30)
})

test('shared opt-in CSS does not make informational surfaces or text fields clickable', () => {
  const css = source('assets/css/interactions.css').replace(/\/\*[\s\S]*?\*\//g, '')
  const selectors = [...css.matchAll(/([^{}]+)\{([^}]+)\}/g)]
  assert.equal(selectors.length, 2)
  for (const [, selector, body] of selectors) {
    assert.match(selector, /^\s*:is\(\s*\.app-select,/)
    assert.doesNotMatch(selector, /(?:^|[\s,(])(?:button|input|textarea|\[role|\*)\s*[,\{]/)
    assert.doesNotMatch(selector, /app-card|app-input|tooltip/)
    assert.match(body.trim(), /^@apply cursor-(pointer|not-allowed);$/)
  }
  assert.match(css, /:not\(:disabled\):not\(\[aria-disabled='true'\]\):not\(\[data-disabled\]\)/)
  assert.match(css, /:is\(:disabled, \[aria-disabled='true'\], \[data-disabled\]\)/)
  assert.match(source('assets/css/tailwind.css'), /@import "\.\/interactions.css"/)
  const groups = source('components/groups/GroupEditDrawer.vue')
  assert.match(groups, /cursor: grab;/)
  assert.match(groups, /cursor: grabbing;/)
  assert.match(groups, /cursor: move/)
})
