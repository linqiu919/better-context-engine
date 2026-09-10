import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    // 相对项目根（ui/）解析，避免引入 Node API（CI 无 @types/node 会挂 tsc）
    outDir: '../web/dist',
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    // skills/bce/SKILL.md 位于仓库根（ui/ 之外），SkillPage 以 ?raw 导入作为唯一
    // 来源；dev server 默认只放行 ui/，需把上级目录加入白名单（build 不受限）
    fs: { allow: ['..'] },
    proxy: {
      '/api': 'http://localhost:18181',
      '/health': 'http://localhost:18181',
    },
  },
})
