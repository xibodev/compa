import fs from "node:fs/promises"
import path from "node:path"

import {
  type APIRequestContext,
  type Page,
  expect,
  test,
} from "@playwright/test"

import { authenticate } from "./helpers"

const password = process.env.COMPA_UAT_PASSWORD
const fakeURL = process.env.COMPA_UAT_FAKE_URL
const workspace = process.env.COMPA_UAT_WORKSPACE
const daemonURL = process.env.COMPA_UAT_EXTENSION_URL
const daemonSecret = process.env.COMPA_UAT_EXTENSION_SECRET
// The upstream requests the fake upstream serves the daemon, "METHOD path".
const servedUpstream: string[] = JSON.parse(
  process.env.COMPA_UAT_EXTENSION_UPSTREAM ?? "[]",
)
if (!password || !fakeURL || !workspace) {
  throw new Error("UAT environment is incomplete")
}

interface DaemonProvider {
  id: string
  name?: string
  credential?: string
  surfaces?: string[]
}

interface Target {
  target: string
  instance_id: string
  model_id: string
  label?: string
  display_name?: string
  instance_label?: string
  surfaces?: string[]
}

const chatSurfaces = ["chat_completions", "messages", "responses"]
// The state the Extension section shows a provider in until it is signed
// in to, by credential kind.
const stateBeforeSignIn: Record<string, string> = {
  none: "Ready — no sign-in needed",
  token: "Needs a token",
  oauth: "Needs sign-in",
}
// The action a provider's own card offers before it is signed in to.
const signInAction: Record<string, string> = {
  token: "Paste token",
  oauth: "Sign in",
}

// How the Chat model menu names a model.
const modelName = (target: Target) =>
  target.label?.trim() || target.display_name?.trim() || target.model_id

// Mirrors Compa's extension instance naming (web/backend/api/extension.go).
const instanceID = (providerID: string) =>
  "ext-" +
  providerID
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[-._]+|[-._]+$/g, "")

async function daemonInfo(request: APIRequestContext) {
  const response = await request.get(`${daemonURL}/extension/v1/info`, {
    headers: { Authorization: `Bearer ${daemonSecret}` },
  })
  expect(response.ok()).toBe(true)
  const info = (await response.json()) as { providers: DaemonProvider[] }
  expect(info.providers.length).toBeGreaterThan(0)
  return info.providers
}

async function targets(page: Page, all: boolean): Promise<Target[]> {
  const response = await page.request.get(
    `/api/provider-targets${all ? "?all=true" : ""}`,
  )
  expect(response.ok()).toBe(true)
  return (await response.json()).targets ?? []
}

