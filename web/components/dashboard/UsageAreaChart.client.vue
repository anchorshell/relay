<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { graphic, init, use, type ECharts } from 'echarts/core'
import { EffectScatterChart, LineChart } from 'echarts/charts'
import { BrushComponent, DataZoomComponent, GridComponent, ToolboxComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'

const props = defineProps<{
  metric: 'requests' | 'tokens' | 'spend'
  window: 'hour' | 'day' | 'week' | 'month'
  points: Array<{
    label: string
    value: number | null
    tokensUp?: number | null
    tokensDown?: number | null
    highlight?: boolean
  }>
  title?: string
  subtitle?: string
  badgeLabel?: string
  zoomable?: boolean
  surface?: boolean
}>()

const emit = defineEmits<{
  'zoom-change': [active: boolean]
  'zoom-range': [range: { startIndex: number, endIndex: number } | null]
}>()

const { number, currencyMicros } = useFormatters()
const { isDark } = useSiteTheme()

use([LineChart, EffectScatterChart, BrushComponent, DataZoomComponent, GridComponent, ToolboxComponent, TooltipComponent, CanvasRenderer])

const chartEl = ref<HTMLDivElement | null>(null)
const hasActiveZoom = ref(false)
const hasTokenBreakdown = computed(() =>
  props.metric === 'tokens' && props.points.some(point => point.tokensUp != null || point.tokensDown != null)
)
let chart: ECharts | null = null
let resizeObserver: ResizeObserver | null = null
let chartEventsBound = false
let lastRenderKey = ''

const palette = computed(() => {
  switch (props.metric) {
    case 'tokens':
      return {
        line: '#f59e0b',
        glow: 'rgba(245, 158, 11, 0.24)',
        top: 'rgba(251, 191, 36, 0.34)',
        bottom: 'rgba(245, 158, 11, 0.02)'
      }
    case 'spend':
      return {
        line: '#34d399',
        glow: 'rgba(52, 211, 153, 0.24)',
        top: 'rgba(52, 211, 153, 0.32)',
        bottom: 'rgba(52, 211, 153, 0.02)'
      }
    default:
      return {
        line: '#38bdf8',
        glow: 'rgba(56, 189, 248, 0.24)',
        top: 'rgba(59, 130, 246, 0.34)',
        bottom: 'rgba(14, 165, 233, 0.02)'
      }
  }
})

const chartTheme = computed(() => isDark.value
  ? {
      tooltipBg: 'rgba(9, 19, 33, 0.96)',
      tooltipText: '#e2e8f0',
      tooltipShadow: '0 22px 60px rgba(2, 6, 23, 0.42)',
      axisLine: 'rgba(148, 163, 184, 0.18)',
      axisLabel: 'rgba(148, 163, 184, 0.78)',
      yAxisLabel: 'rgba(148, 163, 184, 0.68)',
      splitLine: 'rgba(148, 163, 184, 0.08)',
      highlightFill: 'rgba(241, 245, 249, 0.96)',
      highlightRipple: 'rgba(241, 245, 249, 0.56)',
      highlightShadow: 'rgba(56, 189, 248, 0.24)'
    }
  : {
      tooltipBg: 'rgba(255, 255, 255, 0.98)',
      tooltipText: '#10233c',
      tooltipShadow: '0 18px 44px rgba(10, 31, 52, 0.16)',
      axisLine: 'rgba(7, 29, 54, 0.18)',
      axisLabel: 'rgba(45, 64, 85, 0.8)',
      yAxisLabel: 'rgba(75, 96, 119, 0.78)',
      splitLine: 'rgba(7, 29, 54, 0.09)',
      highlightFill: 'rgba(6, 24, 44, 0.9)',
      highlightRipple: 'rgba(6, 24, 44, 0.42)',
      highlightShadow: 'rgba(8, 145, 178, 0.28)'
    })

function formatMetricValue(value: number) {
  return props.metric === 'spend' ? currencyMicros(value) : number(value)
}

function chartPointData(point: { value: number | null, highlight?: boolean }) {
  if (point.highlight) {
    const theme = chartTheme.value
    return {
      value: point.value,
      symbol: 'circle',
      symbolSize: 10,
      itemStyle: {
        color: theme.highlightFill,
        borderColor: palette.value.line,
        borderWidth: 3,
        shadowColor: theme.highlightShadow,
        shadowBlur: 16
      }
    }
  }
  return {
    value: point.value,
    symbol: 'circle',
    symbolSize: 0
  }
}

function highlightedScatterData() {
  return props.points
    .filter(point => point.highlight && point.value != null)
    .map(point => [point.label, point.value as number])
}

function tooltipValue(param: any) {
  if (typeof param?.value === 'number') return param.value
  if (Array.isArray(param?.value) && typeof param.value[1] === 'number') return param.value[1]
  if (typeof param?.data === 'number') return param.data
  if (Array.isArray(param?.data) && typeof param.data[1] === 'number') return param.data[1]
  if (typeof param?.data?.value === 'number') return param.data.value
  if (Array.isArray(param?.data?.value) && typeof param.data.value[1] === 'number') return param.data.value[1]
  return null
}

function setZoomState(active: boolean) {
  if (hasActiveZoom.value === active) return
  hasActiveZoom.value = active
  emit('zoom-change', active)
}

function clearBrushSelection() {
  chart?.dispatchAction({
    type: 'brush',
    areas: []
  })
}

function enableDefaultBrushZoom() {
  if (!props.zoomable || !chart) return
  chart.dispatchAction({
    type: 'takeGlobalCursor',
    key: 'brush',
    brushOption: {
      brushType: 'lineX',
      brushMode: 'single'
    }
  })
}

function pointIndexFromBrushValue(value: unknown) {
  if (typeof value === 'string') {
    return props.points.findIndex(point => point.label === value)
  }
  const numeric = Number(value)
  return Number.isFinite(numeric) ? numeric : -1
}

function selectedBrushRange(params: any) {
  const areas = [
    ...(Array.isArray(params?.areas) ? params.areas : []),
    ...(Array.isArray(params?.batch) ? params.batch.flatMap((item: any) => Array.isArray(item?.areas) ? item.areas : []) : [])
  ]
  const area = [...areas].reverse().find((item: any) => item?.brushType === 'lineX' && Array.isArray(item?.coordRange))
  if (!area) return null

  const [rawStart, rawEnd] = area.coordRange
  const start = pointIndexFromBrushValue(rawStart)
  const end = pointIndexFromBrushValue(rawEnd)
  if (start < 0 || end < 0) return null

  const min = Math.max(0, Math.floor(Math.min(start, end)))
  const max = Math.min(props.points.length - 1, Math.ceil(Math.max(start, end)))
  if (max <= min) return null
  return { min, max }
}

function applyBrushZoom(params: any) {
  if (!props.zoomable || !chart || props.points.length < 2) return
  const range = selectedBrushRange(params)
  clearBrushSelection()
  if (!range) {
    enableDefaultBrushZoom()
    return
  }

  chart.dispatchAction({
    type: 'dataZoom',
    dataZoomIndex: 0,
    startValue: range.min,
    endValue: range.max
  })
  setZoomState(!(range.min === 0 && range.max === props.points.length - 1))
  emit('zoom-range', { startIndex: range.min, endIndex: range.max })
  enableDefaultBrushZoom()
}

function resetZoom() {
  if (!chart) return
  chart.dispatchAction({
    type: 'dataZoom',
    dataZoomIndex: 0,
    start: 0,
    end: 100
  })
  clearBrushSelection()
  setZoomState(false)
  emit('zoom-range', null)
  enableDefaultBrushZoom()
}

function bindChartEvents() {
  if (!chart || chartEventsBound) return
  chart.on('brushEnd', applyBrushZoom as any)
  chartEventsBound = true
}

function renderChart() {
  if (!chartEl.value) return
  const renderKey = JSON.stringify({
    metric: props.metric,
    window: props.window,
    points: props.points,
    zoomable: props.zoomable,
    dark: isDark.value
  })
  if (chart && renderKey === lastRenderKey) return
  if (!chart) {
    chart = init(chartEl.value)
    bindChartEvents()
  }

  const colors = palette.value
  const theme = chartTheme.value
  chart.setOption({
    animationDuration: 420,
    animationDurationUpdate: 320,
    grid: {
      top: 18,
      right: 16,
      bottom: 20,
      left: 16,
      outerBoundsMode: 'same',
      outerBoundsContain: 'axisLabel'
    },
    brush: props.zoomable
      ? {
          xAxisIndex: 0,
          brushType: 'lineX',
          brushMode: 'single',
          throttleType: 'debounce',
          throttleDelay: 80,
          removeOnClick: true,
          brushStyle: {
            color: colors.top,
            borderColor: colors.line,
            borderWidth: 1
          }
        }
      : {},
    dataZoom: props.zoomable
      ? [
          {
            type: 'inside',
            xAxisIndex: 0,
            filterMode: 'filter',
            zoomOnMouseWheel: 'ctrl',
            moveOnMouseMove: true,
            moveOnMouseWheel: true,
            preventDefaultMouseMove: true
          }
        ]
      : [],
    tooltip: {
      trigger: 'axis',
      axisPointer: {
        type: 'line',
        snap: true
      },
      borderWidth: 0,
      backgroundColor: theme.tooltipBg,
      textStyle: {
        color: theme.tooltipText,
        fontSize: 12
      },
      extraCssText: `box-shadow: ${theme.tooltipShadow}; border-radius: 14px; padding: 10px 12px;`,
      formatter: (params: any) => {
        const list = Array.isArray(params) ? params : [params]
        if (hasTokenBreakdown.value) {
          const up = list.find((entry: any) => entry?.seriesName === 'Tokens up')
          const down = list.find((entry: any) => entry?.seriesName === 'Tokens down')
          const upValue = tooltipValue(up)
          const downValue = tooltipValue(down)
          const axisValue = list[0]?.axisValue || ''
          if (upValue == null && downValue == null) {
            return `${axisValue}<br/><span style="color:rgba(148,163,184,0.9);font-weight:600">No data yet</span>`
          }
          const normalizedUp = upValue || 0
          const normalizedDown = downValue || 0
          return [
            axisValue,
            `<span style="color:#f59e0b;font-weight:600">Tokens up&nbsp;&nbsp;${formatMetricValue(normalizedUp)}</span>`,
            `<span style="color:#38bdf8;font-weight:600">Tokens down&nbsp;&nbsp;${formatMetricValue(normalizedDown)}</span>`,
            `<span style="color:${theme.tooltipText};font-weight:700">Total&nbsp;&nbsp;${formatMetricValue(normalizedUp + normalizedDown)}</span>`
          ].join('<br/>')
        }
        const point = list.find((entry: any) => tooltipValue(entry) != null) || list[0]
        const value = tooltipValue(point)
        if (value == null) {
          return `${point.axisValue}<br/><span style="color:rgba(148,163,184,0.9);font-weight:600">No data yet</span>`
        }
        return `${point.axisValue}<br/><span style="color:${colors.line};font-weight:600">${formatMetricValue(value)}</span>`
      }
    },
    xAxis: {
      type: 'category',
      boundaryGap: false,
      data: props.points.map(point => point.label),
      axisLine: {
        lineStyle: {
          color: theme.axisLine
        }
      },
      axisTick: {
        show: false
      },
      axisLabel: {
        color: theme.axisLabel,
        fontSize: 11,
        fontWeight: 600,
        letterSpacing: 1.2
      }
    },
    yAxis: {
      type: 'value',
      splitNumber: 3,
      axisLine: {
        show: false
      },
      axisTick: {
        show: false
      },
      axisLabel: {
        color: theme.yAxisLabel,
        fontSize: 11,
        formatter: (value: number) => formatMetricValue(value)
      },
      splitLine: {
        lineStyle: {
          color: theme.splitLine
        }
      }
    },
    series: [
      ...(hasTokenBreakdown.value
        ? [
            {
              name: 'Tokens down',
              type: 'line',
              stack: 'tokens',
              smooth: false,
              showSymbol: false,
              connectNulls: false,
              data: props.points.map(point => point.tokensDown),
              lineStyle: {
                width: 2.5,
                color: '#38bdf8',
                shadowColor: 'rgba(56, 189, 248, 0.2)',
                shadowBlur: 14
              },
              itemStyle: { color: '#38bdf8' },
              areaStyle: {
                color: new graphic.LinearGradient(0, 0, 0, 1, [
                  { offset: 0, color: 'rgba(56, 189, 248, 0.4)' },
                  { offset: 1, color: 'rgba(14, 165, 233, 0.06)' }
                ])
              },
              emphasis: { focus: 'series', scale: false }
            },
            {
              name: 'Tokens up',
              type: 'line',
              stack: 'tokens',
              smooth: false,
              showSymbol: false,
              connectNulls: false,
              data: props.points.map(point => point.tokensUp),
              lineStyle: {
                width: 2.5,
                color: '#f59e0b',
                shadowColor: 'rgba(245, 158, 11, 0.2)',
                shadowBlur: 14
              },
              itemStyle: { color: '#f59e0b' },
              areaStyle: {
                color: new graphic.LinearGradient(0, 0, 0, 1, [
                  { offset: 0, color: 'rgba(251, 191, 36, 0.4)' },
                  { offset: 1, color: 'rgba(245, 158, 11, 0.06)' }
                ])
              },
              emphasis: { focus: 'series', scale: false }
            }
          ]
        : [
            {
              type: 'line',
              smooth: false,
              showSymbol: true,
              symbol: 'circle',
              symbolSize: 0,
              connectNulls: false,
              data: props.points.map(point => chartPointData(point)),
              lineStyle: {
                width: 3,
                color: colors.line,
                shadowColor: colors.glow,
                shadowBlur: 18
              },
              emphasis: {
                scale: false
              },
              areaStyle: {
                color: new graphic.LinearGradient(0, 0, 0, 1, [
                  { offset: 0, color: colors.top },
                  { offset: 1, color: colors.bottom }
                ])
              }
            }
          ]),
      {
        type: 'effectScatter',
        coordinateSystem: 'cartesian2d',
        z: 5,
        data: highlightedScatterData(),
        symbolSize: 13,
        itemStyle: {
          color: theme.highlightFill,
          borderColor: colors.line,
          borderWidth: 3,
          shadowColor: theme.highlightShadow,
          shadowBlur: 18
        },
        rippleEffect: {
          color: theme.highlightRipple,
          scale: 2.8,
          period: 3.6,
          brushType: 'stroke'
        },
        emphasis: {
          scale: false
        },
        tooltip: {
          show: false
        }
      }
    ]
  }, { replaceMerge: ['series'] })
  lastRenderKey = renderKey
  enableDefaultBrushZoom()
}

function resizeChart() {
  chart?.resize()
}

onMounted(async () => {
  await nextTick()
  renderChart()
  if (chartEl.value && typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(resizeChart)
    resizeObserver.observe(chartEl.value)
  }
  if (typeof window !== 'undefined') {
    window.addEventListener('resize', resizeChart)
  }
})

watch(() => [props.metric, props.window, props.points, props.title, props.subtitle, props.badgeLabel, props.zoomable, isDark.value], () => {
  renderChart()
}, { deep: true })

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  resizeObserver = null
  if (typeof window !== 'undefined') {
    window.removeEventListener('resize', resizeChart)
  }
  if (chartEventsBound) {
    chart?.off('brushEnd', applyBrushZoom as any)
    chartEventsBound = false
  }
  chart?.dispose()
  chart = null
  lastRenderKey = ''
})

