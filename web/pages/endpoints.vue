<script setup lang="ts">
const { modelRelayPath } = useModelRelayRoute()

onMounted(() => {
  navigateTo(modelRelayPath('/providers'), { replace: true })
})
</script>

<template>
  <div class="sr-only">Redirecting to providers…</div>
</template>
