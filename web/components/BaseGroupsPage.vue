<script setup lang="ts">
import type { RoutingLane } from '~/types/admin'

const props = withDefaults(defineProps<{
  managedGroupIds?: string[]
  showManagedSection?: boolean
}>(), {
  managedGroupIds: () => [],
  showManagedSection: false
})

const route = useRoute()
const router = useRouter()
const catalog = useCatalogStore()
const pageData = useAdminPageDataStore()
const app = useAppStore()
const api = useRelayApi()
const relayPermissions = useModelRelayPermissions()
const canManageGroups = computed(() => relayPermissions.has('relay:groups:manage'))
const managedGroupIDSet = computed(() => new Set(props.managedGroupIds))
const managedGroups = computed(() => catalog.lanes.filter(group => managedGroupIDSet.value.has(group.id)))
const regularGroups = computed(() => catalog.lanes.filter(group => !managedGroupIDSet.value.has(group.id)))
const groupSections = computed(() => [
  ...(props.showManagedSection
    ? [{ key: 'smart', label: 'Smart Groups', emptyLabel: 'No smart groups have been created yet.', groups: managedGroups.value }]
    : []),
  {
    key: 'standard',
    label: props.showManagedSection ? 'Standard Groups' : 'Groups',
    emptyLabel: 'No standard groups have been created yet.',
    groups: regularGroups.value
  }
])

const createOpen = ref(false)
const duplicateGroupID = ref('')
const deleteGroupOpen = ref(false)
const deleteGroupID = ref('')
const deletingGroup = ref(false)

const createForm = reactive({
  name: '',
  default_max_wait_ms: '60000',
  enabled: true
})

const selectedGroupID = computed(() => {
  const raw = Array.isArray(route.query.edit) ? route.query.edit[0] : route.query.edit
  return String(raw || '')
})

const selectedGroup = computed(() => catalog.lanes.find((item) => item.id === selectedGroupID.value) || null)

function modelIdentity(endpointId: string) {
  const endpoint = catalog.endpointMap[endpointId]
  if (!endpoint) {
    return {
      label: 'provider-missing/unknown-model',
      displayName: 'Unavailable model'
    }
  }
  const providerSlug = catalog.providerMap[endpoint.provider_id]?.slug?.trim() || 'provider-missing'
  const modelSlug = endpoint.slug?.trim() || 'unnamed-model'
  return {
    label: `${providerSlug}/${modelSlug}`,
    displayName: endpoint.name || endpoint.upstream_model || modelSlug
  }
}

function openCreate() {
  if (!canManageGroups.value) return
  createOpen.value = true
}

function openEdit(groupId: string) {
  return router.replace({
    query: {
      ...route.query,
      edit: String(groupId)
    }
  })
}

function closeEdit() {
  const nextQuery = { ...route.query }
  delete nextQuery.edit
  return router.replace({ query: nextQuery })
}

async function createGroup() {
  if (!canManageGroups.value) return
  try {
    await catalog.saveResource('routing-lanes', {
      name: createForm.name.trim(),
      description: '',
      enabled: createForm.enabled,
      default_max_wait_ms: Number(createForm.default_max_wait_ms || 60000),
      allow_fallback: true,
      default_priority: 50
    })
    createOpen.value = false
    Object.assign(createForm, {
      name: '',
      default_max_wait_ms: '60000',
      enabled: true
    })
    app.pushToast({ title: 'Group created', description: 'The group is ready for ranked model membership editing.' })
  } catch (cause: any) {
    app.pushToast({
      title: 'Group could not be created',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please choose a different group name.',
      tone: 'error'
    })
  }
}

function duplicateName(group: RoutingLane) {
  const base = `${group.name} copy`
  const existing = new Set(catalog.lanes.map(item => item.name.toLowerCase()))
  if (!existing.has(base.toLowerCase())) return base
  let suffix = 2
  while (existing.has(`${base} ${suffix}`.toLowerCase())) suffix += 1
  return `${base} ${suffix}`
}

