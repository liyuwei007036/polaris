<template>
  <div class="meter-box" :class="className">
    <div class="meter-head">
      <span class="meter-label">
        <slot name="icon">
          <component :is="icon" v-if="icon" class="meter-icon" />
        </slot>
        <span class="meter-title truncate">{{ label }}</span>
      </span>
      <span class="meter-val font-mono" :class="{ 'is-danger': danger }">
        {{ displayPercent }}
      </span>
    </div>
    <div class="meter-track neu-inset">
      <div
        class="meter-bar"
        :class="danger ? 'bar-danger' : barToneClass"
        :style="{ width: `${filled}%` }"
      />
    </div>
    <div class="meter-foot font-mono truncate">
      {{ foot }}
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  label: {
    type: String,
    required: true,
  },
  icon: {
    type: Object,
    default: null,
  },
  pct: {
    type: Number,
    default: null,
  },
  foot: {
    type: String,
    default: '',
  },
  empty: {
    type: String,
    default: '—',
  },
  tone: {
    type: String,
    default: 'cpu', // 'cpu' | 'mem' | 'disk' | 'net'
  },
  className: {
    type: String,
    default: '',
  },
})

const filled = computed(() => {
  if (props.pct === null || props.pct === undefined || isNaN(props.pct)) return 0
  return Math.min(100, Math.max(0, props.pct))
})

const danger = computed(() => {
  return props.pct !== null && props.pct !== undefined && filled.value >= 90
})

const displayPercent = computed(() => {
  if (props.pct === null || props.pct === undefined || isNaN(props.pct)) {
    return props.empty
  }
  const f = filled.value
  return `${f < 10 ? f.toFixed(1) : f.toFixed(0)}%`
})

const barToneClass = computed(() => {
  switch (props.tone) {
    case 'cpu': return 'bar-cpu'
    case 'mem': return 'bar-mem'
    case 'disk': return 'bar-disk'
    case 'net': return 'bar-net'
    default: return 'bar-cpu'
  }
})
</script>

<style scoped>
.meter-box {
  min-width: 0;
}

.meter-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
}

.meter-label {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--sb-muted);
}

.meter-icon {
  width: 12px;
  height: 12px;
  flex-shrink: 0;
}

.meter-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.meter-val {
  font-size: 11px;
  font-weight: 600;
  color: var(--sb-text);
  white-space: nowrap;
}

.meter-val.is-danger {
  color: var(--sb-danger, #ef4444);
}

.meter-track {
  margin-top: 6px;
  height: 6px;
  width: 100%;
  overflow: hidden;
  border-radius: 999px;
  background: var(--neu-inset-bg, rgba(15, 23, 42, 0.45));
}

.meter-bar {
  height: 100%;
  border-radius: 999px;
  transition: width 0.6s ease;
}

.meter-foot {
  margin-top: 5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--sb-muted);
}
</style>
