import fs from "node:fs"
import path from "node:path"

import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import react from "@vitejs/plugin-react"
import { type Plugin, defineConfig } from "vite"

// cssPackageLicenses writes .vite/css-licenses.json, in the shape of Vite's
// .vite/license.json (name, version, identifier, text): the packages the
// stylesheets under src pull in with @import or @plugin, such as Tailwind and
// the Inter font. They are not bundled as JavaScript, so Vite's list leaves
// them out.
function cssPackageLicenses(): Plugin {
  const root = import.meta.dirname
  return {
    name: "compa:css-package-licenses",
    apply: "build",
    generateBundle() {
      const names = new Set<string>()
      const src = path.join(root, "src")
      for (const file of fs.readdirSync(src, { recursive: true })) {
        if (typeof file !== "string" || !file.endsWith(".css")) continue
        const css = fs.readFileSync(path.join(src, file), "utf-8")
        // A bare specifier names a package; relative paths and URLs don't.
        for (const [, specifier] of css.matchAll(
          /^\s*@(?:import|plugin)\s+["']([^"'./:][^"':]*)["']/gm,
        )) {
          const parts = specifier.split("/")
          names.add(parts.slice(0, specifier.startsWith("@") ? 2 : 1).join("/"))
        }
      }
      const packages = [...names].sort().map((name) => {
        const dir = path.join(root, "node_modules", name)
        const pkg = JSON.parse(
          fs.readFileSync(path.join(dir, "package.json"), "utf-8"),
        ) as { name: string; version: string; license?: unknown }
        const licenseFile = fs
          .readdirSync(dir)
          .find((file) => /^(license|licence|copying)/i.test(file))
        return {
          name: pkg.name,
          version: pkg.version,
          identifier:
            typeof pkg.license === "string" ? pkg.license.trim() : undefined,
          text: licenseFile
            ? fs.readFileSync(path.join(dir, licenseFile), "utf-8").trim()
            : undefined,
        }
      })
      this.emitFile({
        type: "asset",
        fileName: ".vite/css-licenses.json",
        source: JSON.stringify(packages, null, 2),
      })
    },
  }
}

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
    cssPackageLicenses(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  build: {
    // .vite/license.json lists the npm packages bundled into the JavaScript,
    // with their license texts. With .vite/css-licenses.json, it is what a
    // release's THIRD_PARTY_NOTICES lists for the web UI (cmd/notices).
    license: { fileName: ".vite/license.json" },
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
