<script setup lang="ts">
const route = useRoute()
const { modelRelayPath } = useModelRelayRoute()

onMounted(() => {
  navigateTo({ path: modelRelayPath('/groups'), query: route.query }, { replace: true })
})
</script>

<template>
  <div class="sr-only">Redirecting to groups…</div>
</template>
