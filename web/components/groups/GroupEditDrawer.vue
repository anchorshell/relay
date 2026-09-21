<script setup lang="ts">
import { buildInferenceCurl, inferenceExampleURL } from '../../utils/inference'

const runtimeConfig = useRuntimeConfig()
const props = withDefaults(defineProps<{
  open: boolean
  groupId: string | null
  subtitle?: string
  readonly?: boolean
}>(), {
  subtitle: 'Adjust cooldown max wait and ranked membership without leaving this page.',
  readonly: false
})

const emit = defineEmits<{
  close: []
}>()

const catalog = useCatalogStore()
const app = useAppStore()
const { modelRelayApiPath } = useModelRelayRoute()
const runtimeOrigin = ref('')
type GroupMembership = ReturnType<typeof catalog.membershipsByLane>[number]
type VisualRankRow =
  | { type: 'membership', key: string, membership: GroupMembership, rank: number }
  | { type: 'placeholder', key: string, rank: number }

const REORDER_EDGE_RATIO = 0.34
const REORDER_PREVIEW_DEBOUNCE_MS = 56
const GROUP_DND_MIME = 'application/x-anchorshell-group-dnd'
type GroupDragPayload = { type: 'available', endpointId: string } | { type: 'membership', membershipId: string }

const dragPayload = ref<GroupDragPayload | null>(null)
const dragOverRankIndex = ref<number | null>(null)
const rankDropHover = ref(false)
const availableDropHover = ref(false)
const optimisticRankOrderIds = ref<string[] | null>(null)
const pendingMemberships = reactive(new Set<string>())
let queuedRankIndex: number | null = null
let rankUpdateFrame: number | null = null
let rankUpdateTimer: ReturnType<typeof setTimeout> | null = null

const form = reactive({
  id: '',
  name: '',
  default_max_wait_ms: '60000',
  enabled: true
})

const selectedGroupID = computed(() => String(props.groupId || ''))
const selectedGroup = computed(() => catalog.lanes.find((item) => item.id === selectedGroupID.value) || null)
const isReadonly = computed(() => props.readonly)
const memberships = computed(() => selectedGroupID.value ? catalog.membershipsByLane(selectedGroupID.value) : [])
const isDragging = computed(() => !!dragPayload.value)
const canDropOnAvailable = computed(() => dragPayload.value?.type === 'membership')
const visualMemberships = computed(() => {
  const ordered = memberships.value.slice()
  const optimisticIds = optimisticRankOrderIds.value
  if (optimisticIds === null) return ordered

  const byId = new Map(ordered.map(item => [item.id, item]))
  const seen = new Set<string>()
  const visual = optimisticIds.flatMap((id) => {
    const item = byId.get(id)
    if (!item) return []
    seen.add(id)
    return [item]
  })
  return [
    ...visual,
    ...ordered.filter(item => !seen.has(item.id))
  ]
})
const visualRankRows = computed<VisualRankRow[]>(() => {
  const payload = dragPayload.value
  const targetIndex = dragOverRankIndex.value
  const shouldPreview = rankDropHover.value && payload && targetIndex !== null
  const ordered = visualMemberships.value.slice()

  if (shouldPreview && payload.type === 'membership') {
    const currentIndex = ordered.findIndex(item => item.id === payload.membershipId)
    if (currentIndex >= 0) {
      const [dragged] = ordered.splice(currentIndex, 1)
      ordered.splice(Math.max(0, Math.min(targetIndex, ordered.length)), 0, dragged)
    }
  }

  const rows: VisualRankRow[] = []
  const placeholderIndex = shouldPreview && payload.type === 'available'
    ? Math.max(0, Math.min(targetIndex, ordered.length))
    : null
  let rank = 1

  ordered.forEach((membership, index) => {
    if (placeholderIndex === index) {
      rows.push({ type: 'placeholder', key: 'rank-drop-preview', rank: rank++ })
    }
    rows.push({ type: 'membership', key: `membership:${membership.id}`, membership, rank: rank++ })
  })

  if (placeholderIndex === ordered.length) {
    rows.push({ type: 'placeholder', key: 'rank-drop-preview', rank })
  }

  return rows
})
const availableModels = computed(() => {
  if (!selectedGroupID.value) return []
  const memberIds = new Set(memberships.value.map((item) => item.endpoint_id))
  return catalog.endpoints
    .filter((item) => !memberIds.has(item.id))
    .slice()
    .sort((a, b) => {
      const providerA = catalog.providerMap[a.provider_id]?.slug || ''
      const providerB = catalog.providerMap[b.provider_id]?.slug || ''
      return providerA.localeCompare(providerB) || a.slug.localeCompare(b.slug) || a.id.localeCompare(b.id)
    })
})