defineExpose({
  resetZoom
})
</script>

<template>
  <div :class="[props.surface !== false ? 'app-subsurface' : '', 'overflow-hidden px-4 py-4 md:px-5']">
    <div class="flex items-center justify-between gap-3">
      <div>
        <p v-if="props.title !== ''" class="app-subtle-text text-xs font-semibold uppercase tracking-[0.22em]">{{ props.title || 'Usage trend' }}</p>
        <p class="app-muted-text mt-1 text-sm">{{ props.subtitle || 'Rolling global totals across the current dashboard windows.' }}</p>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <div v-if="hasTokenBreakdown" class="flex items-center gap-3 text-xs font-semibold">
          <span class="inline-flex items-center gap-1.5 text-amber-500"><span class="h-2 w-2 rounded-full bg-amber-500" />Tokens up</span>
          <span class="inline-flex items-center gap-1.5 text-sky-500"><span class="h-2 w-2 rounded-full bg-sky-400" />Tokens down</span>
        </div>
        <UiButton v-if="hasActiveZoom" class="usage-reset-zoom-button" tone="ghost" size="xs" @click="resetZoom">
          <svg class="mr-1.5 h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" aria-hidden="true">
            <path d="M4.5 5.5v5h5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            <path d="M5.2 10.2a7.5 7.5 0 1 0 2.1-4.9L4.5 8" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          Reset Zoom
        </UiButton>
        <UiBadge tone="slate">{{ props.badgeLabel || `${props.metric} · ${props.window}` }}</UiBadge>
      </div>
    </div>
    <div ref="chartEl" class="mt-4 h-[260px] w-full" />
  </div>
</template>

<style scoped>
.usage-reset-zoom-button {
  border-color: color-mix(in srgb, #0284c7 54%, var(--app-border));
  background: var(--app-secondary-bg);
  color: color-mix(in srgb, #075985 86%, var(--app-heading));
  box-shadow: none;
}

:global(:root[data-site-theme='dark']) .usage-reset-zoom-button {
  border-color: color-mix(in srgb, #38bdf8 58%, var(--app-border));
  background: var(--app-secondary-bg);
  color: #dff8ff;
  box-shadow: none;
}

.usage-reset-zoom-button:hover {
  border-color: color-mix(in srgb, #0284c7 76%, var(--app-border));
  transform: translateY(-1px);
}
</style>
