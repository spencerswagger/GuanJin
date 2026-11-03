import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 产物 go:embed 到 guanjind 同端口（技术栈 §2.4）；开发期用代理指向本地后端。
export default defineConfig({
  plugins: [vue()],
  build: { outDir: 'dist', emptyOutDir: true },
  server: { proxy: { '/api': 'http://127.0.0.1:8080', '/auth': 'http://127.0.0.1:8080' } }
})
