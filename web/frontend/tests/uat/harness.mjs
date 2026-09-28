import { spawn } from "node:child_process"
import crypto from "node:crypto"
import fs from "node:fs/promises"
import http from "node:http"
import net from "node:net"
import os from "node:os"
import path from "node:path"
import { fileURLToPath } from "node:url"

const frontendDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "../..",
)
const repoRoot = path.resolve(frontendDir, "../..")
const executable = process.platform === "win32" ? ".exe" : ""
const runRoot = await fs.mkdtemp(path.join(os.tmpdir(), "compa-uat-"))
const home = path.join(runRoot, "home")
const workspace = path.join(home, "workspace")
const bin = path.join(runRoot, "bin")
const artifacts = path.join(runRoot, "artifacts")
await Promise.all(
  [home, workspace, bin, artifacts].map((dir) =>
    fs.mkdir(dir, { recursive: true }),
  ),
)

const reservePort = () =>
  new Promise((resolve, reject) => {
    const server = net.createServer()
    server.once("error", reject)
    server.listen(0, "127.0.0.1", () => {
      const address = server.address()
      const port = typeof address === "object" && address ? address.port : 0
      server.close(() => resolve(port))
    })
  })

const [
  launcherPort,
  gatewayPort,
  fakePort,
  daemonPort,
  stockDaemonPort,
  blackholePort,
] = await Promise.all([
  reservePort(),
  reservePort(),
  reservePort(),
  reservePort(),
  reservePort(),
  reservePort(),
])
const baseURL = `http://127.0.0.1:${launcherPort}`
const fakeURL = `http://127.0.0.1:${fakePort}`
const password = "compa-uat-password"
const observed = {
  chat: [],
  stt: 0,
  tts: [],
  failChat: false,
  upstream: [],
  daemonChat: [],
}

// An extension daemon to test against is optional, and described by a kit
// kept outside this repository: a directory with kit.json and main.go.
// kit.json names the daemon's checkout ("repo", read-only), its Go module
// ("module") and command package ("command"), environment prefixes its
// binary reads ("envPrefixes"), the fake upstream documents its keyless
// providers read ("upstream", by path under /__upstream/<host>) and the
// upstream path its keyless chat provider posts to ("chatPath"). main.go is
// a UAT entrypoint that registers the daemon's providers with every request
// rewritten to this harness's fake upstream. Without a kit only the
// full-stack journey runs.
const kitDir =
  process.env.COMPA_UAT_EXTENSION_KIT ??
  path.join(os.tmpdir(), "opencode", "uat-extension-kit")
const kit = await fs
  .readFile(path.join(kitDir, "kit.json"), "utf8")
  .then((raw) => JSON.parse(raw))
  .catch(() => null)
const daemonURL = `http://127.0.0.1:${daemonPort}`
const daemonSecret = crypto.randomBytes(24).toString("hex")
const upstreamDocuments = kit?.upstream ?? {}

const fakeAudioPath = path.join(runRoot, "microphone.wav")
const sampleRate = 16000
const sampleCount = sampleRate * 3
const wav = Buffer.alloc(44 + sampleCount * 2)
wav.write("RIFF", 0)
wav.writeUInt32LE(wav.length - 8, 4)
wav.write("WAVEfmt ", 8)
wav.writeUInt32LE(16, 16)
wav.writeUInt16LE(1, 20)
wav.writeUInt16LE(1, 22)
wav.writeUInt32LE(sampleRate, 24)
wav.writeUInt32LE(sampleRate * 2, 28)
wav.writeUInt16LE(2, 32)
wav.writeUInt16LE(16, 34)
wav.write("data", 36)
wav.writeUInt32LE(sampleCount * 2, 40)
for (let i = 0; i < sampleCount; i += 1) {
  const active = i >= sampleRate && i < sampleRate * 2
  const sample = active
    ? Math.round(Math.sin((2 * Math.PI * 440 * i) / sampleRate) * 8000)
    : 0
  wav.writeInt16LE(sample, 44 + i * 2)
}
await fs.writeFile(fakeAudioPath, wav)

