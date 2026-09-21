import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire, stripTypeScriptTypes } from 'node:module'
import test from 'node:test'
import { DEFAULT_RELAY_UPGRADE_URL, paidFeatures, paidFeatureLabel, relayUpgradeURL } from '../../web/utils/paidFeatures.ts'
import { upgradeDiscoveryKey, useUpgradeDiscovery } from '../../web/composables/useUpgradeDiscovery.ts'

const source = path => readFileSync(new URL(`../../web/${path}`, import.meta.url), 'utf8')

test('all hosted features have distinct, concrete copy and one safe CTA destination', () => {
  assert.deepEqual(Object.keys(paidFeatures), ['enhancedClassification', 'smartGroups', 'teams', 'permissions', 'apiKeys', 'budgets', 'usageAttribution', 'connectedProviders', 'classificationFeedback'])
  assert.equal(new Set(Object.values(paidFeatures).map(feature => feature.description)).size, 9)
  for (const [id, feature] of Object.entries(paidFeatures)) {
    for (const field of ['title', 'actionLabel', 'headline', 'description', 'ossNote', 'ctaLabel', 'availability', 'icon']) assert.ok(feature[field], `${id}.${field}`)
    assert.equal(feature.availability, 'Available in hosted AnchorShell Relay. Hosted Relay includes a free tier; feature availability depends on your plan.')
    assert.equal(feature.ctaLabel, id === 'enhancedClassification' ? 'Get started with hosted Relay' : 'Explore hosted Relay')
  }
  assert.equal(DEFAULT_RELAY_UPGRADE_URL, 'https://anchorshell.com/pricing')
  assert.equal(relayUpgradeURL(undefined), DEFAULT_RELAY_UPGRADE_URL)
  for (const url of ['javascript:alert(1)', 'http://example.test', '//example.test', 'https://user:secret@example.test', 'not a URL']) {
    assert.equal(relayUpgradeURL(url), DEFAULT_RELAY_UPGRADE_URL)
  }
  assert.equal(relayUpgradeURL('https://example.test/relay'), 'https://example.test/relay')
  assert.equal(paidFeatureLabel('apiKeys', true), 'API Keys')
  assert.equal(paidFeatureLabel('apiKeys'), 'Managed API Keys')
  assert.equal(paidFeatureLabel('smartGroups'), 'Add Smart Group')
  assert.equal(paidFeatureLabel('connectedProviders'), 'Add Subscription Plan')
})

test('product copy preserves OSS functionality and avoids unsupported subscription/training claims', () => {
  assert.match(paidFeatures.smartGroups.ossNote, /Standard groups/)
  assert.match(paidFeatures.apiKeys.ossNote, /RELAY_API_TOKEN/)
  assert.match(paidFeatures.budgets.ossNote, /provider.*resource limits/)
  assert.match(paidFeatures.usageAttribution.ossNote, /analytics/)
  assert.match(paidFeatures.classificationFeedback.ossNote, /Basic request characterization/)
  assert.match(paidFeatures.classificationFeedback.description, /does not automatically retrain/)
  assert.match(paidFeatures.connectedProviders.description, /OpenAI Codex/)
  assert.doesNotMatch(paidFeatures.connectedProviders.description, /Claude|Copilot|Gemini|unlimited/)
})

