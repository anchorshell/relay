<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import { init, use, type ECharts } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import type { CharacterizationIntentAnalyticsItem } from '~/types/admin'

const props = defineProps<{
  items: CharacterizationIntentAnalyticsItem[]
  metric: 'requests' | 'tokens' | 'spend'
}>()

const { number, currencyMicros } = useFormatters()
const { isDark } = useSiteTheme()

use([PieChart, TooltipComponent, CanvasRenderer])

const chartEl = ref<HTMLDivElement | null>(null)
let chart: ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let lastRenderKey = ''

const colors = [
  '#0ea5e9', '#f59e0b', '#10b981', '#8b5cf6', '#f43f5e', '#06b6d4', '#f97316',
  '#6366f1', '#84cc16', '#ec4899', '#14b8a6', '#eab308', '#3b82f6', '#a855f7',
  '#22c55e', '#ef4444', '#0891b2', '#d97706', '#4f46e5', '#65a30d', '#db2777',
  '#0d9488', '#ca8a04', '#2563eb', '#9333ea', '#16a34a', '#dc2626', '#0284c7', '#c2410c'
]

function intentLabel(value: string) {
  return value
    .replace(/[_-]+/g, ' ')
    .replace(/\b\w/g, character => character.toUpperCase())
}

function escapeHTML(value: string) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#039;')
}

function metricValue(value: number) {
  return props.metric === 'spend' ? currencyMicros(value) : number(value)
}

function metricUnit() {
  switch (props.metric) {
    case 'tokens':
      return 'tokens'
    case 'spend':
      return ''
    default:
      return 'requests'
  }
}

function renderChart() {
  if (!chartEl.value) return
  const compact = chartEl.value.clientWidth < 720
  const renderKey = JSON.stringify({ items: props.items, metric: props.metric, dark: isDark.value, compact })
  if (chart && renderKey === lastRenderKey) {
    chart.resize()
    return
  }
  if (!chart) {
    chart = init(chartEl.value)
  }

  const textColor = isDark.value ? '#cbd5e1' : '#334155'
  const maximumValue = Math.max(1, ...props.items.map(item => item.value))
  chart.setOption({
    animationDuration: 420,
    color: colors,
    tooltip: {
      trigger: 'item',
      borderWidth: 0,
      backgroundColor: isDark.value ? 'rgba(9, 19, 33, 0.96)' : 'rgba(255, 255, 255, 0.98)',
      textStyle: { color: textColor, fontSize: 12 },
      extraCssText: 'border-radius: 14px; padding: 10px 12px;',
      formatter: (params: any) => {
        const name = escapeHTML(String(params?.name || 'Unknown'))
        const value = Number(params?.data?.metricValue || 0)
        const percent = Number(params?.data?.percentage || 0)
        const unit = metricUnit()
        return `<strong>${name}</strong><br/>${metricValue(value)}${unit ? ` ${unit}` : ''} · ${percent.toFixed(1)}%`
      }
    },
    series: [
      {
        name: 'Characterization intents',
        type: 'pie',
        roseType: 'area',
        radius: compact ? ['8%', '58%'] : ['10%', '72%'],
        center: ['50%', '50%'],
        itemStyle: {
          borderColor: 'rgba(255, 255, 255, 0.9)',
          borderWidth: 1,
          borderRadius: 4
        },
        label: {
          show: !compact,
          color: textColor,
          fontSize: 10,
          fontWeight: 600,
          formatter: (params: any) => `${params?.name || ''} · ${Number(params?.data?.percentage || 0).toFixed(1)}%`
        },
        labelLine: {
          show: !compact
        },
        emphasis: {
          scale: true,
          scaleSize: 7,
          label: {
            show: !compact,
            color: textColor,
            fontSize: 10,
            fontWeight: 600
          }
        },
        data: props.items.map(item => ({
          name: intentLabel(item.primary_action),
          value: maximumValue * 0.42 + item.value * 0.58,
          metricValue: item.value,
          percentage: item.percentage
        }))
      }
    ]
  }, { notMerge: true })
  lastRenderKey = renderKey
}

function resizeChart() {
  renderChart()
}

onMounted(async () => {
  await nextTick()
  renderChart()
  if (chartEl.value && typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(resizeChart)
    resizeObserver.observe(chartEl.value)
  }
})

watch(() => [props.items, props.metric, isDark.value], renderChart, { deep: true })

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  chart?.dispose()
  chart = null
  lastRenderKey = ''
})
</script>

<template>
  <div ref="chartEl" class="h-[500px] w-full" />
</template>
