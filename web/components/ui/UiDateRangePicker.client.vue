<script setup lang="ts">
import { getLocalTimeZone, parseDate, today, type CalendarDate } from '@internationalized/date'

type PresetRange = {
  label: string
  days?: number
  months?: number
  years?: number
}

type CalendarRange = {
  start: CalendarDate | undefined
  end: CalendarDate | undefined
}

const props = defineProps<{
  startDate: string
  endDate: string
  placeholder?: string
}>()

const emit = defineEmits<{
  'update:startDate': [value: string]
  'update:endDate': [value: string]
}>()

const timezone = getLocalTimeZone()
const isDesktop = ref(false)
const open = ref(false)
const modelValue = shallowRef<CalendarRange | null>(rangeFromProps())

let mediaQuery: MediaQueryList | null = null
let forcingSameDay = false

const ranges: PresetRange[] = [
  { label: 'Today', days: 0 },
  { label: 'Last 7 days', days: 7 },
  { label: 'Last 14 days', days: 14 },
  { label: 'Last 30 days', days: 30 },
  { label: 'Last 3 months', months: 3 },
  { label: 'Last 6 months', months: 6 },
  { label: 'Last year', years: 1 }
]

const label = computed(() => {
  const { start, end } = modelValue.value || {}
  if (!start) return props.placeholder || 'Select date range'
  const startLabel = calendarDateToString(start)
  const endLabel = calendarDateToString(end || start)
  return `${startLabel} - ${endLabel}`
})

function parseDateValue(value: string) {
  if (!value) return undefined
  try {
    return parseDate(value)
  } catch {
    return undefined
  }
}

function calendarDateToString(value: CalendarDate | null | undefined) {
  return value ? value.toString() : ''
}

function orderedRange(start: CalendarDate | null | undefined, end: CalendarDate | null | undefined): CalendarRange | null {
  if (!start && !end) return null
  if (start && !end) return { start, end: undefined }
  if (!start && end) return { start: end, end }
  if (start && end && start.compare(end) > 0) return { start: end, end: start }
  return { start, end }
}

function rangeFromProps() {
  return orderedRange(parseDateValue(props.startDate), parseDateValue(props.endDate))
}

function syncFromProps() {
  const next = rangeFromProps()
  const current = modelValue.value
  if (
    calendarDateToString(current?.start) === calendarDateToString(next?.start) &&
    calendarDateToString(current?.end) === calendarDateToString(next?.end)
  ) {
    return
  }
  modelValue.value = next
}

function emitRange(range: CalendarRange | null) {
  const ordered = orderedRange(range?.start, range?.end)
  const currentStart = calendarDateToString(ordered?.start)
  const currentEnd = calendarDateToString(ordered?.end || ordered?.start)
  if (props.startDate !== currentStart) emit('update:startDate', currentStart)
  if (props.endDate !== currentEnd) emit('update:endDate', currentEnd)
}

function computePresetRange(range: PresetRange) {
  const end = today(timezone)
  const duration = {
    ...(range.days !== undefined ? { days: range.days } : {}),
    ...(range.months !== undefined ? { months: range.months } : {}),
    ...(range.years !== undefined ? { years: range.years } : {})
  }
  return {
    start: end.subtract(duration),
    end
  }
}

function isRangeSelected(range: PresetRange) {
  const selected = modelValue.value
  if (!selected?.start || !selected.end) return false
  const preset = computePresetRange(range)
  return selected.start.compare(preset.start) === 0 && selected.end.compare(preset.end) === 0
}

function selectRange(range: PresetRange) {
  completeRange(computePresetRange(range))
}

function sameCalendarDate(a: CalendarDate | null | undefined, b: CalendarDate | null | undefined) {
  return Boolean(a && b && a.compare(b) === 0)
}

function closePopover() {
  requestAnimationFrame(() => {
    open.value = false
  })
}

function completeRange(range: CalendarRange | null) {
  const ordered = orderedRange(range?.start, range?.end)
  if (!ordered?.start) return
  const complete = {
    start: ordered.start,
    end: ordered.end || ordered.start
  }
  modelValue.value = complete
  emitRange(complete)
  closePopover()
}