watch(selectedGroup, (value) => {
  if (!value) return
  Object.assign(form, {
    id: value.id,
    name: value.name,
    default_max_wait_ms: String(value.default_max_wait_ms || 60000),
    enabled: value.enabled
  })
}, { immediate: true })

function closeDrawer() {
  clearDragState()
  emit('close')
}

function modelIdentity(endpointId: string) {
  const endpoint = catalog.endpointMap[endpointId]
  if (!endpoint) {
    return {
      label: 'provider-missing/unknown-model'
    }
  }
  const providerSlug = catalog.providerMap[endpoint.provider_id]?.slug?.trim() || 'provider-missing'
  const modelSlug = endpoint.slug?.trim() || 'unnamed-model'
  return {
    label: `${providerSlug}/${modelSlug}`
  }
}

async function saveGroup() {
  if (props.readonly) return
  if (!selectedGroup.value) return
  try {
    await catalog.saveResource('routing-lanes', {
      id: form.id,
      name: form.name.trim(),
      description: selectedGroup.value.description || '',
      enabled: form.enabled,
      default_max_wait_ms: Number(form.default_max_wait_ms || 60000),
      allow_fallback: true,
      default_priority: selectedGroup.value.default_priority
    })
    app.pushToast({ title: 'Group updated', description: 'Wait behavior and group settings were saved.' })
    closeDrawer()
  } catch (cause: any) {
    app.pushToast({
      title: 'Group could not be saved',
      description: cause?.data?.error || cause?.data?.message || cause?.message || 'Please choose a different group name.',
      tone: 'error'
    })
  }
}

async function removeMembership(id: string) {
  if (props.readonly) return
  await catalog.deleteResource('lane-memberships', id)
  app.pushToast({ title: 'Model removed', description: 'The model is no longer part of this group.' })
}

async function reorderMembership(membershipId: string, targetIndex: number) {
  if (props.readonly) return
  if (!selectedGroup.value) return
  const ids = reorderedMembershipIds(membershipId, targetIndex)
  if (!ids) return
  await catalog.reorderMemberships(selectedGroupID.value, ids)
}

function reorderedMembershipIds(membershipId: string, targetIndex: number) {
  const ids = memberships.value.map((item) => item.id)
  const currentIndex = ids.indexOf(membershipId)
  if (currentIndex < 0) return null
  ids.splice(currentIndex, 1)
  ids.splice(targetIndex, 0, membershipId)
  return ids
}

async function addMembership(endpointId: string, targetIndex = memberships.value.length) {
  if (props.readonly || !selectedGroup.value) return
  const groupId = selectedGroupID.value
  const pendingKey = `${groupId}:${endpointId}`
  if (pendingMemberships.has(pendingKey) || catalog.membershipsByLane(groupId).some(item => item.endpoint_id === endpointId)) return
  // A drop (or repeated click) can arrive again before the catalog refresh ends.
  pendingMemberships.add(pendingKey)
  try {
    await catalog.saveResource('lane-memberships', {
      lane_id: groupId,
      endpoint_id: endpointId,
      manual_rank: targetIndex + 1,
      enabled: true
    })
    const groupMemberships = catalog.membershipsByLane(groupId)
    const created = groupMemberships.find((item) => item.endpoint_id === endpointId)
    if (created && targetIndex < groupMemberships.length - 1) {
      const ids = groupMemberships.map((item) => item.id)
      const currentIndex = ids.indexOf(created.id)
      if (currentIndex >= 0) {
        ids.splice(currentIndex, 1)
        ids.splice(targetIndex, 0, created.id)
        await catalog.reorderMemberships(groupId, ids)
      }
    }
    app.pushToast({ title: 'Model added', description: 'The model was added to the ranked group list.' })
  } catch (cause: any) {
    app.pushToast({ title: 'Model could not be added', description: cause?.data?.error || 'Please try again.', tone: 'error' })
  } finally {
    pendingMemberships.delete(pendingKey)
  }
}