async function duplicateGroup(group: RoutingLane) {
  if (!canManageGroups.value || duplicateGroupID.value) return
  duplicateGroupID.value = group.id
  try {
    const duplicate = await api.post<RoutingLane>('/api/routing-lanes', {
      name: duplicateName(group),
      description: group.description,
      enabled: group.enabled,
      default_max_wait_ms: group.default_max_wait_ms,
      allow_fallback: group.allow_fallback,
      default_priority: group.default_priority
    })
    for (const membership of catalog.membershipsByLane(group.id)) {
      await api.post('/api/lane-memberships', {
        lane_id: duplicate.id,
        endpoint_id: membership.endpoint_id,
        manual_rank: membership.manual_rank,
        enabled: membership.enabled
      })
    }
    await pageData.load('groups')
    app.pushToast({
      title: 'Group duplicated',
      description: `${duplicate.name} includes the same ranked models and routing settings.`
    })
  } finally {
    duplicateGroupID.value = ''
  }
}

function requestDeleteGroup(group: RoutingLane) {
  if (!canManageGroups.value) return
  deleteGroupID.value = group.id
  deleteGroupOpen.value = true
}

function closeDeleteGroup() {
  if (deletingGroup.value) return
  deleteGroupOpen.value = false
  deleteGroupID.value = ''
}

const deleteGroupTarget = computed(() => catalog.laneMap[deleteGroupID.value] || null)

async function confirmDeleteGroup() {
  const target = deleteGroupTarget.value
  if (!target || deletingGroup.value) return
  deletingGroup.value = true
  try {
    await catalog.deleteResource('routing-lanes', target.id)
    if (selectedGroupID.value === target.id) await closeEdit()
    deleteGroupOpen.value = false
    deleteGroupID.value = ''
    app.pushToast({
      title: 'Group deleted',
      description: `${target.name} and its ranked memberships were removed.`
    })
  } finally {
    deletingGroup.value = false
  }
}

function groupMoreMenuItems(group: RoutingLane) {
  if (!canManageGroups.value) return []
  return [[
    {
      label: 'Duplicate Group',
      disabled: Boolean(duplicateGroupID.value),
      onSelect: () => duplicateGroup(group)
    },
    {
      label: 'Delete Group',
      color: 'error' as const,
      onSelect: () => requestDeleteGroup(group)
    }
  ]]
}

onMounted(() => pageData.load('groups'))
</script>

