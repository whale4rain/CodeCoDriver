import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { '/dashboard': 'http://localhost:8080', '/tasks': 'http://localhost:8080', '/memory': 'http://localhost:8080', '/repositories': 'http://localhost:8080', '/evaluations': 'http://localhost:8080', '/human-reviews': 'http://localhost:8080', '/skills': 'http://localhost:8080' },
  },
})