function encodeDragPayload(payload: GroupDragPayload) {
  return payload.type === 'available'
    ? `available:${payload.endpointId}`
    : `membership:${payload.membershipId}`
}

function buildGroupCurl(groupName: string) {
  const requestPath = modelRelayApiPath('/v1/chat/completions')
  const requestURL = inferenceExampleURL(requestPath, runtimeOrigin.value, String(runtimeConfig.public.relayWsTarget || ''))
  const requestBody = JSON.stringify({
    model: groupName,
    messages: [{
      role: 'user',
      content: 'Explain which model you selected.'
    }]
  }, null, 2)
  return buildInferenceCurl(requestBody, requestURL)
}

onMounted(() => {
  runtimeOrigin.value = window.location.origin
})

function decodeDragPayload(value: string): GroupDragPayload | null {
  const [type, rawId] = value.split(':')
  const id = String(rawId || '')
  if (!id) return null
  if (type === 'available') return { type, endpointId: id }
  if (type === 'membership') return { type, membershipId: id }
  return null
}

function setDragPayload(event: DragEvent, payload: GroupDragPayload) {
  dragPayload.value = payload
  if (!event.dataTransfer) return
  const encoded = encodeDragPayload(payload)
  event.dataTransfer.effectAllowed = 'move'
  event.dataTransfer.dropEffect = 'move'
  try {
    event.dataTransfer.setData(GROUP_DND_MIME, encoded)
  } catch {
    // Plain text is the cross-browser fallback used by payloadFromDropEvent.
  }
  event.dataTransfer.setData('text/plain', encoded)
}

function payloadFromDropEvent(event?: DragEvent) {
  if (dragPayload.value) return dragPayload.value
  const transfer = event?.dataTransfer
  if (!transfer) return null
  return decodeDragPayload(transfer.getData(GROUP_DND_MIME) || transfer.getData('text/plain'))
}

function startAvailableDrag(event: DragEvent, endpointId: string) {
  if (props.readonly) return
  cancelQueuedRankDrop()
  setDragPayload(event, { type: 'available', endpointId })
  dragOverRankIndex.value = memberships.value.length
}

function startMembershipDrag(event: DragEvent, membershipId: string) {
  if (props.readonly) return
  cancelQueuedRankDrop()
  setDragPayload(event, { type: 'membership', membershipId })
  dragOverRankIndex.value = memberships.value.findIndex(item => item.id === membershipId)
}

function cancelQueuedRankDrop() {
  if (rankUpdateTimer !== null && typeof window !== 'undefined') {
    window.clearTimeout(rankUpdateTimer)
  }
  rankUpdateTimer = null
  if (rankUpdateFrame !== null && typeof window !== 'undefined') {
    window.cancelAnimationFrame(rankUpdateFrame)
  }
  rankUpdateFrame = null
  queuedRankIndex = null
}

function clearDragState() {
  cancelQueuedRankDrop()
  dragPayload.value = null
  dragOverRankIndex.value = null
  rankDropHover.value = false
  availableDropHover.value = false
}

function clearDragPayload() {
  clearDragState()
}

function maxRankDropIndex(payload = dragPayload.value) {
  if (payload?.type === 'membership') {
    return Math.max(0, memberships.value.length - 1)
  }
  return memberships.value.length
}