const completion = (message, id = "chatcmpl-uat") => ({
  id,
  object: "chat.completion",
  created: 0,
  model: "uat-model",
  choices: [
    {
      index: 0,
      message,
      finish_reason: message.tool_calls ? "tool_calls" : "stop",
    },
  ],
  usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
})

// completionStream is the SSE stream of a chat.completion: one chunk per
// choice carrying its whole message as the delta, its tool calls indexed,
// then the usage and [DONE].
const completionStream = (completionBody) => {
  const record = (value) => `data: ${JSON.stringify(value)}\n\n`
  const choices = completionBody.choices.map((choice, index) => {
    const delta = { ...(choice.message ?? {}) }
    if (Array.isArray(delta.tool_calls)) {
      delta.tool_calls = delta.tool_calls.map((call, callIndex) => ({
        index: callIndex,
        ...call,
      }))
    }
    return {
      index: choice.index ?? index,
      delta,
      finish_reason: choice.finish_reason,
    }
  })
  return (
    record({
      id: completionBody.id,
      object: "chat.completion.chunk",
      model: completionBody.model,
      choices,
    }) +
    record({
      id: completionBody.id,
      object: "chat.completion.chunk",
      choices: [],
      usage: completionBody.usage,
    }) +
    "data: [DONE]\n\n"
  )
}

// The daemon's keyless chat provider, as its real upstream answers: a
// stream carries a tool call, its arguments ending in the finishing chunk,
// and a record follows [DONE]; an answer that does not stream carries only
// the text, without the call. UAT_DAEMON_TOOL asks it to write a file.
const daemonChatAnswer = (body) => {
  const messages = body.messages ?? []
  const latestUser = [...messages]
    .reverse()
    .find((message) => message.role === "user")?.content
  const text =
    typeof latestUser === "string" ? latestUser : JSON.stringify(latestUser)
  const wantsTool = (text ?? "").includes("UAT_DAEMON_TOOL")
  const answered = messages.some(
    (message) => message.tool_call_id === "call_uat_daemon",
  )
  const chunk = (delta, finish) =>
    `data: ${JSON.stringify({
      id: "chatcmpl-uat-daemon",
      object: "chat.completion.chunk",
      choices: [
        {
          index: 0,
          delta: { role: "assistant", ...delta },
          ...(finish ? { finish_reason: finish } : {}),
        },
      ],
    })}\n\n`
  if (!body.stream) {
    return {
      type: "application/json",
      body: JSON.stringify(
        completion({
          role: "assistant",
          content:
            wantsTool && !answered ? "I'll write it." : "UAT_DAEMON_PLAIN",
        }),
      ),
    }
  }
  const tail = 'data: [DONE]\n\ndata: {"choices":[],"cost":"0"}\n\n'
  if (wantsTool && !answered) {
    const args = JSON.stringify({
      path: "uat-daemon.txt",
      content: "UAT_DAEMON_CONTENT",
    })
    return {
      type: "text/event-stream",
      body:
        chunk({ content: "I'll write it." }) +
        chunk({
          tool_calls: [
            {
              index: 0,
              id: "call_uat_daemon",
              type: "function",
              function: { name: "write_file", arguments: args.slice(0, -1) },
            },
          ],
        }) +
        chunk(
          {
            tool_calls: [{ index: 0, function: { arguments: args.slice(-1) } }],
          },
          "tool_calls",
        ) +
        tail,
    }
  }
  return {
    type: "text/event-stream",
    body:
      chunk(
        { content: wantsTool ? "UAT_DAEMON_DONE" : "UAT_DAEMON_REPLY" },
        "stop",
      ) + tail,
  }
}

