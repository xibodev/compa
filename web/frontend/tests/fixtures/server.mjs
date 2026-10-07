import { mkdtemp, readFile, writeFile } from "node:fs/promises"
import { createServer } from "node:http"
import { tmpdir } from "node:os"
import { extname, join } from "node:path"

const root = new URL("../../dist/", import.meta.url).pathname.replace(
  /^\/(.:\/)/,
  "$1",
)
const initialState = JSON.parse(
  await readFile(new URL("./management.json", import.meta.url), "utf8"),
)
const state = structuredClone(initialState)
const stateDir = await mkdtemp(join(tmpdir(), "compa-ui-fixture-"))
const statePath = join(stateDir, "management.json")
const persist = () => writeFile(statePath, JSON.stringify(state, null, 2))
await persist()
const json = (res, body) => {
  res.writeHead(200, { "content-type": "application/json" })
  res.end(JSON.stringify(body))
}
const reject = (res, message) => {
  res.writeHead(400, { "content-type": "text/plain; charset=utf-8" })
  res.end(`${message}\n`)
}
const readBody = async (req) => {
  let body = ""
  for await (const chunk of req) body += chunk
  return body
}
createServer(async (req, res) => {
  const url = new URL(req.url ?? "/", "http://fixture")
  const { pathname } = url
  if (pathname === "/__fixture/reset" && req.method === "POST") {
    Object.assign(state, structuredClone(initialState))
    await persist()
    return json(res, { status: "ok" })
  }
  if (pathname === "/api/auth/status")
    return json(res, { authenticated: true, initialized: true })
  if (pathname === "/api/gateway/status")
    return json(res, { gateway_status: "stopped", gateway_start_allowed: true })
  if (pathname === "/api/channels/catalog")
    return json(res, {
      channels: [
        { name: "telegram", config_key: "telegram" },
        { name: "web", config_key: "web" },
      ],
    })
  if (pathname === "/api/config" && req.method === "GET")
    return json(res, { channel_list: { web: { enabled: true } } })
  if (pathname === "/api/extension")
    return json(res, {
      has_secret: false,
      status: "not_configured",
      providers: [],
    })
  if (
    pathname === "/api/provider-instances/auto-connect-free" &&
    req.method === "POST"
  )
    return json(res, state.free_test)
  if (pathname === "/api/provider-instances" && req.method === "GET")
    return json(res, { instances: state.instances })
  if (pathname === "/api/provider-targets")
    return json(res, {
      targets:
        url.searchParams.get("all") === "true"
          ? state.targets
          : state.targets.filter((target) =>
              state.active_models.includes(target.target),
            ),
    })
  if (pathname === "/api/active-models")
    return json(res, {
      active_models: state.active_models,
      total: state.active_models.length,
    })
  if (pathname === "/api/provider-instances/catalogs")
    return json(res, { catalogs: state.catalogs })
  if (pathname === "/api/model-routes" && req.method === "GET")
    return json(res, { routes: state.routes })
  if (pathname === "/api/provider-roster")
    return json(res, {
      providers: state.providers,
    })
  if (pathname === "/api/default-model") {
    if (req.method === "PUT") {
      const selection = String(JSON.parse(await readBody(req)).selection ?? "")
      const selectable =
        selection === "" ||
        state.targets.some((target) => target.target === selection) ||
        state.routes.some((route) => route.name === selection)
      if (!selectable)
        return reject(res, `selection "${selection}" does not resolve`)
      state.default_model = selection
      await persist()
    }
    return json(res, { selection: state.default_model })
  }
  if (pathname.endsWith("/catalog/sync")) {
    if ((await readBody(req)) !== "{}") {
      res.writeHead(400)
      return res.end("sync body must be empty")
    }
    return json(res, { models: [] })
  }
  if (pathname.startsWith("/api/provider-instances/") && req.method === "PUT") {
    const update = JSON.parse(await readBody(req))
    const current = state.instances.find((item) => item.id === update.id)
    if (!current) return reject(res, "provider instance not found")
    current.endpoint = update.endpoint
    current.state = update.state
    if (update.runtime && Object.keys(update.runtime).length > 0)
      current.runtime = update.runtime
    else delete current.runtime
    await persist()
    return json(res, { status: "ok", instance: current })
  }
  if (pathname.startsWith("/api/model-routes/")) {
    if (req.method === "PUT") {
      const route = JSON.parse(await readBody(req))
      state.routes = state.routes.map((item) =>
        item.name === route.name ? route : item,
      )
      await persist()
    }
    return json(res, { status: "ok" })
  }
  const path =
    pathname === "/" || pathname === "/models"
      ? "index.html"
      : pathname.slice(1)
  try {
    const data = await readFile(join(root, path))
    const types = {
      ".html": "text/html",
      ".js": "text/javascript",
      ".css": "text/css",
      ".svg": "image/svg+xml",
      ".png": "image/png",
      ".ico": "image/x-icon",
      ".woff2": "font/woff2",
    }
    res.writeHead(200, {
      "content-type": types[extname(path)] || "application/octet-stream",
    })
    res.end(data)
  } catch {
    const data = await readFile(join(root, "index.html"))
    res.writeHead(200, { "content-type": "text/html" })
    res.end(data)
  }
}).listen(4178, "127.0.0.1")
