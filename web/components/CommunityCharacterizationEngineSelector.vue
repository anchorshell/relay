<!-- @format -->

<script setup lang="ts">
withDefaults(
  defineProps<{
    modelValue: "anchorshell" | "laya";
    disabled?: boolean;
    laya?: { configured: boolean; ready: boolean } | null;
  }>(),
  { disabled: false, laya: null },
);
const emit = defineEmits<{
  "update:modelValue": [value: "anchorshell" | "laya"];
}>();
const discovery = useUpgradeDiscovery();
function exploreHosted(event: MouseEvent) {
  if (event.currentTarget instanceof HTMLElement) {
    discovery?.open("enhancedClassification", event.currentTarget);
  }
}
</script>

<template>
  <CharacterizationEngineSelector
    :model-value="modelValue"
    :disabled="disabled"
    :laya="laya"
    basic-label="AnchorShell Classifier Basic"
    laya-label="Laya (system one model - open source)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <template #before-options>
      <button
        v-if="discovery"
        type="button"
        class="community-engine-option"
        :disabled="disabled"
        aria-haspopup="dialog"
        @click="exploreHosted"
      >
        <span class="engine-choice-marker" aria-hidden="true" />
        <span>
          <span class="app-heading-text font-semibold"
            >AnchorShell Classifier Pro + Enhanced (proprietary)</span
          >
          <span class="app-copy-text ml-2 text-xs">Fastest</span>
          <span class="app-copy-text mt-1 block text-sm"
            >Enhanced request classification in hosted Relay.</span
          >
        </span>
      </button>
    </template>
    <template #after-options>
      <label class="community-engine-option engine-coming-soon">
        <input type="radio" disabled />
        <span>
          <span class="app-heading-text font-semibold"
            >Jev (system one model - typesafe.ai)</span
          >
          <span class="app-copy-text ml-2 text-xs">Coming soon</span>
        </span>
      </label>
    </template>
    <template #help>
      <p class="app-copy-text mt-3 text-sm leading-6">
        To use Laya, run <code>make laya-start</code> in a separate terminal and
        keep the server running in the background.
        <a
          class="app-inline-link"
          href="https://github.com/anchorshell/relay"
          target="_blank"
          rel="noopener noreferrer"
          >See setup instructions on GitHub (opens in a new tab)</a
        >.
      </p>
    </template>
  </CharacterizationEngineSelector>
</template>

<style scoped>
.community-engine-option {
  display: flex;
  width: 100%;
  align-items: flex-start;
  gap: 0.75rem;
  padding: 0.75rem 0;
  text-align: start;
  cursor: pointer;
}
.engine-choice-marker {
  width: 13px;
  height: 13px;
  border: 1px solid var(--app-muted);
  border-radius: 50%;
}
.engine-choice-marker,
.community-engine-option input {
  margin-top: 0.25rem;
  flex-shrink: 0;
}
.community-engine-option:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: 4px;
}
.community-engine-option:disabled,
.engine-coming-soon {
  cursor: default;
  opacity: 0.65;
}
</style>