const fakeServer = http.createServer(async (req, res) => {
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const raw = Buffer.concat(chunks)
  res.setHeader("Content-Type", "application/json")

  if (req.url.startsWith("/__upstream/")) {
    const pathname = req.url.split("?")[0]
    observed.upstream.push(`${req.method} ${pathname}`)
    const document = upstreamDocuments[pathname]
    if (req.method === "GET" && document) {
      return res.end(JSON.stringify(document))
    }
    if (req.method === "POST" && kit?.chatPath && pathname === kit.chatPath) {
      const body = JSON.parse(raw.toString() || "{}")
      observed.daemonChat.push({
        stream: body.stream === true,
        messages: body.messages ?? [],
      })
      const answer = daemonChatAnswer(body)
      res.setHeader("Content-Type", answer.type)
      return res.end(answer.body)
    }
    // Anything else a daemon provider asks for is refused locally.
    res.statusCode = 404
    return res.end(JSON.stringify({ error: "uat upstream: not served" }))
  }
  if (req.method === "GET" && req.url === "/v1/models") {
    return res.end(
      JSON.stringify({ data: [{ id: "uat-model", owned_by: "uat" }] }),
    )
  }
  if (req.method === "POST" && req.url === "/v1/chat/completions") {
    const body = JSON.parse(raw.toString())
    observed.chat.push(body)
    if (body.stream) {
      // Asked to stream, the fake answers as an OpenAI-compatible upstream
      // does: its completion becomes a stream of chunks.
      const end = res.end.bind(res)
      res.end = (text) => {
        let payload
        try {
          payload = JSON.parse(text)
        } catch {
          return end(text)
        }
        if (res.statusCode !== 200 || !Array.isArray(payload?.choices))
          return end(text)
        res.setHeader("Content-Type", "text/event-stream")
        return end(completionStream(payload))
      }
    }
    if (req.headers.authorization !== "Bearer uat-secret") {
      // The UI-configured provider is intentionally anonymous.
      if (req.headers.authorization !== undefined) {
        res.statusCode = 401
        return res.end(JSON.stringify({ error: "bad test authorization" }))
      }
    }
    if (observed.failChat) {
      res.statusCode = 503
      return res.end(
        JSON.stringify({ error: { message: "UAT_PROVIDER_DOWN" } }),
      )
    }

    const messages = body.messages ?? []
    const latestUser = [...messages]
      .reverse()
      .find((message) => message.role === "user")?.content
    if (latestUser === "UAT_TOOL_PROMPT") {
      const hasWrite = messages.some(
        (message) => message.tool_call_id === "call_uat_write",
      )
      const hasRead = messages.some(
        (message) => message.tool_call_id === "call_uat_read",
      )
      if (!hasWrite) {
        return res.end(
          JSON.stringify(
            completion(
              {
                role: "assistant",
                content: "",
                tool_calls: [
                  {
                    id: "call_uat_write",
                    type: "function",
                    function: {
                      name: "write_file",
                      arguments: JSON.stringify({
                        path: "uat-tool.txt",
                        content: "UAT_TOOL_CONTENT",
                      }),
                    },
                  },
                ],
              },
              "chatcmpl-uat-write",
            ),
          ),
        )
      }
      if (!hasRead) {
        return res.end(
          JSON.stringify(
            completion(
              {
                role: "assistant",
                content: "",
                tool_calls: [
                  {
                    id: "call_uat_read",
                    type: "function",
                    function: {
                      name: "read_file",
                      arguments: JSON.stringify({ path: "uat-tool.txt" }),
                    },
                  },
                ],
              },
              "chatcmpl-uat-read",
            ),
          ),
        )
      }
      return res.end(
        JSON.stringify(
          completion(
            { role: "assistant", content: "UAT_TOOL_COMPLETE" },
            "chatcmpl-uat-tool-final",
          ),
        ),
      )
    }
    if (latestUser === "UAT_TURN_2") {
      const sawFirstTurn = JSON.stringify(messages).includes("UAT_TURN_1")
      return res.end(
        JSON.stringify(
          completion({
            role: "assistant",
            content: sawFirstTurn ? "UAT_CONTEXT_OK" : "UAT_CONTEXT_MISSING",
          }),
        ),
      )
    }
    const reply =
      latestUser === "UAT_TURN_1" ? "UAT_TURN_1_REPLY" : "UAT_ASSISTANT_REPLY"
    return res.end(
      JSON.stringify(completion({ role: "assistant", content: reply })),
    )
  }
  if (req.method === "POST" && req.url === "/v1/audio/transcriptions") {
    observed.stt += 1
    return res.end(JSON.stringify({ text: "UAT_VOICE_PROMPT" }))
  }
  if (req.method === "POST" && req.url === "/v1/audio/speech") {
    observed.tts.push(JSON.parse(raw.toString()))
    res.setHeader("Content-Type", "audio/wav")
    return res.end(wav)
  }
  if (req.method === "GET" && req.url === "/__uat/requests") {
    return res.end(JSON.stringify(observed))
  }
  if (req.method === "POST" && req.url === "/__uat/reset") {
    observed.chat.length = 0
    observed.stt = 0
    observed.tts.length = 0
    observed.failChat = false
    observed.upstream.length = 0
    observed.daemonChat.length = 0
    return res.end(JSON.stringify({ ok: true }))
  }
  if (req.method === "POST" && req.url === "/__uat/fail-chat") {
    observed.failChat = true
    return res.end(JSON.stringify({ ok: true }))
  }
  if (req.method === "POST" && req.url === "/__uat/recover-chat") {
    observed.failChat = false
    return res.end(JSON.stringify({ ok: true }))
  }
  res.statusCode = 404
  res.end(JSON.stringify({ error: "not found" }))
})
await new Promise((resolve, reject) => {
  fakeServer.once("error", reject)
  fakeServer.listen(fakePort, "127.0.0.1", resolve)
})

