<script lang="ts">
  // ECharts line chart of latency (median and p95) with failure markers.
  // Only the modules used are imported, keeping the bundle small.
  import * as echarts from 'echarts/core'
  import { LineChart, BarChart } from 'echarts/charts'
  import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
  import { CanvasRenderer } from 'echarts/renderers'
  import type { SeriesPoint } from './api'

  echarts.use([LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])

  let { points, unit = 'ms' }: { points: SeriesPoint[]; unit?: string } = $props()
  let el: HTMLDivElement

  function css(name: string) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
  }

  $effect(() => {
    const chart = echarts.init(el, undefined, { renderer: 'canvas' })
    const ro = new ResizeObserver(() => chart.resize())
    ro.observe(el)
    const ink = css('--muted'), line = css('--line'), accent = css('--accent'), bad = css('--bad')
    const t = points.map((p) => new Date(p.t).getTime())
    chart.setOption({
      animation: false,
      grid: { left: 48, right: 48, top: 48, bottom: 28 },
      legend: { top: 0, left: 0, textStyle: { color: ink }, itemWidth: 14, itemHeight: 8 },
      tooltip: { trigger: 'axis', valueFormatter: (v: unknown) => (v == null ? '—' : `${Number(v).toFixed(1)}`) },
      xAxis: { type: 'time', axisLine: { lineStyle: { color: line } }, axisLabel: { color: ink, hideOverlap: true } },
      yAxis: [
        { type: 'value', name: unit, nameTextStyle: { color: ink }, axisLabel: { color: ink }, splitLine: { lineStyle: { color: line } } },
        { type: 'value', name: 'fail %', max: 100, nameTextStyle: { color: ink }, axisLabel: { color: ink }, splitLine: { show: false } },
      ],
      series: [
        { name: 'median', type: 'line', showSymbol: false, data: points.map((p, i) => [t[i], p.latency_ms]), lineStyle: { color: accent, width: 2 }, itemStyle: { color: accent } },
        { name: 'p95', type: 'line', showSymbol: false, data: points.map((p, i) => [t[i], p.latency_p95_ms]), lineStyle: { color: accent, width: 1, type: 'dashed', opacity: 0.6 }, itemStyle: { color: accent } },
        { name: 'failures', type: 'bar', yAxisIndex: 1, barMaxWidth: 6, data: points.map((p, i) => [t[i], p.n ? (100 * p.fail) / p.n : 0]), itemStyle: { color: bad, opacity: 0.7 } },
      ],
    })
    return () => {
      ro.disconnect()
      chart.dispose()
    }
  })
</script>

<div bind:this={el} class="chart" role="img" aria-label="Latency and failures over time"></div>

<style>
  .chart { width: 100%; height: 260px; }
</style>
