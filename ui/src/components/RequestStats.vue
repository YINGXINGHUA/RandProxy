<script setup lang="ts">
import type { ServerStats } from '../types'

defineProps<{
  server: ServerStats
}>()
</script>

<template>
  <div class="request-stats">
    <h3 class="table-title">请求统计</h3>
    <div class="stats-grid">
      <div class="stat-item">
        <div class="stat-label">总请求数</div>
        <div class="stat-value">{{ server.total_requests }}</div>
      </div>
      <div class="stat-item stat-item--success">
        <div class="stat-label">成功</div>
        <div class="stat-value">{{ server.success_requests }}</div>
      </div>
      <div class="stat-item stat-item--fail">
        <div class="stat-label">失败</div>
        <div class="stat-value">{{ server.fail_requests }}</div>
      </div>
      <div class="stat-item">
        <div class="stat-label">平均延迟</div>
        <div class="stat-value">{{ server.avg_latency_ms.toFixed(1) }}ms</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.request-stats {
  background: #ffffff;
  border-radius: 12px;
  padding: 20px 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06), 0 1px 2px rgba(0, 0, 0, 0.04);
  margin-top: 20px;
}

.table-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px 0;
  color: #374151;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}

.stat-item {
  text-align: center;
  padding: 16px;
  background: #f9fafb;
  border-radius: 8px;
}

.stat-item--success {
  background: #f0fdf4;
}

.stat-item--fail {
  background: #fef2f2;
}

.stat-label {
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 6px;
  font-weight: 500;
}

.stat-value {
  font-size: 24px;
  font-weight: 700;
  color: #1a1a2e;
}

.stat-item--success .stat-value { color: #16a34a; }
.stat-item--fail .stat-value { color: #dc2626; }

@media (max-width: 768px) {
  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}
</style>
