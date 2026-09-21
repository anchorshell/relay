<script setup lang="ts">
const { modelRelayPath } = useModelRelayRoute()

await navigateTo(modelRelayPath('/usage'), { redirectCode: 301 })
</script>