function handleCalendarUpdate(value: CalendarRange | null) {
  if (forcingSameDay) return
  const ordered = orderedRange(value?.start, value?.end)
  modelValue.value = ordered
  if (ordered?.start && ordered.end) {
    completeRange(ordered)
  }
}

function handleDayPointerDown(day: CalendarDate) {
  const current = modelValue.value
  if (!current?.start || current.end || !sameCalendarDate(current.start, day)) return
  forcingSameDay = true
  requestAnimationFrame(() => {
    completeRange({ start: day, end: day })
    requestAnimationFrame(() => {
      forcingSameDay = false
    })
  })
}

function updateDesktopFlag(event?: MediaQueryListEvent) {
  isDesktop.value = event ? event.matches : Boolean(mediaQuery?.matches)
}

watch(() => [props.startDate, props.endDate], syncFromProps)

onMounted(() => {
  mediaQuery = window.matchMedia('(min-width: 640px)')
  updateDesktopFlag()
  mediaQuery.addEventListener('change', updateDesktopFlag)
})

onBeforeUnmount(() => {
  mediaQuery?.removeEventListener('change', updateDesktopFlag)
  mediaQuery = null
})
</script>

<template>
  <UPopover v-model:open="open" :content="{ align: 'end', sideOffset: 8, collisionPadding: 12 }">
    <UButton
      color="neutral"
      variant="subtle"
      icon="i-lucide-calendar"
      class="usage-date-range-trigger"
    >
      {{ label }}
    </UButton>

    <template #content>
      <div class="usage-date-range-popover">
        <div class="usage-date-range-presets">
          <UButton
            v-for="range in ranges"
            :key="range.label"
            :label="range.label"
            color="neutral"
            variant="ghost"
            class="usage-date-range-preset"
            :class="{ 'usage-date-range-preset-active': isRangeSelected(range) }"
            truncate
            @click="selectRange(range)"
          />
        </div>

        <UCalendar
          :model-value="modelValue"
          class="usage-date-range-calendar"
          :number-of-months="isDesktop ? 2 : 1"
          prevent-deselect
          range
          @update:model-value="handleCalendarUpdate"
        >
          <template #day="{ day }">
            <span class="usage-date-range-day" @pointerdown.capture="handleDayPointerDown(day)">
              {{ day.day }}
            </span>
          </template>
        </UCalendar>
      </div>
    </template>
  </UPopover>
</template>

<style scoped>
.usage-date-range-trigger {
  width: max-content;
  min-width: calc(23ch + 3.75rem);
  max-width: 100%;
  min-height: 2.75rem;
  justify-content: flex-start;
  border: 1px solid var(--app-input-border);
  border-radius: 0.9rem;
  background: var(--app-input-bg);
  color: var(--app-heading);
  box-shadow: var(--app-input-shadow);
  white-space: nowrap;
}

.usage-date-range-popover {
  display: flex;
  align-items: stretch;
  max-width: calc(100vw - 2rem);
  overflow: hidden;
  border: 1px solid var(--app-border);
  border-radius: 1rem;
  background: var(--app-surface);
  color: var(--app-heading);
  box-shadow: var(--app-panel-shadow);
}

.usage-date-range-presets {
  display: none;
  min-width: 10.5rem;
  flex-direction: column;
  justify-content: center;
  border-right: 1px solid var(--app-border);
  padding: 0.5rem 0;
}

.usage-date-range-preset {
  justify-content: flex-start;
  border-radius: 0;
  padding-inline: 1rem;
}

.usage-date-range-preset-active {
  background: color-mix(in srgb, var(--app-accent) 14%, var(--app-surface));
  color: var(--app-heading);
}

.usage-date-range-calendar {
  padding: 0.5rem;
}

.usage-date-range-day {
  display: inline-flex;
  min-width: 1.75rem;
  justify-content: center;
}

@media (min-width: 640px) {
  .usage-date-range-presets {
    display: flex;
  }
}

:global(:root[data-site-theme='light']) .usage-date-range-trigger {
  background: var(--app-surface);
}

:global(:root[data-site-theme='light']) .usage-date-range-popover {
  background: var(--app-surface);
}
</style>
