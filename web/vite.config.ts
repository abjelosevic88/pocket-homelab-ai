import { defineConfig, type Plugin } from 'vite'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// PAI_API points the dev server at another backend (e.g. PAI_API=http://127.0.0.1:18321 npx vite).
const api = process.env.PAI_API || 'http://localhost:8080'

// Vite empties dist/ on every build; the repo keeps the folder through dist/.gitkeep.
const keep: Plugin = { name: 'keep-gitkeep', closeBundle() { writeFileSync(fileURLToPath(new URL('./dist/.gitkeep', import.meta.url)), '') } }

export default defineConfig({
  plugins: [react(), keep],
  build: { chunkSizeWarningLimit: 1500 },
  server: {
    proxy: { '/api': api },
  },
})
