<script setup lang="ts">
const token = defineModel<string>({ required: true })
withDefaults(defineProps<{ help?: string }>(), {
  help: 'Kept only in memory until you leave this page. Leave empty only if inference authentication is disabled.'
})
const hintId = useId()
</script>

<template>
  <UiField label="Relay API Key" class="inference-token-field">
    <input
      v-model="token"
      type="password"
      autocomplete="off"
      autocapitalize="none"
      :spellcheck="false"
      :maxlength="4096"
      :aria-describedby="hintId"
      class="app-input inference-token-input"
      placeholder="Paste RELAY_API_TOKEN"
    >
    <p :id="hintId" class="app-copy-text mt-2 text-sm leading-6">
      {{ help }}
    </p>
  </UiField>
</template>

<style scoped>
.inference-token-field {
  height: auto;
}

/* Keep this password input readable and avoid focus zoom on mobile Safari. */
.app-input.inference-token-input {
  font-size: 16px;
}
</style>
