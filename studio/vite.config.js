import { defineConfig } from 'vite'

// Output lands inside the Go module so internal/studio embeds one artifact
// shared by the browser shell (cmd/sos-studio) and the Wails desktop shell.
export default defineConfig({
  base: './',
  build: {
    outDir: '../internal/studio/webdist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false
  }
})
