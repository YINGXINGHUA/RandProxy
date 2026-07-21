<script setup lang="ts">
import type { PoolStats, ServerStats } from '../types'
import { computed } from 'vue'

const props = defineProps<{
  pool: PoolStats
  server: ServerStats
}>()

const failRate = computed(() => {
  if (props.server.total_requests === 0) return 0
  return (props.server.fail_requests / props.server.total_requests * 100)
})
</script>

<template>
  <div class="cards">
    <div class="card card--blue">
      <div class="card-label">就绪代理</div>
      <div class="card-value">{{ pool.ready }}</div>
    </div>
    <div class="card card--orange">
      <div class="card-label">待验证</div>
      <div class="card-value">{{ pool.buffer }}</div>
    </div>
    <div class="card card--green">
      <div class="card-label">冷却中</div>
      <div class="card-value">{{ pool.blacklist }}</div>
    </div>
    <div class="card" :class="{ 'card--red': failRate >= 20 }">
      <div class="card-label">失败率</div>
      <div class="card-value">{{ failRate.toFixed(1) }}%</div>
      <div class="card-subtitle">{{ server.total_requests }} 总请求</div>
    </div>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}

.card {
  background: #ffffff;
  border-radius: 12px;
  padding: 20px 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06), 0 1px 2px rgba(0, 0, 0, 0.04);
  border-left: 4px solid #d1d5db;
  transition: box-shadow 0.2s;
}

.card:hover {
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}

.card--blue { border-left-color: #3b82f6; }
.card--orange { border-left-color: #f59e0b; }
.card--green { border-left-color: #22c55e; }
.card--red { border-left-color: #ef4444; }

.card-label {
  font-size: 13px;
  color: #6b7280;
  margin-bottom: 6px;
  font-weight: 500;
}

.card-value {
  font-size: 28px;
  font-weight: 700;
  color: #1a1a2e;
  line-height: 1.2;
}

.card-subtitle {
  font-size: 12px;
  color: #9ca3af;
  margin-top: 4px;
}

@media (max-width: 768px) {
  .cards {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
