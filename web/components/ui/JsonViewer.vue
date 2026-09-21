<script setup lang="ts">
defineProps<{ value: any }>()
</script>

<template>
  <pre class="app-code">{{ JSON.stringify(value, null, 2) }}</pre>
</template>
