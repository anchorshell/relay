<script setup lang="ts">
type UsageAnalyticsQueryParamValue = string | number | boolean | Array<string | number | boolean> | null | undefined

defineProps<{
  usageAnalyticsParams?: Record<string, UsageAnalyticsQueryParamValue>
}>()
</script>

<template>
  <UsageBaseUsagePage :usage-analytics-params="usageAnalyticsParams">
    <template v-if="$slots['filters-extra']" #filters-extra="slotProps">
      <slot name="filters-extra" v-bind="slotProps" />
    </template>

    <template v-if="$slots['summary-extra']" #summary-extra="slotProps">
      <slot name="summary-extra" v-bind="slotProps" />
    </template>

    <template v-if="$slots['table-before']" #table-before="slotProps">
      <slot name="table-before" v-bind="slotProps" />
    </template>

    <template v-if="$slots['empty-extra']" #empty-extra="slotProps">
      <slot name="empty-extra" v-bind="slotProps" />
    </template>

    <template v-if="$slots['table-after']" #table-after="slotProps">
      <slot name="table-after" v-bind="slotProps" />
    </template>
  </UsageBaseUsagePage>
</template>
