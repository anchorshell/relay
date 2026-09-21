<script setup lang="ts">
const route = useRoute()
const { modelRelayPath } = useModelRelayRoute()

onMounted(() => {
  navigateTo(`${modelRelayPath('/groups')}?edit=${route.params.id}`, { replace: true })
})
</script>

<template>
  <div class="sr-only">Redirecting to groups…</div>
</template>
