import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  base: '/',
  build: {
    outDir: fileURLToPath(new URL('../internal/control/web/dist', import.meta.url)),
    emptyOutDir: true,
    assetsDir: 'assets',
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    proxy: {
      '/api': 'http://192.168.31.40:8080',
    },
  },
})
