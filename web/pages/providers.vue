<template>
  <BaseProvidersPage>
    <template #provider-header-actions>
      <UpgradeFeatureButton feature="connectedProviders" icon="i-lucide-plus" class="w-full sm:w-auto" />
    </template>
    <template v-if="$slots['before-provider-list']" #before-provider-list>
      <slot name="before-provider-list" />
    </template>
  </BaseProvidersPage>
</template>