function queueRankDropIndex(targetIndex: number) {
  const payload = dragPayload.value
  if (!payload) return
  const nextIndex = Math.max(0, Math.min(targetIndex, maxRankDropIndex(payload)))
  if (dragOverRankIndex.value === nextIndex && queuedRankIndex === null && rankUpdateTimer === null) return
  queuedRankIndex = nextIndex
  if (rankUpdateFrame !== null || rankUpdateTimer !== null) return
  if (typeof window === 'undefined') {
    dragOverRankIndex.value = nextIndex
    queuedRankIndex = null
    return
  }
  rankUpdateTimer = window.setTimeout(() => {
    rankUpdateTimer = null
    rankUpdateFrame = window.requestAnimationFrame(() => {
      rankUpdateFrame = null
      if (queuedRankIndex === null) return
      dragOverRankIndex.value = queuedRankIndex
      queuedRankIndex = null
    })
  }, REORDER_PREVIEW_DEBOUNCE_MS)
}

function rankTargetFromCard(event: DragEvent, membershipId: string, force = false) {
  const payload = dragPayload.value
  if (!payload) return null
  if (payload.type === 'membership' && payload.membershipId === membershipId) {
    return null
  }
  const card = event.currentTarget as HTMLElement | null
  if (!card) return null
  const rect = card.getBoundingClientRect()
  if (!rect.height) return null
  const ratio = Math.max(0, Math.min(1, (event.clientY - rect.top) / rect.height))
  let placeAfter: boolean
  if (payload.type === 'membership' && !force) {
    if (ratio < REORDER_EDGE_RATIO) {
      placeAfter = false
    } else if (ratio > 1 - REORDER_EDGE_RATIO) {
      placeAfter = true
    } else {
      return null
    }
  } else {
    placeAfter = ratio >= 0.5
  }

  const orderedIds = memberships.value
    .map(item => item.id)
    .filter(id => payload.type !== 'membership' || id !== payload.membershipId)
  const hoverIndex = orderedIds.indexOf(membershipId)
  if (hoverIndex < 0) return null
  return hoverIndex + (placeAfter ? 1 : 0)
}

function markRankZoneOver(event: DragEvent) {
  if (props.readonly) return
  if (!dragPayload.value) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  rankDropHover.value = true
  availableDropHover.value = false
}

function markRankCardDrop(event: DragEvent, membershipId: string) {
  if (props.readonly) return
  if (!dragPayload.value) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  rankDropHover.value = true
  availableDropHover.value = false
  const targetIndex = rankTargetFromCard(event, membershipId)
  if (targetIndex !== null) queueRankDropIndex(targetIndex)
}

function markRankEndDrop(event: DragEvent) {
  if (props.readonly) return
  if (!dragPayload.value) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  rankDropHover.value = true
  availableDropHover.value = false
  queueRankDropIndex(maxRankDropIndex())
}

function markRankPlaceholderDrop(event: DragEvent) {
  if (props.readonly) return
  if (!dragPayload.value) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  rankDropHover.value = true
  availableDropHover.value = false
}

function markRankRowDrop(event: DragEvent, row: VisualRankRow) {
  if (row.type === 'placeholder') {
    markRankPlaceholderDrop(event)
    return
  }
  markRankCardDrop(event, row.membership.id)
}

function leaveRankDrop(event: DragEvent) {
  const current = event.currentTarget as HTMLElement | null
  const next = event.relatedTarget as Node | null
  if (current && next && current.contains(next)) return
  cancelQueuedRankDrop()
  rankDropHover.value = false
  dragOverRankIndex.value = dragPayload.value?.type === 'membership'
    ? memberships.value.findIndex(item => item.id === dragPayload.value?.membershipId)
    : null
}

function markAvailableDrop(event: DragEvent) {
  if (props.readonly) return
  if (!canDropOnAvailable.value) return
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  cancelQueuedRankDrop()
  availableDropHover.value = true
  rankDropHover.value = false
  dragOverRankIndex.value = dragPayload.value?.type === 'membership'
    ? memberships.value.findIndex(item => item.id === dragPayload.value?.membershipId)
    : null
}

function leaveAvailableDrop(event: DragEvent) {
  const current = event.currentTarget as HTMLElement | null
  const next = event.relatedTarget as Node | null
  if (current && next && current.contains(next)) return
  availableDropHover.value = false
}