<template>
  <div>
    <div class="space-y-8">
      <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div>
          <h1 class="app-title">Groups</h1>
        </div>
        <div class="flex w-full flex-col gap-2 sm:w-auto sm:flex-row">
          <slot name="header-actions" :can-manage="canManageGroups" />
          <UiButton v-if="canManageGroups" icon="i-lucide-plus" class="w-full sm:w-auto" @click="openCreate">Add Group</UiButton>
        </div>
      </div>

      <section>
        <UiEmptyState
          v-if="!catalog.lanes.length"
          title="No groups defined yet"
          description="Create a group to add ranked model membership."
          :action-label="canManageGroups ? 'Create Group' : ''"
          @action="openCreate"
        />

        <div v-else class="space-y-8">
          <section v-for="section in groupSections" :key="section.key" class="ui-directory-section" :aria-labelledby="`group-section-${section.key}`">
            <header class="ui-directory-section__header">
              <h2 :id="`group-section-${section.key}`" class="ui-directory-section__title">{{ section.label }}</h2>
            </header>

            <div v-if="section.groups.length" class="ui-directory" :aria-label="section.label">
              <section v-for="group in section.groups" :key="group.id" class="ui-directory__parent">
                <slot
                  v-if="managedGroupIDSet.has(group.id)"
                  name="managed-group"
                  :group="group"
                  :can-manage="canManageGroups"
                />
                <template v-else>
                  <header class="ui-directory__heading">
                    <h3 class="ui-directory__name text-base">{{ group.name }}</h3>
                    <div class="ui-directory__actions">
                      <UiButton tone="secondary" size="sm" @click="openEdit(group.id)">{{ canManageGroups ? 'Edit group' : 'View group' }}</UiButton>
                      <UDropdownMenu
                        v-if="canManageGroups"
                        :items="groupMoreMenuItems(group)"
                        :content="{ align: 'end', sideOffset: 8, collisionPadding: 12 }"
                      >
                        <button type="button" class="ui-action-menu-button" aria-label="More group actions">
                          <span>More</span>
                          <UIcon name="i-lucide-ellipsis" class="h-4 w-4" />
                        </button>
                      </UDropdownMenu>
                    </div>
                  </header>

                  <ul class="ui-directory__children" :aria-label="`Models in ${group.name}`">
                    <li v-for="membership in catalog.membershipsByLane(group.id)" :key="membership.id" class="ui-directory__child">
                      <div class="min-w-0">
                        <p class="ui-directory__name">{{ modelIdentity(membership.endpoint_id).displayName }}</p>
                        <p
                          v-if="modelIdentity(membership.endpoint_id).displayName !== modelIdentity(membership.endpoint_id).label"
                          class="ui-directory__detail font-mono"
                        >
                          {{ modelIdentity(membership.endpoint_id).label }}
                        </p>
                      </div>
                    </li>
                    <li v-if="!catalog.membershipsByLane(group.id).length" class="ui-directory__child">
                      <p class="ui-directory__detail">No models are assigned to this group yet.</p>
                    </li>
                  </ul>
                </template>
              </section>
            </div>
            <p v-else class="ui-directory-section__empty">{{ section.emptyLabel }}</p>
          </section>
        </div>
      </section>
    </div>

    <UiModal
      :open="createOpen"
      title="Create Group"
      subtitle="Groups define cooldown max wait and ranked membership for related models. Create the group here, then manage membership from the edit drawer."
      @close="createOpen = false"
    >
      <div class="space-y-7">
        <UiField label="Group name" help="Use a short task-oriented name like thinking, cheap, or embeddings." required>
          <input v-model="createForm.name" class="app-input" placeholder="Customer support" />
        </UiField>

        <UiField label="Cooldown max wait (ms)" help="How long Model Relay may keep waiting through cooldown for the preferred model before considering another option." required>
          <input v-model="createForm.default_max_wait_ms" class="app-input" inputmode="numeric" placeholder="60000" />
        </UiField>

        <div class="flex justify-end">
          <UiButton class="w-full sm:w-auto" @click="createGroup">Create Group</UiButton>
        </div>
      </div>
    </UiModal>

    <UiModal
      :open="deleteGroupOpen"
      title="Delete Group"
      subtitle="This removes the group and its ranked model memberships. Provider models are not deleted."
      @close="closeDeleteGroup"
    >
      <div class="space-y-6">
        <p class="text-sm leading-6 text-slate-300">
          Delete <span class="font-semibold text-white">{{ deleteGroupTarget?.name }}</span>? Requests will no longer be able to route using this group name.
        </p>
        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <UiButton tone="ghost" :disabled="deletingGroup" @click="closeDeleteGroup">Cancel</UiButton>
          <UiButton tone="danger" :disabled="deletingGroup" @click="confirmDeleteGroup">
            {{ deletingGroup ? 'Deleting…' : 'Delete Group' }}
          </UiButton>
        </div>
      </div>
    </UiModal>

    <GroupsGroupEditDrawer
      :open="!!selectedGroup"
      :group-id="selectedGroupID"
      :readonly="!canManageGroups"
      subtitle="Adjust cooldown max wait and ranked membership without leaving the groups page."
      @close="closeEdit"
    />
    <slot name="managed-overlays" :can-manage="canManageGroups" />
  </div>
</template>
