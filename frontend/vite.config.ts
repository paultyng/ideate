import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    // Target the latest WKWebView (Safari 15+, the engine Wails uses on
    // macOS). Skips esbuild's lowering pass for modern operators like
    // `||=`, which esbuild 0.25.9+ miscompiles inside xterm.js's
    // `requestMode` handler — the lowered output references an
    // undeclared `n` and throws `ReferenceError: Can't find variable: n`
    // when Claude Code emits a DECRQM escape sequence on session resume,
    // freezing the terminal until the React component remounts.
    // See evanw/esbuild#4297; revisit when esbuild ships a fix.
    target: 'esnext',
  },
})