async function dropOnIndex(targetIndex: number, event?: DragEvent) {
  if (props.readonly) return
  const payload = payloadFromDropEvent(event)
  if (!payload) return
  const resolvedIndex = Math.max(0, Math.min(targetIndex, maxRankDropIndex(payload)))
  if (payload.type === 'available') {
    clearDragState()
    await addMembership(payload.endpointId, resolvedIndex)
    return
  }
  const nextOrderIds = reorderedMembershipIds(payload.membershipId, resolvedIndex)
  if (!nextOrderIds) {
    clearDragState()
    return
  }
  optimisticRankOrderIds.value = nextOrderIds
  clearDragState()
  try {
    await reorderMembership(payload.membershipId, resolvedIndex)
    app.pushToast({ title: 'Order updated', description: 'The ranked order inside this group was updated.' })
  } finally {
    optimisticRankOrderIds.value = null
  }
}

async function dropOnEnd(event: DragEvent) {
  await dropOnIndex(maxRankDropIndex(), event)
}

async function dropOnCurrentRank(event: DragEvent) {
  await dropOnIndex(dragOverRankIndex.value ?? maxRankDropIndex(), event)
}

async function dropOnRankCard(event: DragEvent, membershipId: string) {
  const targetIndex = rankTargetFromCard(event, membershipId, true)
  if (targetIndex !== null) queueRankDropIndex(targetIndex)
  await dropOnIndex(targetIndex ?? dragOverRankIndex.value ?? maxRankDropIndex(), event)
}

async function dropOnRankRow(event: DragEvent, row: VisualRankRow) {
  if (row.type === 'placeholder') {
    await dropOnCurrentRank(event)
    return
  }
  await dropOnRankCard(event, row.membership.id)
}

async function dropOnAvailable(event: DragEvent) {
  if (props.readonly) return
  const payload = payloadFromDropEvent(event)
  if (!payload || payload.type !== 'membership') return
  optimisticRankOrderIds.value = memberships.value
    .map(item => item.id)
    .filter(id => id !== payload.membershipId)
  clearDragState()
  try {
    await removeMembership(payload.membershipId)
  } finally {
    optimisticRankOrderIds.value = null
  }
}
</script>

