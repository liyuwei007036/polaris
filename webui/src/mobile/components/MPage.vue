<script setup>
import { computed, getCurrentInstance, inject, useSlots } from 'vue'
import { ArrowLeft } from '@element-plus/icons-vue'

const props = defineProps({
  title: { type: String, default: '' },
  loading: { type: Boolean, default: false },
  back: { type: [Boolean, String], default: null },
  compact: { type: Boolean, default: false },
})

const emit = defineEmits(['back'])
const slots = useSlots()
const instance = getCurrentInstance()
const currentView = inject('currentView', null)
const goBack = inject('goBack', null)
const navigate = inject('navigate', null)

const primaryTabs = ['dashboard', 'connections', 'subscriptions', 'inbounds', 'more']

const showBack = computed(() => {
  if (props.back === false) return false
  if (props.back === true || typeof props.back === 'string' || instance?.vnode.props?.onBack) return true
  return Boolean(currentView?.value && !primaryTabs.includes(currentView.value))
})

function handleBack() {
  if (instance?.vnode.props?.onBack) {
    emit('back')
    return
  }
  if (typeof props.back === 'string' && navigate) {
    navigate(props.back)
  } else if (goBack) {
    goBack('more')
  } else if (window.history.length > 1) {
    window.history.back()
  }
}
</script>

<template>
  <section class="m-page" :class="{ 'has-fab': Boolean(slots.fab), 'has-header': Boolean(title || slots.header) }">
    <header v-if="title || slots.header" class="m-header">
      <slot name="header">
        <div class="m-header__left">
          <button
            v-if="showBack"
            type="button"
            class="m-header__back"
            aria-label="返回上一页"
            @click="handleBack"
          >
            <el-icon :size="19"><ArrowLeft /></el-icon>
          </button>
          <h1 class="m-header__title">{{ title }}</h1>
        </div>
        <div v-if="slots.actions" class="m-header__right">
          <slot name="actions" />
        </div>
      </slot>
    </header>

    <div v-loading="loading" class="m-page__body" :class="{ 'is-compact': compact }">
      <slot />
    </div>
    <slot name="fab" />
    <slot name="foot" />
  </section>
</template>

<style scoped>
.m-page {
  position: relative;
  height: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
  background: var(--sb-bg);
}
.m-page__body {
  flex: 1;
  min-height: 0;
  padding: 12px 14px 24px;
  overflow-y: auto;
  overflow-x: hidden;
  -webkit-overflow-scrolling: touch;
  scrollbar-width: none;
}
.m-page__body::-webkit-scrollbar {
  display: none;
}
.m-page__body.is-compact {
  padding: 8px 10px 20px;
}
.m-page.has-fab .m-page__body {
  padding-bottom: 88px;
}
</style>
