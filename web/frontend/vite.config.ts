import path from "path"

import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  server: {
    // The proxy keeps the browser's Host header: the launcher compares it
    // with Origin (first-run password setup) and checks it against the names
    // it serves, so rewriting it to the target would fail both checks.
    proxy: {
      "/api": {
        target: "http://localhost:18800",
      },
      "/web/media": {
        target: "http://localhost:18800",
      },
      "/web/ws": {
        target: "ws://localhost:18800",
        ws: true,
      },
    },
  },
})