<template>
  <UiInspectorDrawer
    :open="open && !!selectedGroup"
    :title="selectedGroup?.name || (isReadonly ? 'View Group' : 'Edit Group')"
    :subtitle="subtitle"
    @close="closeDrawer"
  >
    <div v-if="selectedGroup" class="space-y-6">
      <UiPanel tone="subsurface" title="Group settings" subtitle="Control how long Model Relay should wait through cooldown before it becomes reasonable to try another ranked option.">
        <div class="space-y-4">
          <div class="grid gap-4 md:auto-rows-fr md:grid-cols-2">
            <UiField label="Group name" help="Use a short task-oriented name like thinking, cheap, or embeddings." required>
              <input v-model="form.name" class="app-input" placeholder="Customer support" :disabled="isReadonly" />
            </UiField>

            <UiField label="Cooldown max wait (ms)" help="If the best model should free up within this cooldown window, Model Relay can keep waiting instead of degrading too early." required>
              <input v-model="form.default_max_wait_ms" class="app-input" inputmode="numeric" placeholder="60000" :disabled="isReadonly" />
            </UiField>

          </div>

          <label class="cursor-pointer has-disabled:cursor-not-allowed block border-y border-[var(--app-border)] py-4 text-sm text-slate-200/84">
            <span class="flex items-start gap-3">
              <input v-model="form.enabled" type="checkbox" class="app-checkbox mt-1" :disabled="isReadonly" />
              <span>
                <span class="block font-medium text-white">Group enabled</span>
                <span class="mt-1 block leading-6 text-slate-300/72">Disable this group if you want to keep the record but stop routing traffic through it.</span>
              </span>
            </span>
          </label>
        </div>
      </UiPanel>

      <div class="grid gap-6 xl:grid-cols-[0.92fr_1.08fr]">
        <UiPanel tone="subsurface" title="Available models" subtitle="Drag a provider/model pair into the ranked list to add it to this group, or drag a ranked model back here to remove it.">
          <div
            class="group-dropzone space-y-3"
            :class="{
              'is-drop-armed': isDragging && canDropOnAvailable,
              'is-drop-hot': availableDropHover
            }"
            @dragenter.prevent="markAvailableDrop"
            @dragover.prevent="markAvailableDrop"
            @dragleave="leaveAvailableDrop"
            @drop.prevent="dropOnAvailable($event)"
          >
            <div
              v-for="model in availableModels"
              :key="model.id"
              :draggable="!isReadonly"
              class="group-draggable-card flex items-center gap-3 px-5 py-4"
              :class="{ 'is-readonly': isReadonly, 'is-being-dragged': dragPayload?.type === 'available' && dragPayload.endpointId === model.id }"
              @dragstart="startAvailableDrag($event, model.id)"
              @dragend="clearDragPayload"
            >
              <UIcon v-if="!isReadonly" name="i-lucide-grip-vertical" class="size-4 shrink-0 text-[var(--app-muted)]" aria-hidden="true" />
              <p class="min-w-0 flex-1 truncate text-sm font-medium text-[var(--app-heading)]" :title="modelIdentity(model.id).label">{{ modelIdentity(model.id).label }}</p>
              <UiButton v-if="!isReadonly" tone="secondary" size="sm" :disabled="pendingMemberships.has(`${selectedGroupID}:${model.id}`)" :aria-label="`Add ${modelIdentity(model.id).label} to group`" @click="addMembership(model.id)">Add</UiButton>
            </div>
            <div
              v-if="!availableModels.length"
              class="available-empty-dropzone"
              :class="{
                'is-drop-armed': isDragging && canDropOnAvailable,
                'is-drop-hot': availableDropHover
              }"
            >
              <p>{{ canDropOnAvailable ? 'Drop here to remove from ranked models' : 'All configured models are in this group' }}</p>
              <span>Drag a ranked model back here when you want to remove it from the group.</span>
            </div>
          </div>
        </UiPanel>

        <UiPanel tone="subsurface" title="Ranked models" subtitle="Drag to reorder the waterfall. Lower positions are only used when waiting no longer makes sense.">
          <div
            class="group-dropzone"
            :class="{
              'is-drop-armed': isDragging,
              'is-drop-hot': rankDropHover
            }"
            @dragenter.prevent="markRankZoneOver"
            @dragover.prevent="markRankZoneOver"
            @dragleave="leaveRankDrop"
            @drop.prevent="dropOnCurrentRank($event)"
          >
            <div
              v-if="!memberships.length"
              class="group-empty-dropzone app-subsurface flex min-h-[12rem] items-center justify-center border border-dashed border-white/12 p-8 text-center"
              :class="{ 'is-drop-hot': rankDropHover }"
              @dragenter.prevent.stop="markRankEndDrop"
              @dragover.prevent.stop="markRankEndDrop"
              @drop.prevent.stop="dropOnEnd($event)"
            >
              <div class="max-w-sm space-y-3">
                <p class="text-base font-semibold text-white">No ranked models in this group yet</p>
                <p class="text-sm leading-7 text-slate-300/72">
                  Drag one of the available models into this area to define the routing order for this group.
                </p>
              </div>
            </div>

            <TransitionGroup
              v-else
              name="ranked-model-motion"
              tag="div"
              class="ranked-model-list"
            >
              <div
                v-for="row in visualRankRows"
                :key="row.key"
                :draggable="!isReadonly && row.type === 'membership'"
                class="ranked-model-row"
                :class="{ 'is-preview-row': row.type === 'placeholder' }"
                @dragstart="row.type === 'membership' && startMembershipDrag($event, row.membership.id)"
                @dragend="clearDragPayload"
                @dragenter.prevent.stop="markRankRowDrop($event, row)"
                @dragover.prevent.stop="markRankRowDrop($event, row)"
                @drop.prevent.stop="dropOnRankRow($event, row)"
              >
                <div
                  v-if="row.type === 'placeholder'"
                  class="ranked-model-drop-preview"
                >
                  <span class="text-xs font-semibold uppercase tracking-[0.22em]">#{{ row.rank }}</span>
                  <span class="ranked-model-drop-preview__line" />
                </div>

                <div
                  v-else
                  class="group-draggable-card px-5 py-4 text-left"
                  :class="{ 'is-readonly': isReadonly, 'is-being-dragged': dragPayload?.type === 'membership' && dragPayload.membershipId === row.membership.id }"
                >
                  <div class="grid gap-3 md:grid-cols-[auto_minmax(0,1fr)_auto] md:items-center">
                    <div class="flex items-center gap-2 text-xs font-semibold text-[var(--app-muted)]">
                      <UIcon v-if="!isReadonly" name="i-lucide-grip-vertical" class="size-4 shrink-0" aria-hidden="true" />
                      <span>#{{ row.rank }}</span>
                    </div>
                    <div class="min-w-0">
                      <p class="truncate text-sm font-medium text-[var(--app-heading)]" :title="modelIdentity(row.membership.endpoint_id).label">{{ modelIdentity(row.membership.endpoint_id).label }}</p>
                    </div>
                    <UiButton v-if="!isReadonly" tone="danger" size="sm" @click.stop="removeMembership(row.membership.id)">Remove</UiButton>
                  </div>
                </div>
              </div>
            </TransitionGroup>

            <div
              v-if="memberships.length"
              class="ranked-model-drop-tail"
              :class="{ 'is-drop-hot': rankDropHover && dragOverRankIndex === maxRankDropIndex() && dragPayload?.type === 'membership' }"
              @dragenter.prevent.stop="markRankEndDrop"
              @dragover.prevent.stop="markRankEndDrop"
              @drop.prevent.stop="dropOnEnd($event)"
            />
          </div>
        </UiPanel>
      </div>

      <UiPanel tone="subsurface" title="Test this group" subtitle="Use the group alias directly against the gateway to verify its behavior.">
        <UiCodeBlock :code="buildGroupCurl(selectedGroup.name)" label="Gateway curl" />
      </UiPanel>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <UiButton v-if="!isReadonly" class="w-full sm:w-auto" @click="saveGroup">Save Group</UiButton>
      </div>
    </template>
  </UiInspectorDrawer>