const configPath = path.join(home, "config.json")
await fs.writeFile(
  configPath,
  JSON.stringify(
    {
      agents: {
        defaults: {
          workspace,
          restrict_to_workspace: true,
          // The default model selection: an exact target or a route name.
          // The journey starts without one and selects its model in Chat.
          model_name: "",
          max_tokens: 256,
          context_window: 32768,
          max_tool_iterations: 3,
          max_llm_retries: 0,
        },
      },
      gateway: {
        host: "127.0.0.1",
        port: gatewayPort,
        hot_reload: false,
        log_level: "warn",
      },
      // Voice runs on the provider instance the journey connects in Chat's
      // first step; a voice target is only resolved when voice is used.
      voice: {
        enabled: true,
        mode: "cascade",
        stt_target: "uat-provider/uat-whisper",
        tts_target: "uat-provider/uat-tts",
        tts_voice: "uat-voice",
      },
      heartbeat: { enabled: false, interval: 30 },
    },
    null,
    2,
  ),
)

const run = (command, args, options = {}) =>
  new Promise((resolve, reject) => {
    const isWindowsPnpm = process.platform === "win32" && command === "pnpm"
    const executableCommand = isWindowsPnpm ? process.env.ComSpec : command
    const executableArgs = isWindowsPnpm
      ? ["/d", "/s", "/c", "pnpm", ...args]
      : args
    const child = spawn(executableCommand, executableArgs, {
      stdio: "inherit",
      ...options,
    })
    child.once("error", reject)
    child.once("exit", (code) =>
      code === 0 ? resolve() : reject(new Error(`${command} exited ${code}`)),
    )
  })

const kernelPath = path.join(bin, `compa-kernel${executable}`)
const launcherPath = path.join(bin, `compa${executable}`)
const stockDaemonPath = path.join(bin, `extension-daemon${executable}`)
const daemonPath = path.join(bin, `extension-daemon-uat${executable}`)

const stopProcess = async (child) => {
  if (!child || child.exitCode !== null || child.signalCode !== null) return
  const exited = new Promise((resolve) => {
    child.once("exit", resolve)
    setTimeout(resolve, 10_000)
  })
  if (process.platform === "win32") {
    await run("taskkill", ["/PID", String(child.pid), "/T", "/F"]).catch(
      () => {},
    )
  } else {
    child.kill("SIGTERM")
  }
  await exited
}

