import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { runInNewContext } from 'node:vm'
import test from 'node:test'

const require = createRequire(new URL('../../web/package.json', import.meta.url))
const { parse, compileTemplate } = require('@vue/compiler-sfc')
const { transpileModule } = require('typescript')
const { ref, reactive, computed, watch } = require('vue')
const source = readFileSync(new URL('../../web/components/groups/GroupEditDrawer.vue', import.meta.url), 'utf8')
const { descriptor } = parse(source)

function fixture() {
  const props = reactive({ open: true, groupId: 'group-a', readonly: false })
  const rows = []
  const calls = []
  const toasts = []
  let complete
  let fail
  const pending = new Promise((resolve, reject) => { complete = resolve; fail = reject })
  const catalog = {
    lanes: [{ id: 'group-a', name: 'Support' }, { id: 'group-b', name: 'Another group' }],
    membershipsByLane: id => rows.filter(row => row.lane_id === id),
    saveResource: async (resource, payload) => {
      calls.push({ resource, payload })
      await pending
      rows.push({ ...payload, id: `membership-${rows.length}` })
    },
    reorderMemberships: async () => {},
  }
  // Execute the actual drawer handlers against an in-memory catalog, not a server.
  const script = descriptor.scriptSetup.content.replace(/^import .*$/gm, '')
  const js = transpileModule(script, { compilerOptions: { target: 99 } }).outputText
  const drawer = runInNewContext(`(() => { ${js}; return { addMembership, dropOnEnd }; })()`, {
    ref, reactive, computed, watch,
    useRuntimeConfig: () => ({ public: {} }),
    defineProps: () => props,
    withDefaults: value => value,
    defineEmits: () => () => {},
    useCatalogStore: () => catalog,
    useAppStore: () => ({ pushToast: toast => toasts.push(toast) }),
    useModelRelayRoute: () => ({ modelRelayApiPath: path => path }),
    onMounted: () => {},
  })
  return { ...drawer, props, rows, calls, toasts, complete, fail }
}

test('empty-group drop stops propagation to its parent dropzone', () => {
  const compiled = compileTemplate({ source: descriptor.template.content, filename: 'GroupEditDrawer.vue', id: 'groups' })
  assert.deepEqual(compiled.errors, [])
  assert.match(compiled.code, /dropOnEnd\(\$event\)\), \["prevent","stop"\]/)
  assert.doesNotMatch(source, /@drop\.prevent="dropOnEnd/)
})

test('a repeated drop makes only one membership request and one success toast', async () => {
  const state = fixture()
  const drop = { dataTransfer: { getData: () => 'available:model-a' } }
  const first = state.dropOnEnd(drop)
  await state.dropOnEnd(drop)
  assert.equal(state.calls.length, 1)
  state.complete()
  await first
  await state.dropOnEnd(drop)
  assert.equal(state.calls.length, 1)
  assert.equal(state.rows.length, 1)
  assert.equal(state.toasts.length, 1)
  assert.equal(state.toasts[0].title, 'Model added')
})

test('membership save stays attached to its original group if the drawer selection changes', async () => {
  const state = fixture()
  const first = state.addMembership('model-a')
  state.props.groupId = 'group-b'
  state.complete()
  await first
  assert.equal(state.rows[0].lane_id, 'group-a')
})

test('failed membership saves release the guard and never report success', async () => {
  const state = fixture()
  const first = state.addMembership('model-a')
  state.fail(new Error('fixture failure'))
  await first
  await state.addMembership('model-a')
  assert.equal(state.calls.length, 2)
  assert.equal(state.rows.length, 0)
  assert.ok(state.toasts.every(toast => toast.tone === 'error'))
})

test('readonly drawers cannot add a model; cards offer a keyboard/touch Add action', async () => {
  const state = fixture()
  state.props.readonly = true
  await state.addMembership('model-a')
  assert.equal(state.calls.length, 0)
  assert.match(source, /@click="addMembership\(model.id\)"/)
  assert.match(source, /i-lucide-grip-vertical/)
  assert.doesNotMatch(source, /class="group-draggable-card app-subsurface/)
  assert.match(source, /background: var\(--app-subsurface\)/)
})