</template>

<style scoped>
.group-dropzone {
  min-height: 100%;
  border: 1px dashed transparent;
  border-radius: 1.15rem;
  margin: -0.35rem;
  padding: 0.35rem;
  transition:
    background-color 180ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease,
    backdrop-filter 180ms ease;
}

.group-dropzone.is-drop-armed {
  border-color: color-mix(in srgb, var(--app-border) 72%, transparent);
  background: var(--app-surface);
  cursor: move;
}

.group-dropzone.is-drop-hot {
  border-color: color-mix(in srgb, #22d3ee 56%, var(--app-border));
  background: color-mix(in srgb, #22d3ee 10%, var(--app-surface));
  box-shadow:
    inset 0 0 0 1px color-mix(in srgb, #22d3ee 18%, transparent),
    0 18px 42px color-mix(in srgb, #22d3ee 10%, transparent);
  backdrop-filter: blur(8px);
}

.group-empty-dropzone {
  transition:
    background-color 180ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease,
    transform 180ms ease;
}

.group-empty-dropzone.is-drop-hot {
  border-color: color-mix(in srgb, #22d3ee 68%, var(--app-border));
  background: color-mix(in srgb, #22d3ee 12%, #0b1422);
  box-shadow:
    inset 0 0 0 1px color-mix(in srgb, #22d3ee 20%, transparent),
    0 16px 44px color-mix(in srgb, #22d3ee 12%, transparent);
  transform: translateY(-1px);
}

.available-empty-dropzone {
  display: flex;
  min-height: 14rem;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  border: 1px dashed color-mix(in srgb, var(--app-border) 82%, transparent);
  border-radius: 1rem;
  background: var(--app-surface);
  padding: 1.5rem;
  text-align: center;
  transition:
    background-color 180ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease,
    transform 180ms ease;
}

.available-empty-dropzone.is-drop-armed {
  border-color: color-mix(in srgb, #22d3ee 36%, var(--app-border));
  background: color-mix(in srgb, #22d3ee 7%, var(--app-surface));
}

.available-empty-dropzone p {
  margin: 0;
  color: var(--app-heading);
  font-size: 0.9rem;
  font-weight: 800;
}

.available-empty-dropzone span {
  display: block;
  max-width: 18rem;
  margin-top: 0.45rem;
  color: var(--app-muted);
  font-size: 0.78rem;
  line-height: 1.45;
}

.available-empty-dropzone.is-drop-hot {
  border-color: color-mix(in srgb, #22d3ee 68%, var(--app-border));
  background: color-mix(in srgb, #22d3ee 12%, var(--app-surface));
  box-shadow:
    inset 0 0 0 1px color-mix(in srgb, #22d3ee 18%, transparent),
    0 18px 42px color-mix(in srgb, #22d3ee 10%, transparent);
  transform: translateY(-1px);
}

.ranked-model-list {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.ranked-model-row {
  position: relative;
}

.ranked-model-row.is-preview-row {
  cursor: move;
}

.ranked-model-drop-preview {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-items: center;
  gap: 0.9rem;
  min-height: 3.25rem;
  border: 1px dashed color-mix(in srgb, #22d3ee 68%, var(--app-border));
  border-radius: 1rem;
  padding: 0.85rem 1rem;
  color: color-mix(in srgb, #67e8f9 82%, white);
  background: var(--app-surface-muted);
  box-shadow:
    inset 0 0 0 1px color-mix(in srgb, #22d3ee 16%, transparent),
    0 12px 34px color-mix(in srgb, #22d3ee 10%, transparent);
}

.ranked-model-drop-preview__line {
  display: block;
  height: 2px;
  border-radius: 999px;
  background: var(--app-accent);
}

.ranked-model-drop-tail {
  min-height: 1.45rem;
  border: 1px dashed transparent;
  border-radius: 1rem;
  margin-top: 0.75rem;
  transition:
    background-color 160ms ease,
    border-color 160ms ease;
}

.ranked-model-drop-tail.is-drop-hot {
  border-color: color-mix(in srgb, #22d3ee 48%, var(--app-border));
  background: color-mix(in srgb, #22d3ee 9%, var(--app-surface));
}

.group-draggable-card {
  border: 1px solid var(--app-border);
  border-radius: 0.75rem;
  background: var(--app-subsurface);
  cursor: grab;
  user-select: none;
  transition:
    transform 220ms cubic-bezier(0.22, 1, 0.36, 1),
    opacity 160ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease,
    background-color 180ms ease;
}

.group-draggable-card:not(.is-readonly):hover {
  border-color: var(--app-accent);
}

.group-draggable-card.is-readonly {
  cursor: default;
}

.group-draggable-card:active,
.group-draggable-card.is-being-dragged {
  cursor: grabbing;
}

.group-draggable-card.is-being-dragged {
  opacity: 0.58;
  border-style: dashed;
  border-color: color-mix(in srgb, #22d3ee 58%, var(--app-border));
  box-shadow: 0 12px 34px color-mix(in srgb, #22d3ee 10%, transparent);
}

.ranked-model-motion-move,
.ranked-model-motion-enter-active,
.ranked-model-motion-leave-active {
  transition:
    transform 260ms cubic-bezier(0.22, 1, 0.36, 1),
    opacity 180ms ease,
    border-color 180ms ease,
    box-shadow 180ms ease;
}

.ranked-model-motion-enter-from,
.ranked-model-motion-leave-to {
  opacity: 0;
  transform: translateY(10px) scale(0.985);
}

.ranked-model-motion-leave-active {
  position: absolute;
  left: 0;
  right: 0;
}

@media (prefers-reduced-motion: reduce) {
  .group-dropzone,
  .group-empty-dropzone,
  .available-empty-dropzone,
  .group-draggable-card,
  .ranked-model-drop-preview,
  .ranked-model-drop-tail,
  .ranked-model-motion-move,
  .ranked-model-motion-enter-active,
  .ranked-model-motion-leave-active {
    transition: none;
  }
}
</style>
