<script setup lang="ts">
import { upgradeDiscoveryKey } from '../composables/useUpgradeDiscovery'
import { paidFeatures, type PaidFeatureID } from '../utils/paidFeatures'

const route = useRoute()
const auth = useAuthStore()
const featureID = ref<PaidFeatureID | null>(null)
const open = ref(false)
const trigger = shallowRef<HTMLElement | null>(null)
const feature = computed(() => featureID.value ? paidFeatures[featureID.value] : null)

provide(upgradeDiscoveryKey, {
  open(id, element) {
    featureID.value = id
    trigger.value = element
    open.value = true
  }
})

watch(() => route.fullPath, () => { open.value = false })
watch(() => auth.authenticated, () => { open.value = false })
</script>

<template>
  <slot />
  <UpgradeFeatureModal
    v-if="feature"
    v-model:open="open"
    :feature="feature"
    :return-focus-to="trigger"
  />
</template>
