import { computed, ref } from 'vue'

const THEME_STORAGE_KEY = 'polaris_theme'

function getSavedTheme() {
  const saved = localStorage.getItem(THEME_STORAGE_KEY)
  if (saved === 'dark' || saved === 'light') {
    return saved
  }
  return 'dark'
}

export const themeMode = ref(getSavedTheme())
export const isDark = computed(() => themeMode.value === 'dark')

export function applyTheme(mode) {
  themeMode.value = mode
  localStorage.setItem(THEME_STORAGE_KEY, mode)
  if (typeof document !== 'undefined') {
    const root = document.documentElement
    if (mode === 'dark') {
      root.classList.add('dark')
      root.classList.remove('light')
    } else {
      root.classList.remove('dark')
      root.classList.add('light')
    }
    // Clean up any legacy style classes
    root.classList.remove('style-admin')
    root.classList.add('style-openlat')
    window.dispatchEvent(new CustomEvent('polaris:theme-change', { detail: { theme: mode } }))
  }
}

export function toggleTheme() {
  applyTheme(themeMode.value === 'dark' ? 'light' : 'dark')
}

// Initial sync
if (typeof window !== 'undefined') {
  window.__toggleTheme = toggleTheme
  window.__applyTheme = applyTheme
  try {
    localStorage.removeItem('sb_ui_style')
  } catch {}
  applyTheme(themeMode.value)
}
