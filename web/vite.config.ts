import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vitest/config"

// The build goes straight into internal/ui/dist, which go:embed bundles into
// the binary. emptyOutDir stays off so the versioned placeholder (.keep)
// survives; scripts/clean.mjs removes the previous build instead.
export default defineConfig({
  base: "/ui/",
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { "@": path.resolve(import.meta.dirname, "src") },
  },
  build: {
    outDir: "../internal/ui/dist",
    emptyOutDir: false,
    // No inline scripts or data: modules: the CSP only allows 'self'.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    sourcemap: false,
  },
  server: {
    // npm run dev: the API of a local media-maid (LISTEN_ADDR :8080).
    proxy: {
      "/api": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: false,
  },
})