const startDaemon = (binary, port, extraArgs, env) => {
  const child = spawn(
    binary,
    [
      "-host",
      "127.0.0.1",
      "-port",
      String(port),
      "-secret",
      daemonSecret,
      ...extraArgs,
    ],
    { env, stdio: ["ignore", "pipe", "pipe"] },
  )
  child.stdout.pipe(process.stdout)
  child.stderr.pipe(process.stderr)
  return child
}

const daemonInfo = async (url, timeoutMs = 20_000) => {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${url}/extension/v1/info`, {
        headers: { Authorization: `Bearer ${daemonSecret}` },
        signal: AbortSignal.timeout(2_000),
      })
      if (response.ok) return await response.json()
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 200))
  }
  throw new Error(`extension daemon readiness timed out at ${url}`)
}

const providerShape = (info) =>
  JSON.stringify(
    (info.providers ?? [])
      .map((provider) => ({
        id: provider.id,
        credential: provider.credential,
        surfaces: provider.surfaces,
      }))
      .sort((a, b) => a.id.localeCompare(b.id)),
  )

// Proxies that refuse everything: a daemon provider that bypasses the
// injected client still cannot leave the machine.
const egressBlocked = {
  HTTP_PROXY: `http://127.0.0.1:${blackholePort}`,
  HTTPS_PROXY: `http://127.0.0.1:${blackholePort}`,
  NO_PROXY: "",
}

