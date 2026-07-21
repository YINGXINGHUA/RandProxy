<script setup lang="ts">
import { ref, watch, onUnmounted, computed } from 'vue'
import { Chart, registerables } from 'chart.js'

Chart.register(...registerables)

const props = defineProps<{
  history: number[]
}>()

const canvasRef = ref<HTMLCanvasElement | null>(null)
let chart: Chart<'line'> | null = null

const chartData = computed(() => ({
  labels: props.history.map((_, i) => i),
  datasets: [
    {
      data: props.history,
      borderColor: '#3b82f6',
      borderWidth: 2,
      tension: 0.35,
      fill: true,
      backgroundColor: (ctx: any) => {
        const g = ctx.chart.ctx.createLinearGradient(0, 0, 0, 200)
        g.addColorStop(0, 'rgba(59, 130, 246, 0.25)')
        g.addColorStop(1, 'rgba(59, 130, 246, 0.02)')
        return g
      },
      pointRadius: 0,
      pointHitRadius: 0,
    },
  ],
}))

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  animation: { duration: 400 },
  plugins: { legend: { display: false } },
  scales: {
    x: { display: false },
    y: {
      beginAtZero: true,
      ticks: {
        stepSize: 1,
        font: { size: 11 },
        color: '#9ca3af',
      },
      grid: { color: '#f3f4f6' },
    },
  },
}))

function initChart() {
  if (!canvasRef.value || props.history.length < 2) return
  if (chart) chart.destroy()
  chart = new Chart(canvasRef.value, {
    type: 'line',
    data: chartData.value,
    options: chartOptions.value,
  })
}

watch(
  () => props.history,
  (val) => {
    if (val.length < 2) {
      if (chart) { chart.destroy(); chart = null }
      return
    }
    if (!chart) initChart()
    else {
      chart.data.datasets[0].data = val
      chart.update('none')
    }
  },
  { deep: true },
)

onUnmounted(() => {
  if (chart) { chart.destroy(); chart = null }
})
</script>

<template>
  <div class="ready-chart">
    <h3 class="chart-title">就绪代理趋势 (30min)</h3>
    <div class="chart-container">
      <canvas ref="canvasRef" />
    </div>
    <div v-if="history.length < 2" class="chart-empty">等待数据…</div>
  </div>
</template>

<style scoped>
.ready-chart {
  background: #ffffff;
  border-radius: 12px;
  padding: 20px 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06), 0 1px 2px rgba(0, 0, 0, 0.04);
}

.chart-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px 0;
  color: #374151;
}

.chart-container {
  position: relative;
  height: 200px;
}

.chart-empty {
  text-align: center;
  color: #9ca3af;
  font-size: 13px;
  padding: 40px 0;
}
</style>