test('discovery is absent by default and uses only the explicitly provided root context', t => {
  let context = null
  Object.defineProperty(globalThis, 'inject', {
    configurable: true,
    value: (key, fallback) => {
      assert.equal(key, upgradeDiscoveryKey)
      return context ?? fallback
    }
  })
  t.after(() => Reflect.deleteProperty(globalThis, 'inject'))
  assert.equal(useUpgradeDiscovery(), null)
  let opened = ''
  context = { open: id => { opened = id } }
  useUpgradeDiscovery().open('teams', {})
  assert.equal(opened, 'teams')
  assert.match(source('app.vue'), /<UpgradeDiscoveryProvider>/)
  assert.match(source('components/UpgradeFeatureButton.vue'), /v-if="discovery && navigation"/)
  assert.match(source('components/UpgradeFeatureButton.vue'), /v-else-if="discovery"/)
  assert.doesNotMatch(source('composables/useUpgradeDiscovery.ts'), /useState|defineStore|localStorage|fetch\(/)
})

test('contextual actions use existing slots and leave ordinary group creation intact', () => {
  const placements = [
    ['pages/groups/index.vue', 'smartGroups'],
    ['pages/providers.vue', 'connectedProviders'],
    ['pages/limits.vue', 'budgets'],
    ['pages/usage.vue', 'usageAttribution'],
    ['pages/settings.vue', 'permissions'],
    ['pages/settings.vue', 'apiKeys'],
    ['pages/requests.vue', 'classificationFeedback'],
    ['components/shell/SidebarNav.vue', 'teams'],
    ['components/shell/SidebarNav.vue', 'apiKeys']
  ]
  for (const [path, feature] of placements) assert.ok(source(path).includes(`feature="${feature}"`), path)
  assert.match(source('components/BaseGroupsPage.vue'), /@click="openCreate">Add Group/)
  assert.match(source('components/BaseGroupsPage.vue'), /@click="createGroup">Create Group/)
  assert.match(source('components/requests/RequestInspector.vue'), /<slot v-if="characterization" name="characterization-actions"/)
  assert.match(source('layers/model-relay-base/components/BaseLogsPage.vue'), /v-if="\$slots\['characterization-actions'\]"/)
  for (const path of ['components/UpgradeFeatureButton.vue', 'components/UpgradeDiscoveryProvider.vue', 'components/UpgradeFeatureModal.vue']) {
    assert.doesNotMatch(source(path), /\$fetch|useFetch|useRelayApi|\/api\/|sendBeacon|localStorage/)
  }
  assert.match(source('components/UpgradeFeatureModal.vue'), /<UModal/)
  assert.match(source('components/UpgradeFeatureModal.vue'), /onCloseAutoFocus: restoreFocus/)
  assert.match(source('components/UpgradeFeatureModal.vue'), /rel="noopener noreferrer"/)
  assert.doesNotMatch(source('components/UpgradeFeatureModal.vue'), /<UIcon/)
  assert.doesNotMatch(source('components/UpgradeFeatureButton.vue'), /<UIcon/)
  assert.doesNotMatch(source('components/UpgradeFeatureButton.vue'), /upgrade-badge|upgrade-feature-action|>Pro<|paid AnchorShell/)
  assert.match(source('components/UpgradeFeatureButton.vue'), /tone: 'primary', size: 'md'/)
  for (const path of ['pages/groups/index.vue', 'pages/providers.vue']) {
    assert.match(source(path), /icon="i-lucide-plus" class="w-full sm:w-auto"/)
  }
  assert.match(source('pages/limits.vue'), /tone="secondary" size="sm"/)
})

test('actual trigger component renders no markup in an inheriting app without the OSS provider', async () => {
  // Compile the real SFC with the already-installed Vue compiler. Nuxt's two
  // auto-imports are supplied explicitly; there is no private-repo dependency.
  const require = createRequire(new URL('../../web/package.json', import.meta.url))
  const vue = require('vue')
  const { renderToString } = require('@vue/server-renderer')
  const { parse, compileScript } = require('@vue/compiler-sfc')
  const { descriptor } = parse(source('components/UpgradeFeatureButton.vue'))
  const compiled = stripTypeScriptTypes(compileScript(descriptor, { id: 'discovery-test', inlineTemplate: true, genDefaultAs: 'component' }).content)
    .replace(/import \{([^}]+)\} from ["']vue["']/g, (_match, names) => `const {${names.replace(/\bas\b/g, ':')}} = vue`)
    .replace(/import \{[^}]+\} from ["']\.\.\/utils\/paidFeatures["']/g, '')
  const Button = new Function('vue', 'paidFeatures', 'paidFeatureLabel', 'useUpgradeDiscovery', 'computed', `${compiled}; return component`)(
    vue, paidFeatures, paidFeatureLabel, () => vue.inject(upgradeDiscoveryKey, null), vue.computed
  )
  for (const feature of Object.keys(paidFeatures)) {
    for (const navigation of [false, true]) {
      const createApp = () => {
        const app = vue.createSSRApp({ render: () => vue.h(Button, { feature, navigation }) })
        app.component('UiButton', { render() { return vue.h('button', this.$slots.default?.()) } })
        return app
      }
      assert.equal((await renderToString(createApp())).replace(/<!--[\s\S]*?-->/g, ''), '', `${feature} leaked without an OSS root`)
      const app = createApp()
      app.provide(upgradeDiscoveryKey, { open() {} })
      const visible = await renderToString(app)
      assert.ok(visible.includes(`data-upgrade-feature="${feature}"`))
      assert.ok(visible.includes('aria-haspopup="dialog"'))
      assert.ok(!visible.includes('upgrade-badge') && !visible.includes('>Pro<'))
    }
  }
})
