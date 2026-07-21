<script setup lang="ts">
import type { RequestRecord } from '../types'

defineProps<{
  requests: RequestRecord[]
}>()
</script>

<template>
  <div class="recent-requests" v-if="requests.length">
    <h3 class="table-title">最近请求</h3>
    <div class="scroll-table">
      <table>
        <thead>
          <tr>
            <th>时间</th>
            <th>目标</th>
            <th>代理IP</th>
            <th>延迟 (ms)</th>
            <th>状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(req, i) in requests" :key="i">
            <td class="time-cell">{{ req.time }}</td>
            <td class="target-cell" :title="req.target">{{ req.target }}</td>
            <td>{{ req.proxy_ip }}</td>
            <td class="latency-cell">{{ req.latency_ms }}</td>
            <td class="status-cell">
              <span :class="req.success ? 'ok' : 'ko'">
                {{ req.success ? '✓' : '✗' }}
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.recent-requests {
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

.scroll-table {
  max-height: 300px;
  overflow-y: auto;
  border-radius: 8px;
  border: 1px solid #f3f4f6;
}

.scroll-table::-webkit-scrollbar {
  width: 6px;
}

.scroll-table::-webkit-scrollbar-track {
  background: #f9fafb;
}

.scroll-table::-webkit-scrollbar-thumb {
  background: #d1d5db;
  border-radius: 3px;
}

table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

thead th {
  position: sticky;
  top: 0;
  text-align: left;
  padding: 10px 12px;
  color: #6b7280;
  font-weight: 600;
  background: #f9fafb;
  border-bottom: 2px solid #e5e7eb;
  white-space: nowrap;
  z-index: 1;
}

tbody td {
  padding: 8px 12px;
  border-bottom: 1px solid #f3f4f6;
  color: #374151;
}

tbody tr:hover {
  background: #f9fafb;
}

.time-cell {
  color: #9ca3af;
  white-space: nowrap;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.target-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.latency-cell {
  font-variant-numeric: tabular-nums;
  font-weight: 500;
  text-align: right;
}

.status-cell {
  text-align: center;
  font-size: 16px;
}

.ok { color: #22c55e; font-weight: 700; }
.ko { color: #ef4444; font-weight: 700; }
</style>