test("extension daemon providers through Models and Chat", async ({
  page,
  request,
}) => {
  test.skip(
    !daemonURL || !daemonSecret,
    "no extension daemon kit: see tests/uat/harness.mjs",
  )
  const browserErrors: string[] = []
  page.on("pageerror", (error) => browserErrors.push(error.message))
  page.on("console", (message) => {
    if (message.type() === "error") browserErrors.push(message.text())
  })
  await request.post(`${fakeURL}/__uat/reset`)
  const providers = await daemonInfo(request)
  const kinds = new Map<string, number>()
  for (const provider of providers) {
    kinds.set(
      provider.credential ?? "",
      (kinds.get(provider.credential ?? "") ?? 0) + 1,
    )
  }
  // The daemon serves every credential kind Compa handles.
  for (const kind of Object.keys(stateBeforeSignIn)) {
    expect(
      kinds.get(kind) ?? 0,
      `providers with credential ${kind}`,
    ).toBeGreaterThan(0)
  }
  const keylessChat = providers.find(
    (provider) =>
      provider.credential === "none" &&
      provider.surfaces?.includes("chat_completions"),
  )
  const speechOnly = providers.find(
    (provider) =>
      provider.credential === "none" &&
      (provider.surfaces ?? []).length > 0 &&
      provider.surfaces?.every((surface) => surface === "audio_speech"),
  )
  expect(keylessChat, "a keyless chat provider").toBeDefined()
  expect(speechOnly, "a speech-only provider").toBeDefined()
  const chatInstance = instanceID(keylessChat!.id)
  const speechInstance = instanceID(speechOnly!.id)

  await authenticate(page, password)

  // 1. Connect the extension under Models: address and secret.
  const extensionStatus = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/extension" &&
      response.request().method() === "GET",
  )
  await page.goto("/models")
  const section = page.getByRole("region", { name: "Extension", exact: true })
  await expect(section).toBeVisible()
  const firstConnection =
    (await (await extensionStatus).json()).status === "not_configured"
  await section
    .getByRole("button", { name: /^(Connect|Edit connection)$/ })
    .click()
  const dialog = page.getByRole("dialog", { name: "Connect the extension" })
  const address = dialog.getByLabel("Address", { exact: true })
  // A first connection starts at the extension's default address.
  if (firstConnection)
    await expect(address).toHaveValue("http://127.0.0.1:18888")
  await address.fill(daemonURL)
  await dialog.getByLabel("Shared secret").fill(daemonSecret)
  await dialog.getByRole("button", { name: "Save" }).click()
  await expect(dialog).toBeHidden({ timeout: 60_000 })
  await expect(page.getByText(/^Connected to the extension\./)).toBeVisible()
  await expect(section.getByText(/^Connected · /)).toBeVisible()

  // 2. The Extension section lists the daemon's providers and their state,
  // and each provider's own card is where it is signed in to.
  const items = section.getByRole("listitem")
  await expect(items).toHaveCount(providers.length)
  for (const provider of providers) {
    const item = section.getByRole("listitem", {
      name: provider.name || provider.id,
      exact: true,
    })
    await expect(item).toContainText(
      stateBeforeSignIn[provider.credential ?? ""],
    )
  }
  for (const kind of ["oauth", "token"]) {
    const provider = providers.find(
      (candidate) => candidate.credential === kind,
    )!
    const name = provider.name || provider.id
    await page
      .getByRole("button", { name: `Manage ${name}`, exact: true })
      .click()
    const card = page.getByRole("region", { name, exact: true })
    await expect(
      card.getByRole("button", { name: signInAction[kind], exact: true }),
    ).toBeVisible()
    // The extension manages the provider: nothing to edit or test by hand.
    for (const absent of ["Edit", "Test connection"]) {
      await expect(
        card.getByRole("button", { name: absent, exact: true }),
      ).toHaveCount(0)
    }
    await card.getByRole("button", { name: "Close" }).click()
  }

  // 3. Keyless providers connect on their own, with a catalog, and say so
  // in one line.
  for (const provider of [keylessChat!, speechOnly!]) {
    await expect(
      section
        .getByRole("listitem", {
          name: provider.name || provider.id,
          exact: true,
        })
        .getByText("Ready — no sign-in needed", { exact: true }),
    ).toBeVisible()
  }
  const instances = (
    await (await page.request.get("/api/provider-instances")).json()
  ).instances as Array<{ id: string; state?: string }>
  for (const id of [chatInstance, speechInstance]) {
    expect(instances.find((instance) => instance.id === id)?.state).toBe(
      "enabled",
    )
  }
  // In the provider list the keyless provider reads by its name, with the
  // free ones, and is connected.
  await expect(
    page
      .getByRole("region", { name: "Free — no key needed" })
      .getByRole("button", {
        name: `Manage ${keylessChat!.name || keylessChat!.id}`,
      }),
  ).toContainText("Connected")
  await expect(page.getByText(chatInstance, { exact: true })).toHaveCount(0)
  const allTargets = await targets(page, true)
  const chatModels = allTargets.filter(
    (target) => target.instance_id === chatInstance,
  )
  const speechModels = allTargets.filter(
    (target) => target.instance_id === speechInstance,
  )
  expect(chatModels.length).toBeGreaterThan(0)
  expect(speechModels.length).toBeGreaterThan(0)
  const chatTarget = chatModels[0]
  expect(
    chatTarget.surfaces?.some((surface) => chatSurfaces.includes(surface)),
  ).toBe(true)
  for (const speech of speechModels) {
    expect(speech.surfaces).toContain("audio_speech")
    expect(
      speech.surfaces?.some((surface) => chatSurfaces.includes(surface)),
    ).toBe(false)
  }

  // 4. The keyless chat model joins Chat and is selectable there. A
  // speech-only model is refused a place in the Chat shortlist.
  const added = await page.request.post("/api/active-models/add", {
    data: { target: chatTarget.target },
  })
  expect(added.ok(), await added.text()).toBe(true)
  const refused = await page.request.post("/api/active-models/add", {
    data: { target: speechModels[0].target },
  })
  expect(refused.status()).toBe(400)
  expect(await refused.text()).toContain("does not serve chat")
  await page.goto("/")
  const selector = page.getByRole("combobox", { name: "Chat model" })
  await selector.click()
  await expect(
    page.getByRole("option", { name: modelName(speechModels[0]), exact: true }),
  ).toHaveCount(0)
  // The menu names models and their providers, never raw instance ids.
  await expect(
    page.getByRole("listbox").getByText(chatInstance, { exact: false }),
  ).toHaveCount(0)
  await page
    .getByRole("option", { name: modelName(chatTarget), exact: true })
    .click()
  await expect(selector).toContainText(modelName(chatTarget))
  await expect(selector).toHaveAttribute("title", chatTarget.target)
  const composer = page.getByPlaceholder("Start a new message…")
  await expect(composer).toBeEnabled()
  const chatTargets = await targets(page, false)
  const selected = chatTargets.find(
    (target) => target.target === chatTarget.target,
  )
  expect(selected, "the selected model is a chat target").toBeDefined()
  expect(
    selected!.surfaces?.some((surface) => chatSurfaces.includes(surface)),
  ).toBe(true)
  expect(
    chatTargets.filter((target) => target.instance_id === speechInstance),
  ).toEqual([])

  // A tool call the daemon's model makes runs: the provider's stream
  // carries it (its plain answer does not), and Compa streams every chat
  // call to the daemon.
  await composer.fill("UAT_DAEMON_TOOL: write the file")
  await page.getByRole("button", { name: "Send message" }).click()
  await expect(
    page.getByText("UAT_DAEMON_DONE", { exact: true }).last(),
  ).toBeVisible({ timeout: 60_000 })
  await expect
    .poll(() =>
      fs
        .readFile(path.join(workspace!, "uat-daemon.txt"), "utf8")
        .catch(() => ""),
    )
    .toBe("UAT_DAEMON_CONTENT")
  const daemonChat = (
    await (await request.get(`${fakeURL}/__uat/requests`)).json()
  ).daemonChat as Array<{ stream: boolean }>
  expect(daemonChat.length).toBeGreaterThanOrEqual(2)
  expect(daemonChat.filter((call) => !call.stream)).toEqual([])

  // 5. The speech-only provider is a voice TTS option, and nothing else.
  const voice = await (await page.request.get("/api/voice/options")).json()
  const optionTargets = (list: Array<{ target: string }> | undefined) =>
    (list ?? []).map((option) => option.target)
  expect(optionTargets(voice.tts)).toEqual(
    expect.arrayContaining(speechModels.map((target) => target.target)),
  )
  for (const list of [voice.stt, voice.stt_chat]) {
    expect(
      optionTargets(list).filter((target) =>
        target.startsWith(`${speechInstance}/`),
      ),
    ).toEqual([])
  }
  // On the Voice page the speech-only provider's voices read by its name.
  await page.goto("/config/voice")
  await page.getByRole("combobox", { name: "Text-to-speech model" }).click()
  const voiceList = page.getByRole("listbox", { name: "Text-to-speech model" })
  const speechVoice = (
    voice.tts as Array<{ target: string; label: string }>
  ).find((option) => option.target === speechModels[0].target)
  await expect(
    voiceList
      .getByRole("option", {
        name: speechVoice?.label ?? speechModels[0].model_id,
      })
      .first(),
  ).toBeVisible()
  await expect(voiceList.getByText(/· extension$/)).toHaveCount(0)
  await page.keyboard.press("Escape")

  // No request left for an upstream the fake upstream does not serve.
  const observed = await (await request.get(`${fakeURL}/__uat/requests`)).json()
  expect(observed.upstream.length).toBeGreaterThan(0)
  for (const call of observed.upstream as string[]) {
    expect(servedUpstream, `daemon upstream request ${call}`).toContain(call)
  }

  // 6. Disconnecting leaves no extension target behind.
  await page.goto("/models")
  await section.getByRole("button", { name: "Disconnect" }).click()
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Disconnect" })
    .click()
  await expect(
    section.getByText("Not connected", { exact: true }),
  ).toBeVisible()
  await expect
    .poll(async () =>
      (await targets(page, false)).filter((target) =>
        target.instance_id.startsWith("ext-"),
      ),
    )
    .toEqual([])
  expect(browserErrors).toEqual([])
})
