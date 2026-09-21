<script setup lang="ts">
defineProps<{ columns: string[] }>()
</script>

<template>
  <div class="ui-table-wrap w-full max-w-full overflow-x-auto overflow-y-hidden rounded-lg border">
    <table class="ui-table min-w-max border-separate border-spacing-0 text-[13px] sm:min-w-full [&_td+td]:border-l [&_th+th]:border-l">
      <thead class="bg-transparent">
        <tr>
          <th v-for="column in columns" :key="column" class="ui-table-head-cell whitespace-nowrap px-3 py-2 text-left text-[10px] font-semibold uppercase tracking-[0.12em] first:rounded-tl-lg last:rounded-tr-lg">
            {{ column }}
          </th>
        </tr>
      </thead>
      <tbody class="bg-transparent [&_tr+tr_td]:border-t">
        <slot />
      </tbody>
    </table>
  </div>
</template>