let launcher
let daemon
let stockDaemon
let failed = false
try {
  await run("pnpm", ["run", "build:backend"], { cwd: frontendDir })
  await run(
    "go",
    ["build", "-tags", "goolm,stdjson", "-o", kernelPath, "./cmd/compa-kernel"],
    { cwd: repoRoot },
  )
  await run(
    "go",
    ["build", "-tags", "goolm,stdjson", "-o", launcherPath, "./web/backend"],
    { cwd: repoRoot },
  )

  // The extension daemon, when a kit describes one: the stock binary, and
  // the kit's UAT entrypoint, which registers the same providers with
  // egress rewritten to the fake upstream.
  if (kit) {
    const extensionRepo = kit.repo
    const goOffline = { ...process.env, GOFLAGS: "", GOPROXY: "off" }
    await run("go", ["build", "-o", stockDaemonPath, kit.command], {
      cwd: extensionRepo,
      env: { ...goOffline, GOWORK: "off" },
    })
    const daemonModule = path.join(runRoot, "extension-daemon-uat")
    await fs.mkdir(daemonModule, { recursive: true })
    const extensionMod = await fs.readFile(
      path.join(extensionRepo, "go.mod"),
      "utf8",
    )
    const extensionGoVersion = extensionMod.match(/^go\s+(\S+)/m)?.[1]
    // The module requires what the daemon requires, so the daemon's go.sum
    // verifies every dependency and nothing is fetched.
    const requires = [
      ...extensionMod.matchAll(/^\s*(?:require\s+)?(\S+)\s+(v\S+)\s*$/gm),
    ]
      .map((match) => `\t${match[1]} ${match[2]}`)
      .join("\n")
    await fs.writeFile(
      path.join(daemonModule, "go.mod"),
      `module compa.uat/extensiondaemon\n\ngo ${extensionGoVersion}\n\nrequire (\n\t${kit.module} v0.0.0\n${requires}\n)\n\nreplace ${kit.module} => ${JSON.stringify(extensionRepo)}\n`,
    )
    await fs.copyFile(
      path.join(extensionRepo, "go.sum"),
      path.join(daemonModule, "go.sum"),
    )
    await fs.copyFile(
      path.join(kitDir, "main.go"),
      path.join(daemonModule, "main.go"),
    )
    await run("go", ["build", "-mod=mod", "-o", daemonPath, "."], {
      cwd: daemonModule,
      env: { ...goOffline, GOWORK: "off" },
    })

    const daemonEnv = { ...process.env, ...egressBlocked }
    const prefixes = kit.envPrefixes ?? []
    for (const key of Object.keys(daemonEnv)) {
      const readByDaemon = prefixes.some((prefix) => key.startsWith(prefix))
      if (
        (readByDaemon || /^(https?_proxy|no_proxy)$/.test(key)) &&
        !(key in egressBlocked)
      )
        delete daemonEnv[key]
    }
    stockDaemon = startDaemon(stockDaemonPath, stockDaemonPort, [], daemonEnv)
    const stockInfo = await daemonInfo(`http://127.0.0.1:${stockDaemonPort}`)
    await stopProcess(stockDaemon)
    daemon = startDaemon(
      daemonPath,
      daemonPort,
      ["-upstream", fakeURL],
      daemonEnv,
    )
    const uatInfo = await daemonInfo(daemonURL)
    if (providerShape(stockInfo) !== providerShape(uatInfo)) {
      throw new Error(
        `UAT daemon providers differ from the stock daemon:\n${providerShape(stockInfo)}\n${providerShape(uatInfo)}`,
      )
    }
  } else {
    console.log(
      `No extension kit at ${kitDir}: the extension daemon journey is skipped.`,
    )
  }

  const env = {
    ...process.env,
    COMPA_HOME: home,
    COMPA_CONFIG: configPath,
    COMPA_BINARY: kernelPath,
    COMPA_GATEWAY_HOST: "127.0.0.1",
    COMPA_UAT_BASE_URL: baseURL,
    COMPA_UAT_FAKE_URL: fakeURL,
    COMPA_UAT_PASSWORD: password,
    COMPA_UAT_WORKSPACE: workspace,
    COMPA_UAT_AUDIO_FILE: fakeAudioPath,
  }
  if (kit) {
    Object.assign(env, {
      COMPA_UAT_EXTENSION_URL: daemonURL,
      COMPA_UAT_EXTENSION_SECRET: daemonSecret,
      // Every upstream path the fake upstream serves the daemon.
      COMPA_UAT_EXTENSION_UPSTREAM: JSON.stringify([
        ...Object.keys(upstreamDocuments).map((pathname) => `GET ${pathname}`),
        ...(kit.chatPath ? [`POST ${kit.chatPath}`] : []),
      ]),
    })
  } else {
    delete env.COMPA_UAT_EXTENSION_URL
    delete env.COMPA_UAT_EXTENSION_SECRET
    delete env.COMPA_UAT_EXTENSION_UPSTREAM
  }
  for (const key of ["OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY"])
    delete env[key]

  launcher = spawn(
    launcherPath,
    [
      "-console",
      "-no-browser",
      "-host",
      "127.0.0.1",
      "-port",
      String(launcherPort),
      configPath,
    ],
    {
      cwd: repoRoot,
      env,
      stdio: ["ignore", "pipe", "pipe"],
    },
  )
  launcher.stdout.pipe(process.stdout)
  launcher.stderr.pipe(process.stderr)

  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseURL}/api/auth/status`)
      if (response.ok) break
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  if (Date.now() >= deadline) throw new Error("launcher readiness timed out")

  const playwrightArgs = [
    "exec",
    "playwright",
    "test",
    "-c",
    "playwright.uat.config.ts",
  ]
  if (process.env.COMPA_UAT_PROJECT) {
    playwrightArgs.push("--project", process.env.COMPA_UAT_PROJECT)
  }
  await run("pnpm", playwrightArgs, { cwd: frontendDir, env })
} catch (error) {
  failed = true
  console.error(error)
  process.exitCode = 1
} finally {
  await stopProcess(launcher)
  await stopProcess(daemon)
  await stopProcess(stockDaemon)
  await new Promise((resolve) => fakeServer.close(resolve))
  if (!failed || process.env.COMPA_UAT_KEEP !== "1") {
    for (let attempt = 0; attempt < 10; attempt += 1) {
      try {
        await fs.rm(runRoot, { recursive: true, force: true })
        break
      } catch (error) {
        if (attempt === 9) throw error
        await new Promise((resolve) => setTimeout(resolve, 250))
      }
    }
  } else {
    console.error(`UAT state retained at ${runRoot}`)
  }
}
