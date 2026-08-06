<script setup>
import { computed, ref, watch } from 'vue'

// A table that always shows one page worth of rows and its own pager, so a
// growing list never turns the page into a long scroll. Column definitions
// are passed through unchanged via the default slot.
const props = defineProps({
  rows: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  pageSize: { type: Number, default: 10 },
  rowKey: { type: String, default: 'id' },
  emptyText: { type: String, default: '暂无数据' },
})

const page = ref(1)
const size = ref(props.pageSize)
const total = computed(() => props.rows.length)
const pageRows = computed(() => props.rows.slice((page.value - 1) * size.value, page.value * size.value))

// Filtering or deleting can leave the current page past the end of the list.
watch(total, () => {
  const lastPage = Math.max(1, Math.ceil(total.value / size.value))
  if (page.value > lastPage) page.value = lastPage
})
</script>

<template>
  <div class="paged-table">
    <div class="paged-table__body">
      <el-table v-loading="props.loading" :data="pageRows" :row-key="props.rowKey" :empty-text="props.emptyText">
        <slot />
      </el-table>
    </div>
    <div class="pagination-bar">
      <el-pagination
        v-model:current-page="page"
        v-model:page-size="size"
        :total="total"
        :page-sizes="[10, 20, 50, 100]"
        layout="total, sizes, prev, pager, next"
        background
      />
    </div>
  </div>
</template>

<style scoped>
/* The rows scroll inside their own area so the pager stays reachable no
   matter how tall a page of rows turns out to be — a wrapped cell or a
   larger page size used to push it off the bottom of the screen with no way
   to scroll to it. */
.paged-table { display: flex; flex-direction: column; min-height: 0; }
.paged-table__body { flex: 1 1 auto; min-height: 0; overflow: auto; }
.pagination-bar { flex: 0 0 auto; }
</style>
