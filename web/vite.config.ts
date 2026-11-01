import { svelte } from '@sveltejs/vite-plugin-svelte'
import { defineConfig } from 'vite'

// The production build is embedded into the controller binary
// (internal/controller/webui), which serves it at "/".
export default defineConfig({
  plugins: [svelte()],
  base: './',
  build: {
    outDir: '../internal/controller/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 900,
  },
  server: {
    // During `npm run dev`, forward API calls to a local controller.
    proxy: { '/api': 'http://127.0.0.1:8080', '/readyz': 'http://127.0.0.1:8080' },
  },
})
