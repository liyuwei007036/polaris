<template>
  <div class="openlat-toolbar neu glass">
    <!-- Search Box -->
    <div class="search-box-wrap">
      <svg class="search-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <circle cx="11" cy="11" r="8" />
        <line x1="21" y1="21" x2="16.65" y2="16.65" />
      </svg>
      <input
        type="text"
        :value="query"
        placeholder="搜索名称、地区、系统、CPU…"
        class="neu-track search-input"
        @input="$emit('update:query', $event.target.value)"
      />
      <button
        v-if="query"
        type="button"
        class="clear-btn"
        title="清空"
        @click="$emit('update:query', '')"
      >
        &times;
      </button>
    </div>

    <div class="toolbar-spacer" />

    <!-- Controls right -->
    <div class="controls-right">
      <!-- Group Select -->
      <select
        :value="group"
        class="neu-track select-ctrl"
        @change="$emit('update:group', $event.target.value)"
      >
        <option value="none">不分组</option>
        <option value="country">按地区</option>
        <option value="status">按状态</option>
        <option value="os">按系统</option>
      </select>

      <!-- Sort Select -->
      <select
        :value="sort"
        class="neu-track select-ctrl"
        @change="$emit('update:sort', $event.target.value)"
      >
        <option value="default">默认排序</option>
        <option value="name">按名称</option>
        <option value="cpu">按 CPU</option>
        <option value="mem">按内存</option>
        <option value="traffic">按流量</option>
        <option value="ping">按延迟</option>
      </select>

      <!-- View Switcher (Grid vs List) -->
      <div class="neu-inset view-switcher">
        <button
          type="button"
          class="view-btn"
          :class="{ 'is-active neu-sm': view === 'grid' }"
          title="卡片视图"
          @click="$emit('update:view', 'grid')"
        >
          <svg class="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="3" y="3" width="7" height="7" />
            <rect x="14" y="3" width="7" height="7" />
            <rect x="14" y="14" width="7" height="7" />
            <rect x="3" y="14" width="7" height="7" />
          </svg>
        </button>
        <button
          type="button"
          class="view-btn"
          :class="{ 'is-active neu-sm': view === 'list' }"
          title="列表视图"
          @click="$emit('update:view', 'list')"
        >
          <svg class="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <line x1="8" y1="6" x2="21" y2="6" />
            <line x1="8" y1="12" x2="21" y2="12" />
            <line x1="8" y1="18" x2="21" y2="18" />
            <line x1="3" y1="6" x2="3.01" y2="6" />
            <line x1="3" y1="12" x2="3.01" y2="12" />
            <line x1="3" y1="18" x2="3.01" y2="18" />
          </svg>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
defineProps({
  query: {
    type: String,
    default: '',
  },
  group: {
    type: String,
    default: 'none',
  },
  sort: {
    type: String,
    default: 'default',
  },
  view: {
    type: String,
    default: 'grid',
  },
})

defineEmits(['update:query', 'update:group', 'update:sort', 'update:view'])
</script>

<style scoped>
.openlat-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  border-radius: 16px;
  padding: 8px 12px;
}

.search-box-wrap {
  position: relative;
  width: 100%;
}

@media (min-width: 640px) {
  .search-box-wrap {
    width: 240px;
  }
}

.search-icon {
  position: absolute;
  top: 50%;
  left: 10px;
  transform: translateY(-50%);
  width: 14px;
  height: 14px;
  color: var(--sb-muted);
  pointer-events: none;
}

.search-input {
  width: 100%;
  height: 32px;
  padding-left: 32px;
  padding-right: 28px;
  border-radius: 8px;
  font-size: 12px;
  color: var(--sb-text);
  outline: none;
}

.search-input:focus {
  border-color: var(--sb-accent);
}

.clear-btn {
  position: absolute;
  top: 50%;
  right: 6px;
  transform: translateY(-50%);
  display: grid;
  place-items: center;
  width: 16px;
  height: 16px;
  border-radius: 4px;
  border: none;
  background: transparent;
  color: var(--sb-muted);
  font-size: 16px;
  cursor: pointer;
}

.clear-btn:hover {
  color: var(--sb-text);
}

.toolbar-spacer {
  display: none;
  flex: 1;
}

@media (min-width: 640px) {
  .toolbar-spacer {
    display: block;
  }
}

.controls-right {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

@media (min-width: 640px) {
  .controls-right {
    width: auto;
    justify-content: flex-start;
  }
}

.select-ctrl {
  height: 32px;
  padding: 0 10px;
  border-radius: 8px;
  font-size: 12px;
  color: var(--sb-text);
  outline: none;
  cursor: pointer;
}

.view-switcher {
  display: flex;
  align-items: center;
  gap: 2px;
  border-radius: 8px;
  padding: 2px;
}

.view-btn {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  border: none;
  background: transparent;
  color: var(--sb-muted);
  cursor: pointer;
  transition: all 0.15s ease;
}

.view-btn:hover {
  color: var(--sb-text);
}

.view-btn.is-active {
  color: var(--sb-accent);
}
</style>
