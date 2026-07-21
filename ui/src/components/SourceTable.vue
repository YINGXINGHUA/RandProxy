<script setup lang="ts">
import type { SourceInfo } from '../types'
import { computed } from 'vue'

const props = defineProps<{
  sources: SourceInfo[]
}>()

const sortedSources = computed(() =>
  [...props.sources].sort((a, b) => a.name.localeCompare(b.name, 'zh-CN'))
)

function statusEmoji(s: string): string {
  if (s === 'online') return '🟢'
  if (s === 'offline') return '🔴'
  return '🟣'
}
</script>

<template>
  <div class="source-table">
    <h3 class="table-title">数据源状态</h3>
    <table>
      <thead>
        <tr>
          <th>名称</th>
          <th>状态</th>
          <th>拉取数</th>
          <th>通过 / 失败</th>
          <th>存活率</th>
          <th>就绪数</th>
          <th>最后拉取</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="src in sortedSources" :key="src.name">
          <td class="name-cell">{{ src.name }}</td>
          <td>{{ statusEmoji(src.status) }}</td>
          <td>{{ src.total_fetched }}</td>
          <td>
            <span class="pass">{{ src.validated }}</span>
            <span class="sep">/</span>
            <span class="fail">{{ src.validation_failed }}</span>
          </td>
          <td>
            <span :class="['rate', { 'rate--low': src.pass_rate < 5 }]">
              {{ src.pass_rate.toFixed(1) }}%
            </span>
          </td>
          <td>{{ src.in_ready }}</td>
          <td class="time-cell">{{ src.last_fetch }}</td>
        </tr>
        <tr v-if="sortedSources.length === 0">
          <td colspan="7" class="empty">暂无数据源</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.source-table {
  background: #ffffff;
  border-radius: 12px;
  padding: 20px 24px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06), 0 1px 2px rgba(0, 0, 0, 0.04);
  overflow-x: auto;
}

.table-title {
  font-size: 15px;
  font-weight: 600;
  margin: 0 0 16px 0;
  color: #374151;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

thead th {
  text-align: left;
  padding: 10px 12px;
  color: #6b7280;
  font-weight: 600;
  border-bottom: 2px solid #e5e7eb;
  white-space: nowrap;
}

tbody td {
  padding: 10px 12px;
  border-bottom: 1px solid #f3f4f6;
  color: #374151;
}

tbody tr:hover {
  background: #f9fafb;
}

.name-cell {
  font-weight: 600;
  color: #1a1a2e;
}

.pass { color: #22c55e; font-weight: 600; }
.sep { color: #d1d5db; margin: 0 4px; }
.fail { color: #ef4444; font-weight: 600; }

.rate {
  font-weight: 600;
  color: #2563eb;
}

.rate--low {
  color: #ef4444;
}

.time-cell {
  color: #9ca3af;
  white-space: nowrap;
  font-size: 12px;
}

.empty {
  text-align: center;
  color: #9ca3af;
  padding: 24px !important;
}
</style>
